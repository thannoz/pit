package sandbox

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/thannoz/pit/internal/analysis"
	"github.com/thannoz/pit/internal/config"
	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/state"
	"github.com/thannoz/pit/internal/workspace"
)

// MigrationCheck is what running a pull request's migrations on the
// data of its base showed.
type MigrationCheck struct {
	// BaseSHA and HeadSHA are the commits migrated from and to.
	BaseSHA, HeadSHA string
	// Scenario is the data the base had when the migrations ran.
	Scenario string
	// Migrations are what the pull request does to the migrations.
	Migrations analysis.Migrations
	// Took is how long the migrations ran, until they finished or
	// failed.
	Took time.Duration
	// Failed is why they failed; nil when they ran through.
	Failed error
	// Rows are the rows the base's data had, when data.check counts
	// them.
	Rows int64
	// Losses are the data the migrations took away, and Locks the
	// tables they made others wait for.
	Losses []Loss
	Locks  []Lock
	// Unseen says why pit could not look at the data, when it could
	// not: data.check is not configured, or its commands failed.
	Unseen string
}

// Ran reports whether there was anything to run: a pull request that
// adds or changes no migration has nothing to check.
func (c MigrationCheck) Ran() bool {
	return len(c.Migrations.New) > 0 || len(c.Migrations.Changed) > 0
}

// CheckMigrations runs a pull request's migrations where they will run
// once it is merged: on a database of the branch it goes into, with
// data in it. Every migration passes on an empty database; the one that
// takes a lock for minutes, or fails on a row nobody thought of, only
// shows on one that has rows.
//
// The base is brought up in a sandbox of its own with the scenario,
// then moved to the pull request the way an update would -- what
// changed is rebuilt, the data is kept -- and the migrations run. The
// sandbox is taken down afterwards, whatever happened.
func (m *Manager) CheckMigrations(ctx context.Context, req UpRequest, rep Reporter) (MigrationCheck, error) {
	pr := req.PR.Number
	id := req.Repo.Identity
	var check MigrationCheck

	// Nothing is brought up for a pull request that brings no
	// migrations.
	head, err := workspace.Fetch(ctx, m.Git, req.Repo, pr)
	if err != nil {
		return check, err
	}
	base, err := workspace.FetchBase(ctx, m.Git, req.Repo, pr, req.PR.BaseBranch)
	if err != nil {
		m.dropRefs(ctx, req)
		return check, errs.Wrap(err, "cannot fetch the branch #%d goes into", pr)
	}
	d, err := workspace.Changes(ctx, m.Git, req.Repo, base, head)
	if err != nil {
		m.dropRefs(ctx, req)
		return check, err
	}
	check.BaseSHA, check.HeadSHA = base, head
	check.Migrations = analysis.MigrationsOf(analysis.ClassifyWith(d, analysis.Options{Migrations: req.Config.Data.Migrations}))
	if !check.Ran() {
		m.dropRefs(ctx, req)
		return check, nil
	}

	// What an earlier check left behind -- a crash, a Ctrl+C at the
	// wrong moment -- goes first: its data is not the scenario's.
	slot := state.Sandbox{RepoRef: id.Ref(), PR: pr, Check: true}
	if left, ok := m.recorded(slot); ok {
		_ = m.Down(ctx, left, io.Discard, io.Discard)
	}
	defer func() {
		if box, ok := m.recorded(slot); ok {
			// Taken down even when ctx was cancelled: that is when it
			// matters most.
			_ = m.Down(context.WithoutCancel(ctx), box, io.Discard, io.Discard)
		}
	}()

	req.Check, req.Base = true, true
	req.Confirm, req.OfferSave = nil, nil
	baseBox, err := m.Up(ctx, req, rep)
	if err != nil {
		return check, errs.Wrap(err, "the base of #%d could not be brought up to migrate", pr)
	}
	check.Scenario = baseBox.Scenario

	// What the data is before: to compare with after, and to say how
	// much data the migrations ran on.
	look := checkOf(baseBox.Worktree, req.Config)
	before, seen := m.lookAt(ctx, baseBox, look, &check)
	check.Rows = before.total()

	// To the pull request, keeping the data: it is the scenario the
	// base has, and nobody is asked whether to load it again. The
	// locks are watched while the migrations run, and only then.
	req.Base = false
	var watch *lockWatch
	timer := &stepTimer{Reporter: rep, step: "migrate"}
	if seen && look.Locks != "" {
		timer.onBegin = func() { watch = m.watchLocks(ctx, baseBox, look.Locks, lockLook) }
		timer.onEnd = func() { check.Locks = watch.locks() }
	}
	_, err = m.Up(ctx, req, timer)
	switch {
	case timer.done:
		check.Took = timer.took
	case timer.running:
		timer.end()
		check.Took, check.Failed = time.Since(timer.began), err
		if ctx.Err() != nil {
			return check, ctx.Err()
		}
		return check, nil
	case err == nil:
		return check, errs.New("#%d has no migrations for pit to run", pr).
			WithHint("data.migrate in the .pit.yaml says how to run them")
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		// The migrations ran; what failed after them -- the pull
		// request not answering -- is not theirs, and not this check's.
		rep.Note("the migrations ran; after them: %v", err)
	}
	if seen && ctx.Err() == nil {
		// The pull request's own commands, where it has them: it may
		// be the one that adds them.
		if after, ok := m.lookAt(ctx, baseBox, checkOf(baseBox.Worktree, &config.Config{Data: config.Data{Check: look}}), &check); ok {
			check.Losses = losses(before, after)
		}
	}
	return check, ctx.Err()
}

