// Package sandbox ties the pieces together: git, containers and the
// record of what exists. It is the layer the commands call into, so
// that internal/cli stays about flags and this stays about behaviour.
package sandbox

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sync/errgroup"

	"github.com/thannoz/pit/internal/data"
	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/hooks"
	"github.com/thannoz/pit/internal/runtime"
	"github.com/thannoz/pit/internal/secrets"
	"github.com/thannoz/pit/internal/state"
	"github.com/thannoz/pit/internal/workspace"
)

// statusConcurrency bounds how many runtimes are asked at once. Enough
// that a listing feels instant, few enough not to flood the daemon.
const statusConcurrency = 8

// Manager owns the lifecycle of sandboxes.
type Manager struct {
	// Store is the record of what exists.
	Store *state.Store
	// Runtime starts and stops services.
	Runtime runtime.Runtime
	// Git runs git.
	Git workspace.Runner
	// Proc runs the repository's configured hooks.
	Proc hooks.Runner
	// Data puts a sandbox into a named data state.
	Data data.Store
	// Secrets asks 1Password and Vault for what env.secrets names.
	Secrets secrets.Runner
	// StateDir is where worktrees and generated files live.
	StateDir string
}

// Entry is a recorded sandbox together with what it is actually doing.
type Entry struct {
	state.Sandbox

	// Services is what the runtime reports, or nil when it could not
	// be asked.
	Services []runtime.Status
	// Unreachable holds the reason the runtime could not be asked,
	// which is itself worth showing.
	Unreachable error
	// Edited says whether the data was written to since it was
	// loaded, where the sandbox is running and can be asked.
	Edited Edit
}

// Running reports whether every recorded service is up. A sandbox with
// one exited container is not running, however good the rest looks.
func (e Entry) Running() bool {
	if len(e.Services) == 0 {
		return false
	}
	for _, s := range e.Services {
		if !s.Running() {
			return false
		}
	}
	return true
}

// AnyRunning reports whether at least one service is up.
//
// It is a weaker question than Running, and the right one before
// running a command inside the sandbox: a project where a one-shot
// service has finished on purpose is not broken, but one where
// everything has exited has nothing to run a command in.
func (e Entry) AnyRunning() bool {
	for _, s := range e.Services {
		if s.Running() {
			return true
		}
	}
	return false
}

// Status is a word for what the sandbox is doing.
func (e Entry) Status() string {
	switch {
	case e.Unreachable != nil:
		return "unknown"
	case e.Running():
		return "running"
	case len(e.Services) == 0:
		return "gone"
	default:
		return "stopped"
	}
}

