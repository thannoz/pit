package sandbox

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/thannoz/pit/internal/config"
	"github.com/thannoz/pit/internal/data"
	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/forge"
	"github.com/thannoz/pit/internal/hooks"
	"github.com/thannoz/pit/internal/ports"
	"github.com/thannoz/pit/internal/runtime"
	"github.com/thannoz/pit/internal/snapshot"
	"github.com/thannoz/pit/internal/state"
	"github.com/thannoz/pit/internal/workspace"
)

// cleanupBudget is how long the undo of a failed setup may take. It is
// generous because stopping containers is slow, and bounded because a
// cleanup that hangs is worse than one that gives up.
const cleanupBudget = 60 * time.Second

// Reporter is told what Up is doing, so the command can render it. The
// lifecycle knows the steps; how they look on screen is not its
// business.
type Reporter interface {
	// Begin announces that a step has started. streams says whether
	// the step writes output of its own, which decides whether an
	// animation would be overwritten by it.
	Begin(name string, streams bool)
	// Done reports that the step begun last has finished.
	Done(format string, args ...any)
	// Note says something that is not a step: what pit noticed, or
	// decided, on the way. Steps are things that take time; this is
	// for things the reviewer has to know about them.
	Note(format string, args ...any)
	// Stdout and Stderr are where a step's own output goes.
	Stdout() io.Writer
	Stderr() io.Writer
}

// Steps that write output of their own. Naming them here keeps the
// knowledge with the code that runs them.
const (
	quiet     = false
	streaming = true
)

// UpRequest is everything needed to build a sandbox.
type UpRequest struct {
	// Repo is the repository the review happens in.
	Repo workspace.Repo
	// PR is the pull request, already read from the forge.
	PR forge.PR
	// Config is the repository's .pit.yaml.
	Config *config.Config
	// Scenario is the data state the reviewer asked for. Empty means
	// the one the repository configured as its default.
	Scenario string
	// Snapshot, when set, is loaded instead of any scenario: a state
	// someone saved, to start from exactly there.
	Snapshot *snapshot.Snapshot
	// Confirm asks the reviewer a yes-or-no question.
	//
	// It is used in one place: whether to replace data a sandbox
	// already holds. A nil Confirm answers no, because pit does not
	// throw away someone's work on the grounds that nobody was there
	// to object.
	Confirm func(question string) bool
	// OfferSave is called before the data of a running sandbox is
	// replaced by another scenario, when it was changed since it was
	// loaded: the one moment to keep what the reviewer entered. An
	// error leaves the data alone and stops. Nil replaces it without
	// offering anything.
	OfferSave func(box state.Sandbox) error
	// Base brings up the commit the pull request goes into instead of
	// the pull request: the state before it, in a sandbox of its own
	// beside the pull request's.
	Base bool
	// Check brings it up in the slot pit migrate-check uses, apart
	// from the reviewer's sandboxes: at the base with Base, else at the
	// pull request.
	Check bool
}

// slot is where the request brings a sandbox up.
func (r UpRequest) slot() string {
	return state.Sandbox{Base: r.Base, Check: r.Check}.Slot()
}