// lockLook is how often the locks are looked at while migrations run.
const lockLook = 100 * time.Millisecond

// checkOf is the data.check of the configuration in a worktree, or the
// fallback's when it has none.
func checkOf(worktree string, fallback *config.Config) config.Check {
	if cfg, _, err := config.LoadFrom(worktree); err == nil && !cfg.Data.Check.Empty() {
		return cfg.Data.Check
	}
	if fallback != nil {
		return fallback.Data.Check
	}
	return config.Check{}
}

// lookAt asks what the data is, and says in the check why not when it
// cannot.
func (m *Manager) lookAt(ctx context.Context, box state.Sandbox, c config.Check, check *MigrationCheck) (shape, bool) {
	if c.Rows == "" && c.Columns == "" {
		check.Unseen = "data.check is not configured, so pit cannot count what the migrations do to the data"
		return shape{}, false
	}
	s, err := m.shapeOf(ctx, box, c)
	if err != nil {
		check.Unseen = err.Error()
		return shape{}, false
	}
	return s, true
}

// recorded is the sandbox in a slot, if there is one.
func (m *Manager) recorded(slot state.Sandbox) (state.Sandbox, bool) {
	f, err := m.Store.Load()
	if err != nil {
		return state.Sandbox{}, false
	}
	return f.Current(slot)
}

// dropRefs takes away what fetching for a check left, unless a sandbox
// of the pull request needs it.
func (m *Manager) dropRefs(ctx context.Context, req UpRequest) {
	if !m.hasOwn(state.Sandbox{RepoRef: req.Repo.Identity.Ref(), PR: req.PR.Number}) {
		_ = workspace.DeleteRef(ctx, m.Git, req.Repo, req.PR.Number)
	}
}

// stepTimer times one step, passing everything on, and says when it
// begins and ends.
type stepTimer struct {
	Reporter
	step           string
	current        string
	began          time.Time
	took           time.Duration
	running, done  bool
	onBegin, onEnd func()
	ended          bool
}

func (t *stepTimer) Begin(name string, streams bool) {
	t.current = name
	if name == t.step && !t.done && !t.running {
		t.began, t.running = time.Now(), true
		if t.onBegin != nil {
			t.onBegin()
		}
	}
	t.Reporter.Begin(name, streams)
}

func (t *stepTimer) Done(format string, args ...any) {
	if t.current == t.step && t.running {
		t.took, t.running, t.done = time.Since(t.began), false, true
		t.end()
	}
	t.Reporter.Done(format, args...)
}

// end says the step is over, once.
func (t *stepTimer) end() {
	if !t.ended && t.onEnd != nil && !t.began.IsZero() {
		t.ended = true
		t.onEnd()
	}
}