// List returns every recorded sandbox with its live state.
//
// The runtime is asked about each sandbox at the same time rather than
// one after another: with several reviews open, doing it in sequence
// would make `pit ls` feel slow for no reason.
func (m *Manager) List(ctx context.Context) ([]Entry, error) {
	recorded, err := m.Store.List()
	if err != nil {
		return nil, err
	}

	entries := make([]Entry, len(recorded))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(statusConcurrency)

	for i, box := range recorded {
		g.Go(func() error {
			entries[i] = m.describe(gctx, box)
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return entries, nil
}

// hasOwn reports whether the pull request of a base sandbox has a
// sandbox of its own too.
func (m *Manager) hasOwn(base state.Sandbox) bool {
	f, err := m.Store.Load()
	if err != nil {
		return true // keeping a ref is the harmless mistake
	}
	_, ok := f.Find(base.RepoRef, base.PR)
	return ok
}

// FindBox is Find for the sandbox a record is of: a pull request's
// own, or its base.
func (m *Manager) FindBox(ctx context.Context, box state.Sandbox) (Entry, error) {
	f, err := m.Store.Load()
	if err != nil {
		return Entry{}, err
	}
	current, ok := f.Current(box)
	if !ok {
		return Entry{}, errs.New("there is no sandbox for %s", box.Name()).
			WithHint("`pit ls` shows what exists")
	}
	return m.describe(ctx, current), nil
}

// Find returns one recorded sandbox with its live state.
func (m *Manager) Find(ctx context.Context, repoRef string, pr int) (Entry, error) {
	f, err := m.Store.Load()
	if err != nil {
		return Entry{}, err
	}

	box, ok := f.Find(repoRef, pr)
	if !ok {
		return Entry{}, errs.New("there is no sandbox for #%d", pr).
			WithHint("`pit ls` shows what exists")
	}
	return m.describe(ctx, box), nil
}

// describe asks the runtime what a recorded sandbox is doing. A runtime
// that cannot be reached is reported per sandbox rather than failing
// the whole listing: `pit ls` should still work when Docker is off.
func (m *Manager) describe(ctx context.Context, box state.Sandbox) Entry {
	e := Entry{Sandbox: box}

	statuses, err := m.Runtime.Status(ctx, RuntimeSandbox(box))
	if err != nil {
		e.Unreachable = err
		return e
	}
	e.Services = statuses
	switch {
	case box.Edited:
		e.Edited = Edited
	case e.Running():
		e.Edited = m.edited(ctx, box)
	}
	return e
}

// Down removes everything a sandbox consists of: its containers,
// networks and volumes, its worktree, the ref it was fetched into, the
// generated override file, and finally the record itself.
//
// Every step runs even when an earlier one failed, because a teardown
// that stops at the first problem leaves more behind than one that
// keeps going. The record is dropped only when nothing failed: an entry
// pointing at containers that are still there is more useful than no
// entry at all.
func (m *Manager) Down(ctx context.Context, box state.Sandbox, stdout, stderr io.Writer) error {
	var failures []error

	if err := m.Runtime.Down(ctx, RuntimeSandbox(box), stdout, stderr); err != nil {
		failures = append(failures, err)
	}

	if box.RepoRoot != "" {
		repo := workspace.Repo{Root: box.RepoRoot}
		if err := workspace.RemoveWorktree(ctx, m.Git, repo, box.Worktree); err != nil {
			failures = append(failures, err)
		}
		// The refs are the pull request's: a base sandbox leaves them
		// to the pull request's own, while there is one.
		if box.Slot() == "" || !m.hasOwn(box) {
			if err := workspace.DeleteRef(ctx, m.Git, repo, box.PR); err != nil {
				failures = append(failures, err)
			}
		}
	}

	for _, generated := range []string{
		runtime.OverridePathIn(m.RepoDir(box), box.PR, box.Slot()),
		runtime.DevcontainerPathIn(m.RepoDir(box), box.PR, box.Slot()),
		PlanPathIn(m.RepoDir(box), box.PR, box.Slot()),
		KubePlanPathIn(m.RepoDir(box), box.PR, box.Slot()),
		kustomizationFor(KubePlanPathIn(m.RepoDir(box), box.PR, box.Slot())),
	} {
		if err := os.RemoveAll(generated); err != nil {
			failures = append(failures, errs.Wrap(err, "cannot remove %s", generated))
		}
	}
	// An empty repository directory is litter; a non-empty one holds
	// another review, and os.Remove refuses it for us.
	_ = os.Remove(m.RepoDir(box))

	if len(failures) > 0 {
		return errs.Wrap(errors.Join(failures...), "#%d was not fully removed", box.PR).
			WithHint("the record is kept so you can try again; `docker ps` and `git worktree list` show what is left")
	}

	return m.Store.Update(func(f *state.File) error {
		f.RemoveBox(box)
		return nil
	})
}

// RepoDir is where everything for one repository lives.
func (m *Manager) RepoDir(box state.Sandbox) string {
	return filepath.Join(m.StateDir, box.RepoRef)
}

// RuntimeSandbox rebuilds what the runtime needs from what was
// recorded. The compose files come from the record rather than from
// today's configuration: a sandbox has to be taken down the way it was
// brought up, even if someone has edited .pit.yaml since.
func RuntimeSandbox(box state.Sandbox) runtime.Sandbox {
	return runtime.Sandbox{
		Project:    box.Project,
		Dir:        box.Worktree,
		Files:      box.ComposeFiles,
		Processes:  box.Processes,
		Kubernetes: box.Kubernetes,
	}
}

// CommandEnv is what a command run for a sandbox of processes is
// given: its port, its name, and what .pit.yaml sets.
func CommandEnv(box state.Sandbox) []string { return commandsIn(box).Env }

// CommandWrap is what a command run for a sandbox of processes is put
// behind: the dev shell of the pull request's flake or devenv, if it
// has one.
func CommandWrap(box state.Sandbox) []string { return commandsIn(box).Wrap }

// commandsIn is where a sandbox's configured commands run: through its
// compose project, or, for a sandbox of processes, in its worktree with
// what its processes are given.
func commandsIn(box state.Sandbox) hooks.Sandbox {
	h := hooks.Sandbox{Project: box.Project, Files: box.ComposeFiles, Dir: box.Worktree}
	switch {
	case box.Processes:
		h.Env, h.Wrap = processTarget(box.ComposeFiles)
	case box.Kubernetes:
		h.Kubectl, h.Env = kubeTarget(box.ComposeFiles)
	}
	return h
}

// Gone reports whether the record describes something that no longer
// exists. After a reboot the containers are gone but the record, the
// worktree and the generated files are all still there, so a listing
// that trusted the record alone would be lying.
func (e Entry) Gone() bool {
	return e.Unreachable == nil && len(e.Services) == 0
}

// Stale returns the recorded sandboxes whose containers are gone.
func Stale(entries []Entry) []Entry {
	var out []Entry
	for _, e := range entries {
		if e.Gone() {
			out = append(out, e)
		}
	}
	return out
}