// Up builds a sandbox for a pull request and records it.
//
// Every step that creates something registers how to undo it. If a
// later step fails -- or the reviewer presses Ctrl+C -- the undos run
// in reverse, so a failed setup leaves the machine as it found it. A
// half-built sandbox is worse than none: it holds a port, a worktree
// and a set of containers that nothing knows about.
func (m *Manager) Up(ctx context.Context, req UpRequest, rep Reporter) (state.Sandbox, error) {
	var undo rollback
	defer func() { undo.run(ctx) }()

	id := req.Repo.Identity
	pr := req.PR.Number

	st := newSteps(rep)
	started := time.Now()

	st.begin("fetch", quiet)
	var sha string
	var err error
	if req.Base {
		// The branch the pull request goes into, as it is now: what
		// merging it would change.
		if sha, err = workspace.FetchBase(ctx, m.Git, req.Repo, pr, req.PR.BaseBranch); err != nil {
			return state.Sandbox{}, errs.Wrap(err, "cannot fetch the branch #%d goes into", pr)
		}
		st.done(ctx, "%s at %s, the base of #%d", orElse(req.PR.BaseBranch, "the default branch"), short(sha), pr)
	} else {
		if sha, err = workspace.Fetch(ctx, m.Git, req.Repo, pr); err != nil {
			return state.Sandbox{}, err
		}
		// The branch it goes into, which `pit what` measures the
		// change from. Not needed to run the sandbox, so not worth
		// failing it: without it, the guide says what it cannot do.
		if _, err := workspace.FetchBase(ctx, m.Git, req.Repo, pr, req.PR.BaseBranch); err != nil {
			rep.Note("could not fetch the branch #%d goes into, so `pit what` cannot tell what it changes: %v", pr, err)
		}
		st.done(ctx, "#%d at %s", pr, short(sha))
	}

	// Before anything is created, and before the ref is registered for
	// cleanup: a sandbox that is already running is the answer, and
	// undoing the fetch would take the ref the running one is on.
	// The probe below uses the reviewer's healthcheck settings, not
	// the pull request's: reading the branch's file first would cost a
	// note and a question for a sandbox pit may not even keep. Being
	// wrong here means building again, which is safe.
	previous, updating := m.updating(ctx, req)
	if updating && previous.SHA == sha && m.answers(ctx, previous, req.Config) {
		// The worktree of the running sandbox is this same commit, so
		// its configuration is the one that governs here too.
		adopted, err := m.adopt(req, previous.Worktree, rep)
		if err != nil {
			return state.Sandbox{}, err
		}
		mine := req.Config
		req.Config = adopted

		scenario, err := m.selectData(mine, req, previous.Worktree, rep)
		if err != nil {
			return state.Sandbox{}, err
		}

		undo.disarm()
		return m.reuse(ctx, previous, scenario, st, req)
	}

	// Nothing that already exists is registered for undoing. A failed
	// setup of a new sandbox should leave the machine as it found it,
	// but a failed update of one the reviewer is working in should
	// leave them what they had, not take it away because a migration
	// in the new commit is broken.
	// The refs are the pull request's, and a base leaves them to the
	// pull request's own sandbox while there is one.
	slot := req.slot()
	if !updating && (slot == "" || !m.hasOwn(state.Sandbox{RepoRef: id.Ref(), PR: pr})) {
		undo.push(func(c context.Context) { _ = workspace.DeleteRef(c, m.Git, req.Repo, pr) })
	}

	st.begin("worktree", quiet)
	var wt workspace.Worktree
	if slot != "" {
		wt, err = workspace.AddWorktreeAt(ctx, m.Git, req.Repo, id.WorktreeDirIn(m.StateDir, pr, slot), pr, sha)
	} else {
		wt, err = workspace.AddWorktree(ctx, m.Git, req.Repo, m.StateDir, pr)
	}
	if err != nil {
		return state.Sandbox{}, err
	}
	if !updating {
		undo.push(func(c context.Context) { _ = workspace.RemoveWorktree(c, m.Git, req.Repo, wt.Path) })
	}
	st.done(ctx, "%s", wt.Path)

	// From here on the pull request's own configuration governs. It is
	// read after the worktree exists because that is where it lives,
	// and before anything is built or started.
	adopted, err := m.adopt(req, wt.Path, rep)
	if err != nil {
		return state.Sandbox{}, err
	}
	mine := req.Config
	req.Config = adopted

	// The scenario is selected against the adopted file, not the
	// reviewer's: a pull request that adds the scenario someone asked
	// for is exactly the case the reviewer's file cannot answer. Still
	// long before anything is built, which is what the check is for.
	// A scenario only the reviewer's file has comes from there.
	scenario, err := m.selectData(mine, req, wt.Path, rep)
	if err != nil {
		return state.Sandbox{}, err
	}

	project, err := runtime.ProjectNameIn(id.Ref(), pr, slot)
	if err != nil {
		return state.Sandbox{}, err
	}

	// A sandbox that is being updated keeps the port it already has.
	// Asking the allocator would move it: the port is bound, by this
	// sandbox's own container, so it looks taken to anyone who asks.
	port := previous.Port
	if !updating {
		key := id.String()
		if slot != "" {
			key += " " + slot
		}
		assigned, err := ports.Reserve(ctx, key, pr, m.portTaken(ctx, state.Sandbox{RepoRef: id.Ref(), PR: pr, Base: req.Base, Check: req.Check}))
		if err != nil {
			return state.Sandbox{}, err
		}
		port = assigned.Port
	}

	repoDir := id.RepoDir(m.StateDir)
	overridePath := runtime.OverridePathIn(repoDir, pr, slot)
	files := append(absoluteFiles(wt.Path, req.Config.Compose.Files), overridePath)
	box := runtime.Sandbox{Project: project, Dir: wt.Path, Files: files}

	// Which services this review needs at all. Everything below is
	// about them only: building an image for a service nobody starts
	// is the purest waste there is.
	chosen, err := selectServices(req.Config, wt.Path)
	if err != nil {
		return state.Sandbox{}, err
	}

	// What has to be built here. For a sandbox that does not exist yet
	// that is every service with a build; for one being updated, the
	// services the new commit can have touched.
	//
	// Unless a pipeline publishes images: then the registry answers
	// the same question better than a diff can, because the image for
	// this commit either exists or it does not.
	work := everything(req.Config, wt.Path)
	if updating && req.Config.Build.Prebuilt == "" {
		work = m.plan(ctx, req, previous, sha, wt.Path)
	}
	work = work.within(chosen)

	// Only for a sandbox that already exists: the override that is
	// already there describes it correctly, and rewriting it would
	// change the configuration of containers this setup is
	// deliberately leaving alone. For a new one there is no file yet,
	// and every compose command below is given it -- including the
	// ones for a project that builds nothing at all.
	if updating && work.nothing() {
		st.begin("build", quiet)
		st.done(ctx, "nothing to rebuild")
	} else {
		st.begin("build", streaming)
		ready, err := m.prepare(ctx, req, box, work, wt.Path, sha, rep)
		if err != nil {
			return state.Sandbox{}, err
		}

		if err := runtime.WriteOverride(overridePath, overrideFor(req.Config, port, ready.images)); err != nil {
			return state.Sandbox{}, err
		}
		if !updating {
			undo.push(func(context.Context) { _ = removeFile(overridePath) })
		}

		if !ready.build.nothing() {
			if err := m.Runtime.Build(ctx, box, ready.build.services, rep.Stdout(), rep.Stderr()); err != nil {
				return state.Sandbox{}, err
			}
		}
		st.done(ctx, "%s", ready.summarise(project))
	}

	st.begin("services", streaming)
	if err := m.Runtime.Up(ctx, box, chosen.names, rep.Stdout(), rep.Stderr()); err != nil {
		return state.Sandbox{}, err
	}
	if !updating {
		undo.push(func(c context.Context) { _ = m.Runtime.Down(c, box, io.Discard, io.Discard) })
	}
	st.done(ctx, "%s", chosen.describe(project))

	if updating {
		// From here on the sandbox holds the new commit, so the record
		// has to say so even if a later step fails.
		if err := m.recordCommit(previous, sha); err != nil {
			return state.Sandbox{}, err
		}
	}

	h := hooks.Sandbox{Project: project, Files: files, Dir: wt.Path}

	// Whether the data a sandbox keeps across an update was written to,
	// asked before the new commit's hooks and migrations write to it
	// themselves: that is the commit's doing, not the reviewer's.
	before := EditUnknown
	if updating {
		before = m.editedSince(ctx, previous)
	}

	after := hooks.AfterUp(req.Config.Hooks.AfterUp)
	if !after.Empty() {
		st.begin("hooks", streaming)
		if err := hooks.Run(ctx, m.Proc, after, h, rep.Stdout(), rep.Stderr()); err != nil {
			return state.Sandbox{}, err
		}
		st.done(ctx, "%s", plural(len(after.Lines), "command", "commands"))
	}

	// The schema before the data, and both as steps of their own. A
	// migration that fails is frequently the change under review;
	// reporting it as "a hook failed" hides the one thing the reviewer
	// most wants to know.
	migrations := hooks.Migrations(req.Config.Data.Migrate)
	if !migrations.Empty() {
		st.begin("migrate", streaming)
		if err := hooks.Run(ctx, m.Proc, migrations, h, rep.Stdout(), rep.Stderr()); err != nil {
			return state.Sandbox{}, err
		}
		st.done(ctx, "%s", plural(len(migrations.Lines), "migration", "migrations"))
	}

	// Applied after the hooks and the migrations, because a fixture
	// that loads before the table it fills exists fails in a way that
	// is tedious to diagnose. Before the healthcheck, so that the
	// moment pit says the sandbox answers, it answers with data.
	loaded, restored, kept := scenario.Name, "", false
	switch {
	case req.Snapshot != nil && (!updating || wantsSnapshot(req, previous)):
		if updating && before == Edited && req.OfferSave != nil {
			if err := req.OfferSave(previous); err != nil {
				return state.Sandbox{}, err
			}
		}
		snap := *req.Snapshot
		st.begin("data", streaming)
		into := state.Sandbox{PR: pr, Base: req.Base, Check: req.Check, RepoRef: id.Ref(), RepoRoot: req.Repo.Root, Project: project, ComposeFiles: files, Worktree: wt.Path, SHA: sha}
		if err := m.restoreParts(ctx, into, snap, rep); err != nil {
			return state.Sandbox{}, err
		}
		st.done(ctx, "snapshot %s, saved in #%d at %s", snap.Label(), snap.PR, short(snap.SHA))
		// Its schema is that of the commit it was saved at.
		if snap.SHA != sha && !migrations.Empty() {
			st.begin("migrate", streaming)
			if err := hooks.Run(ctx, m.Proc, migrations, h, rep.Stdout(), rep.Stderr()); err != nil {
				return state.Sandbox{}, err
			}
			st.done(ctx, "%s again, as the snapshot is from %s", plural(len(migrations.Lines), "migration", "migrations"), short(snap.SHA))
		}
		loaded, restored = snap.Scenario, snap.ID
	case scenario.Empty():
		// Nothing configured, so nothing to say about it. What an
		// update found is still there, and so is where it came from.
		kept = updating
		if updating {
			loaded, restored = previous.Scenario, previous.Snapshot
		}
	case updating && !wants(req, scenario, previous):
		// The data survived the update. Replacing it would throw away
		// whatever the reviewer had done in the sandbox so far.
		loaded, restored, kept = previous.Scenario, previous.Snapshot, true
		st.begin("data", quiet)
		st.done(ctx, "kept as it was")
	default:
		// Replacing what an update would have kept. What it holds was
		// counted before this commit's migrations wrote to it.
		if updating && before == Edited && req.OfferSave != nil {
			if err := req.OfferSave(previous); err != nil {
				return state.Sandbox{}, err
			}
		}
		st.begin("data", streaming)
		target := data.Sandbox{Project: project, Files: files, Dir: wt.Path}
		if err := m.Data.Apply(ctx, target, scenario, rep.Stdout(), rep.Stderr()); err != nil {
			return state.Sandbox{}, err
		}
		st.done(ctx, "scenario %s", scenario.Describe())
	}

	url := runtime.ExpandURL(req.Config.Healthcheck.URL, "localhost", port)
	st.begin("healthy", quiet)
	probe := runtime.Probe{
		URL:          url,
		ExpectStatus: req.Config.Healthcheck.ExpectStatus,
		Timeout:      req.Config.Healthcheck.Timeout.Duration(),
		Interval:     req.Config.Healthcheck.Interval.Duration(),
	}
	if err := m.Runtime.WaitReady(ctx, box, req.Config.Web.Service, probe); err != nil {
		return state.Sandbox{}, err
	}
	// Not the URL: the step line says the sandbox answered, and the
	// URL is the result. Printing it here as well made it appear three
	// times in a row.
	st.done(ctx, "it answers")

	record := state.Sandbox{
		PR:           pr,
		Base:         req.Base,
		Check:        req.Check,
		Repo:         id.String(),
		RepoRef:      id.Ref(),
		RepoRoot:     req.Repo.Root,
		Project:      project,
		WebService:   req.Config.Web.Service,
		ComposeFiles: files,
		Worktree:     wt.Path,
		Port:         port,
		URL:          url,
		SHA:          sha,
		Branch:       req.PR.Branch,
		BaseBranch:   req.PR.BaseBranch,
		Title:        req.PR.Title,
		Author:       req.PR.Author,
		Scenario:     loaded,
		Snapshot:     restored,
		CreatedAt:    time.Now(),
		ProbedAt:     time.Now(),
		Steps:        st.taken,
		SetupMillis:  state.Millis(time.Since(started)),
	}
	if updating {
		// What the reviewer has looked at survives an update; pit what
		// decides which checks the new commit makes stale.
		record.Checked = previous.Checked
	}
	// Counted after the healthcheck, whose request may itself write: a
	// session, a visit. Data that was kept stays edited if it was; if
	// pit could not tell before the update, it cannot tell now either.
	if !kept || before != EditUnknown {
		record.Writes = m.baseline(ctx, record, rep)
		record.Edited = kept && before == Edited
	}
	if err := m.Store.Update(func(f *state.File) error {
		f.Put(record)
		return nil
	}); err != nil {
		return state.Sandbox{}, err
	}

	// Everything worked, so nothing is undone.
	undo.disarm()
	return record, nil
}

