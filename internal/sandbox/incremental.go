package sandbox

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/thannoz/pit/internal/config"
	"github.com/thannoz/pit/internal/data"
	"github.com/thannoz/pit/internal/runtime"
	"github.com/thannoz/pit/internal/state"
	"github.com/thannoz/pit/internal/workspace"
)

// build says which services have to be built.
//
// The distinction is between "everything" and "these": all set means
// the whole project, an empty list without it means there is genuinely
// nothing to do, and the two must not be confused into one nil slice.
//
// all is the fallback for when the services cannot be listed at all.
// Everywhere else the list is explicit, because a prebuilt image is
// decided per service and "everything" cannot have one of its members
// removed.
type build struct {
	all      bool
	services []string
}

// everything is what a sandbox that does not exist yet needs: every
// service that has something to build, named, so that individual ones
// can be answered by a registry instead.
func everything(c *config.Config, worktree string) build {
	services, err := buildableServices(c, worktree)
	if err != nil {
		return build{all: true}
	}

	names := make([]string, 0, len(services))
	for _, s := range services {
		names = append(names, s.name)
	}
	return build{services: names}
}

// nothing reports whether the sandbox is already what the new commit
// describes.
func (b build) nothing() bool { return !b.all && len(b.services) == 0 }

// updating returns the sandbox this pull request already has, when
// there is one that can be updated rather than replaced.
//
// "Can be updated" means its containers are still there. Once they are
// gone the volumes went with them, so there is nothing left to keep
// and nothing to be incremental about.
func (m *Manager) updating(ctx context.Context, req UpRequest) (state.Sandbox, bool) {
	f, err := m.Store.Load()
	if err != nil {
		slog.DebugContext(ctx, "cannot read the state, building from scratch", "error", err)
		return state.Sandbox{}, false
	}

	box, ok := f.Current(state.Sandbox{RepoRef: req.Repo.Identity.Ref(), PR: req.PR.Number, Base: req.Base, Check: req.Check})
	if !ok {
		return state.Sandbox{}, false
	}

	statuses, err := m.Runtime.Status(ctx, RuntimeSandbox(box))
	if err != nil || len(statuses) == 0 {
		slog.DebugContext(ctx, "the recorded containers are gone, building from scratch", "error", err)
		return state.Sandbox{}, false
	}
	return box, true
}

// plan works out what a new commit means for the sandbox that exists.
//
// Anything it cannot account for is answered with everything: being
// wrong in that direction costs a rebuild, being wrong in the other
// hands the reviewer a sandbox that does not contain the change.
func (m *Manager) plan(ctx context.Context, req UpRequest, previous state.Sandbox, sha, worktree string) build {
	changed, err := workspace.ChangedFiles(ctx, m.Git, req.Repo, previous.SHA, sha)
	if err != nil {
		slog.DebugContext(ctx, "cannot tell what changed, building everything", "error", err)
		return everything(req.Config, worktree)
	}
	if len(changed) == 0 {
		// Two commits with the same tree: a rebase, an amended
		// message, an empty commit.
		return build{}
	}

	// The compose file describes the services themselves, and pit's
	// own file decides the ports and the environment they run with.
	// Once either has moved, what a diff means cannot be worked out
	// from the diff.
	for _, f := range changed {
		if f == config.FileName || f == req.Config.Devcontainer.File || listed(req.Config.Compose.Files, f) {
			slog.DebugContext(ctx, "the setup itself changed, building everything", "file", f)
			return everything(req.Config, worktree)
		}
	}

	services, err := buildableServices(req.Config, worktree)
	if err != nil {
		slog.DebugContext(ctx, "cannot read the compose files, building everything", "error", err)
		return everything(req.Config, worktree)
	}

	var affected []string
	for _, s := range services {
		if touches(changed, s.dir) {
			affected = append(affected, s.name)
		}
	}
	return build{services: affected}
}

// service is a built service and the directory a change to it would
// live in, relative to the repository root.
type service struct {
	name string
	dir  string
}

