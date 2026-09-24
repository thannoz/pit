package sandbox

import (
	"context"
	"io"
	"path/filepath"

	"github.com/thannoz/pit/internal/config"
	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/hooks"
	"github.com/thannoz/pit/internal/runtime"
	"github.com/thannoz/pit/internal/snapshot"
	"github.com/thannoz/pit/internal/state"
)

// Snapshots is where a repository's snapshots are kept: beside its
// sandboxes rather than inside the directory they share, because a
// snapshot outlives the sandbox it was taken from. Saving one before
// `pit down` is the point of having them.
func (m *Manager) Snapshots(box state.Sandbox) snapshot.Store {
	return snapshot.Store{Dir: filepath.Join(m.StateDir, "snapshots", box.RepoRef)}
}

// SaveSnapshot runs the repository's save command in a sandbox and
// keeps what it writes. What the command says on stderr goes to
// stderr; its stdout is the snapshot.
//
// The commands are the sandbox's own, from the pull request's .pit.yaml
// where it has them: its containers were brought up with that file, and
// a pull request that renames the database service renames it for its
// snapshots too. Where it has none, the reviewer's own file is asked.
// That is the file someone adds them to when pit says they are
// missing, and it would be no help to add them there and be told the
// same again because the pull request carries a .pit.yaml of its own.
func (m *Manager) SaveSnapshot(ctx context.Context, box state.Sandbox, name string, stderr io.Writer) (snapshot.Snapshot, error) {
	cfg, commands, err := snapshotCommands(box)
	if err != nil {
		return snapshot.Snapshot{}, err
	}
	db, _ := config.FindDatabase(cfg.Data.Service, composeDatabases(box.ComposeFiles))

	target := hooks.Sandbox{Project: box.Project, Files: box.ComposeFiles, Dir: box.Worktree}
	save := hooks.List{Path: "data.snapshot.save", Lines: []string{commands.Save}}

	snap, err := m.Snapshots(box).Save(ctx, snapshot.Snapshot{
		Name: name, PR: box.PR, SHA: box.SHA, Scenario: box.Scenario, Service: db.Service,
	}, func(ctx context.Context, w io.Writer) error {
		return hooks.Run(ctx, m.Proc, save, target, w, stderr)
	})
	if err != nil && errs.Hint(err) == "" && ctx.Err() == nil {
		err = errs.Hinted(err, "the command runs in #%d's containers: `pit ls` shows which are running, `pit logs %d <service>` what they say",
			box.PR, box.PR)
	}
	return snap, err
}

// snapshotCommands finds the snapshot commands for a sandbox: in the
// pull request's .pit.yaml, then in the reviewer's. The configuration
// returned is the one they came from.
func snapshotCommands(box state.Sandbox) (*config.Config, config.Snapshot, error) {
	databases := composeDatabases(box.ComposeFiles)
	var first error
	for _, dir := range []string{box.Worktree, box.RepoRoot} {
		cfg, _, err := config.LoadFrom(dir)
		if err != nil {
			continue
		}
		commands, err := cfg.SnapshotCommands(databases)
		if err == nil {
			return cfg, commands, nil
		}
		if first == nil {
			first = err
		}
	}
	if first == nil {
		return nil, config.Snapshot{}, errs.New("cannot read the configuration of #%d", box.PR).
			WithHint("the sandbox was created from %s, which has to still be there", box.RepoRoot)
	}
	return nil, config.Snapshot{}, first
}

// composeDatabases lists the services of a sandbox's compose files with
// the image each runs, the later files overriding the earlier ones as
// Compose merges them. A file pit cannot read -- its own generated
// override has no services of note -- adds nothing; this is for
// suggesting commands, and a suggestion is allowed to know less.
func composeDatabases(files []string) []config.Database {
	var order []string
	images := map[string]string{}
	for _, f := range files {
		services, err := runtime.ReadServices(f)
		if err != nil {
			continue
		}
		for _, s := range services {
			if _, seen := images[s.Name]; !seen {
				order = append(order, s.Name)
			}
			if s.Image != "" || images[s.Name] == "" {
				images[s.Name] = s.Image
			}
		}
	}
	out := make([]config.Database, 0, len(order))
	for _, name := range order {
		out = append(out, config.Database{Service: name, Image: images[name]})
	}
	return out
}