// portTaken tells the port allocator about ports pit itself has handed
// out. Without it two sandboxes started in quick succession can pick
// the same number: the first has not bound it yet when the second
// checks.
func (m *Manager) portTaken(ctx context.Context, own state.Sandbox) func(int) bool {
	reserved := map[int]bool{}
	if recorded, err := m.Store.List(); err == nil {
		for _, box := range recorded {
			// Not the sandbox being rebuilt: its own port is the one
			// it should get back. Counting it as taken would move a
			// pull request to a new port every time it is set up
			// again, which is the opposite of what deterministic
			// ports are for -- a browser tab that stays valid.
			if box.Same(own) {
				continue
			}
			reserved[box.Port] = true
		}
	}
	return func(p int) bool {
		return reserved[p] || ports.Taken(ctx, p)
	}
}

func overrideFor(c *config.Config, hostPort int, images map[string]string) runtime.Override {
	return runtime.Override{
		Service:       c.Web.Service,
		HostPort:      hostPort,
		ContainerPort: c.Web.Port,
		Env:           c.Env.Set,
		Images:        images,
	}
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// rollback holds the undo of each step that created something.
type rollback struct {
	steps    []func(context.Context)
	disarmed bool
}

func (r *rollback) push(f func(context.Context)) { r.steps = append(r.steps, f) }

func (r *rollback) disarm() { r.disarmed = true }

// run undoes the steps in reverse.
//
// It deliberately does not use the caller's context. By the time this
// runs that context is usually the reason we are here -- cancelled by
// Ctrl+C -- and every cleanup command would refuse to start. Cleanup
// gets a context of its own, detached but bounded.
func (r *rollback) run(ctx context.Context) {
	if r.disarmed {
		return
	}

	clean, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupBudget)
	defer cancel()

	for i := len(r.steps) - 1; i >= 0; i-- {
		r.steps[i](clean)
	}
}

func removeFile(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return errs.Wrap(err, "cannot remove %s", path)
	}
	return nil
}

// absoluteFiles resolves the configured compose files against the
// worktree. They have to be absolute because the generated override
// lives outside it, and Compose resolves relative paths against the
// first file it was given.
func absoluteFiles(worktree string, files []string) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		if filepath.IsAbs(f) {
			out = append(out, f)
			continue
		}
		out = append(out, filepath.Join(worktree, f))
	}
	return out
}

func orElse(s, fallback string) string {
	if s != "" {
		return s
	}
	return fallback
}