// buildableServices lists the services that are built from source.
//
// A service running a published image cannot be changed by a commit,
// so it never needs rebuilding -- and one whose source is mounted
// rather than built picks the new commit up from the worktree without
// anyone doing anything.
func buildableServices(c *config.Config, worktree string) ([]service, error) {
	var names []string
	dirs := map[string]string{}

	for _, file := range c.Compose.Files {
		declared, err := runtime.ReadServices(inWorktree(worktree, file))
		if err != nil {
			return nil, err
		}
		for _, s := range declared {
			// A later file can take the build away, and one that says
			// nothing of it leaves what an earlier file said.
			if s.Unbuilt {
				delete(dirs, s.Name)
				continue
			}
			if s.Context == "" {
				continue
			}
			// The context is relative to its own compose file, and
			// the diff is relative to the repository root.
			dir := filepath.Clean(filepath.Join(filepath.Dir(file), s.Context))
			// A file pit wrote, from a devcontainer.json, names the
			// context where it is.
			if filepath.IsAbs(s.Context) {
				rel, err := filepath.Rel(worktree, s.Context)
				if err != nil {
					return nil, err
				}
				dir = rel
			}
			if _, ok := dirs[s.Name]; !ok {
				names = append(names, s.Name)
			}
			dirs[s.Name] = dir
		}
	}
	var out []service
	for _, n := range names {
		if dir, ok := dirs[n]; ok {
			out = append(out, service{name: n, dir: dir})
		}
	}
	return out, nil
}

// touches reports whether any changed file lies inside dir.
func touches(changed []string, dir string) bool {
	if dir == "." || dir == "" {
		return true // the whole repository is the build context
	}

	prefix := dir + string(filepath.Separator)
	for _, f := range changed {
		if strings.HasPrefix(filepath.Clean(f), prefix) {
			return true
		}
	}
	return false
}

// listed reports whether list contains s. It compares cleaned paths,
// because the compose files are written as the author spelled them and
// the diff spells them its own way.
func listed(list []string, s string) bool {
	for _, item := range list {
		if filepath.Clean(item) == filepath.Clean(s) {
			return true
		}
	}
	return false
}

// recordCommit moves the record to the commit the sandbox now holds.
func (m *Manager) recordCommit(box state.Sandbox, sha string) error {
	return m.Store.Update(func(f *state.File) error {
		current, ok := f.Current(box)
		if !ok {
			return nil
		}
		current.SHA = sha
		f.Put(current)
		return nil
	})
}

// wants reports whether the data should be replaced on a sandbox that
// already had some.
//
// Asking for a particular scenario is an answer in itself: someone who
// types --scenario has said what they want the sandbox to contain.
// Otherwise the reviewer is asked, and silence keeps what is there.
// wantsSnapshot is wants for a snapshot: one the sandbox's data did not
// come from is what was asked for; the one it did, only when asked.
func wantsSnapshot(req UpRequest, previous state.Sandbox) bool {
	if previous.Snapshot != req.Snapshot.ID {
		return true
	}
	if req.Confirm == nil {
		return false
	}
	return req.Confirm(fmt.Sprintf("#%d kept the data it had. Restore %s again?", req.PR.Number, req.Snapshot.Label()))
}

func wants(req UpRequest, sc data.Scenario, previous state.Sandbox) bool {
	if req.Scenario != "" && (req.Scenario != previous.Scenario || previous.Snapshot != "") {
		return true
	}
	if req.Confirm == nil {
		return false
	}
	return req.Confirm(fmt.Sprintf("#%d kept the data it had. Load %s again?", req.PR.Number, sc.Describe()))
}

// within drops whatever is not part of the selection. Building an
// image for a service this review does not start would cost exactly
// as much as building it for one that it does.
func (b build) within(s selection) build {
	if s.whole() || b.all {
		return b
	}

	kept := make([]string, 0, len(b.services))
	for _, name := range b.services {
		if s.has(name) {
			kept = append(kept, name)
		}
	}
	return build{services: kept}
}
