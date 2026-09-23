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
	// Confirm asks the reviewer a yes-or-no question.
	//
	// It is used in one place: whether to replace data a sandbox
	// already holds. A nil Confirm answers no, because pit does not
	// throw away someone's work on the grounds that nobody was there
	// to object.
	Confirm func(question string) bool
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

	// Before anything is built: a misspelled --scenario is a typo, and
	// finding it after a five-minute build is an insult. Selecting is
	// pure configuration work and costs nothing here.
	scenario, err := data.Select(req.Config, req.Scenario)
	if err != nil {
		return state.Sandbox{}, err
	}

	st := newSteps(rep)
	started := time.Now()

	st.begin("fetch", quiet)
	sha, err := workspace.Fetch(ctx, m.Git, req.Repo, pr)
	if err != nil {
		return state.Sandbox{}, err
	}
	st.done(ctx, "#%d at %s", pr, short(sha))

	// Before anything is created, and before the ref is registered for
	// cleanup: a sandbox that is already running is the answer, and
	// undoing the fetch would take the ref the running one is on.
	previous, updating := m.updating(ctx, req)
	if updating && previous.SHA == sha && m.answers(ctx, previous, req.Config) {
		undo.disarm()
		return m.reuse(ctx, previous, scenario, st)
	}

	// Nothing that already exists is registered for undoing. A failed
	// setup of a new sandbox should leave the machine as it found it,
	// but a failed update of one the reviewer is working in should
	// leave them what they had, not take it away because a migration
	// in the new commit is broken.
	if !updating {
		undo.push(func(c context.Context) { _ = workspace.DeleteRef(c, m.Git, req.Repo, pr) })
	}

	st.begin("worktree", quiet)
	wt, err := workspace.AddWorktree(ctx, m.Git, req.Repo, m.StateDir, pr)
	if err != nil {
		return state.Sandbox{}, err
	}
	if !updating {
		undo.push(func(c context.Context) { _ = workspace.RemoveWorktree(c, m.Git, req.Repo, wt.Path) })
	}
	st.done(ctx, "%s", wt.Path)

	project, err := runtime.ProjectName(id.Ref(), pr)
	if err != nil {
		return state.Sandbox{}, err
	}

	// A sandbox that is being updated keeps the port it already has.
	// Asking the allocator would move it: the port is bound, by this
	// sandbox's own container, so it looks taken to anyone who asks.
	port := previous.Port
	if !updating {
		assigned, err := ports.Reserve(ctx, id.String(), pr, m.portTaken(ctx, id.Ref(), pr))
		if err != nil {
			return state.Sandbox{}, err
		}
		port = assigned.Port
	}

	repoDir := id.RepoDir(m.StateDir)
	overridePath := runtime.OverridePath(repoDir, pr)
	if err := runtime.WriteOverride(overridePath, overrideFor(req.Config, port)); err != nil {
		return state.Sandbox{}, err
	}
	if !updating {
		undo.push(func(context.Context) { _ = removeFile(overridePath) })
	}

	files := append(absoluteFiles(wt.Path, req.Config.Compose.Files), overridePath)
	box := runtime.Sandbox{Project: project, Dir: wt.Path, Files: files}

	// Which services a new commit can possibly have changed. For a
	// sandbox that does not exist yet the answer is all of them.
	work := everything()
	if updating {
		work = m.plan(ctx, req, previous, sha, wt.Path)
	}

	if work.nothing() {
		st.begin("services", quiet)
		st.done(ctx, "nothing to rebuild")
	} else {
		st.begin("services", streaming)
		if err := m.Runtime.Up(ctx, box, work.services, rep.Stdout(), rep.Stderr()); err != nil {
			return state.Sandbox{}, err
		}
		if !updating {
			undo.push(func(c context.Context) { _ = m.Runtime.Down(c, box, io.Discard, io.Discard) })
		}
		st.done(ctx, "%s", describe(work, project))
	}

	if updating {
		// From here on the sandbox holds the new commit, so the record
		// has to say so even if a later step fails.
		if err := m.recordCommit(previous, sha); err != nil {
			return state.Sandbox{}, err
		}
	}

	h := hooks.Sandbox{Project: project, Files: files, Dir: wt.Path}

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
	loaded := scenario.Name
	switch {
	case scenario.Empty():
		// Nothing configured, so nothing to say about it.
	case updating && !wants(req, scenario, previous):
		// The data survived the update. Replacing it would throw away
		// whatever the reviewer had done in the sandbox so far.
		loaded = previous.Scenario
		st.begin("data", quiet)
		st.done(ctx, "kept as it was")
	default:
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
		Title:        req.PR.Title,
		Author:       req.PR.Author,
		Scenario:     loaded,
		CreatedAt:    time.Now(),
		Steps:        st.taken,
		SetupMillis:  state.Millis(time.Since(started)),
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
func (m *Manager) portTaken(ctx context.Context, repoRef string, pr int) func(int) bool {
	reserved := map[int]bool{}
	if recorded, err := m.Store.List(); err == nil {
		for _, box := range recorded {
			// Not the sandbox being rebuilt: its own port is the one
			// it should get back. Counting it as taken would move a
			// pull request to a new port every time it is set up
			// again, which is the opposite of what deterministic
			// ports are for -- a browser tab that stays valid.
			if box.RepoRef == repoRef && box.PR == pr {
				continue
			}
			reserved[box.Port] = true
		}
	}
	return func(p int) bool {
		return reserved[p] || ports.Taken(ctx, p)
	}
}

func overrideFor(c *config.Config, hostPort int) runtime.Override {
	return runtime.Override{
		Service:       c.Web.Service,
		HostPort:      hostPort,
		ContainerPort: c.Web.Port,
		Env:           c.Env.Set,
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
