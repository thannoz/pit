package sandbox

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"slices"

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
	return snapshot.Store{Dir: filepath.Join(m.snapshotRoot(), box.RepoRef)}
}

// SnapshotStores are the snapshot stores of every repository, for
// commands that, like pit ls, look at all of them.
func (m *Manager) SnapshotStores() ([]snapshot.Store, error) {
	return snapshot.Stores(m.snapshotRoot())
}

func (m *Manager) snapshotRoot() string { return filepath.Join(m.StateDir, "snapshots") }

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
//
// With consistent, every service that is not one of the databases is
// paused while they are saved, and let go on afterwards whatever
// happened. Nothing then writes between one database's dump and the
// next, so the parts of the snapshot are one moment of the application,
// not several. The services it paused are returned.
func (m *Manager) SaveSnapshot(ctx context.Context, box state.Sandbox, name string, consistent bool, stderr io.Writer) (snapshot.Snapshot, []string, error) {
	cfg, commands, err := snapshotCommands(box)
	if err != nil {
		return snapshot.Snapshot{}, nil, err
	}
	db, _ := config.FindDatabase(cfg.Data.Service, composeDatabases(box.ComposeFiles))
	parts := commands.Each()

	target := commandsIn(box)
	var dumps []snapshot.Dump
	for i, part := range parts {
		path := "data.snapshot.save"
		if len(commands.Parts) > 0 {
			path = fmt.Sprintf("data.snapshot[%d].save", i)
		}
		save := hooks.List{Path: path, Lines: []string{part.Save}}
		dumps = append(dumps, snapshot.Dump{Service: part.Service, Write: func(ctx context.Context, w io.Writer) error {
			return hooks.Run(ctx, m.Proc, save, target, w, stderr)
		}})
	}

	var paused []string
	if consistent {
		paused, err = m.pauseAllBut(ctx, box, parts)
		if err != nil {
			return snapshot.Snapshot{}, nil, err
		}
		defer func() {
			if len(paused) == 0 {
				return
			}
			// Let them go on even when saving was cancelled: a sandbox
			// left frozen is one the reviewer cannot use and cannot
			// see why.
			if uerr := m.Runtime.Unpause(context.WithoutCancel(ctx), RuntimeSandbox(box), paused); uerr != nil && err == nil {
				err = uerr
			}
		}()
	}

	store := m.Snapshots(box)
	store.Limit = cfg.Data.SnapshotMax()
	snap, err := store.Save(ctx, snapshot.Snapshot{
		Name: name, Repo: box.Repo, PR: box.PR, SHA: box.SHA, Scenario: box.Scenario, Service: db.Service,
	}, dumps)
	if err != nil && errs.Hint(err) == "" && ctx.Err() == nil {
		err = errs.Hinted(err, "the command runs in #%d's containers: `pit ls` shows which are running, `pit logs %d <service>` what they say",
			box.PR, box.PR)
	}
	return snap, paused, err
}

// pauseAllBut pauses every running service of a sandbox that is not one
// of the databases being saved, and says which it paused.
func (m *Manager) pauseAllBut(ctx context.Context, box state.Sandbox, parts []config.SnapshotPart) ([]string, error) {
	keep := map[string]bool{}
	for _, p := range parts {
		svc, ok := p.Target()
		if !ok {
			return nil, errs.New("--consistent needs to know which service %q saves, to keep it running", p.Save).
				WithHint("add `service: <name>` to data.snapshot, or save with `compose exec <service> ...`")
		}
		keep[svc] = true
	}
	statuses, err := m.Runtime.Status(ctx, RuntimeSandbox(box))
	if err != nil {
		return nil, err
	}
	var pause []string
	for _, st := range statuses {
		if st.Running() && !keep[st.Service] {
			pause = append(pause, st.Service)
		}
	}
	if len(pause) == 0 {
		return nil, nil
	}
	if err := m.Runtime.Pause(ctx, RuntimeSandbox(box), pause); err != nil {
		return nil, err
	}
	return pause, nil
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

// RestoreSnapshot puts a sandbox's data back into a saved state: the
// snapshot goes to the restore command's stdin.
//
// A snapshot taken at another commit has that commit's schema. The
// migrations of this one run after it, as they would after a scenario,
// so that the code under review meets the tables it expects.
func (m *Manager) RestoreSnapshot(ctx context.Context, box state.Sandbox, snap snapshot.Snapshot, rep Reporter) error {
	if err := CheckSnapshot(box, snap); err != nil {
		return err
	}
	rep.Begin("restore", streaming)
	if err := m.restoreParts(ctx, box, snap, rep); err != nil {
		return err
	}
	rep.Done("%s", snap.Label())

	target := commandsIn(box)
	if snap.SHA != box.SHA {
		cfg, err := ownConfig(box)
		if err != nil {
			return err
		}
		if migrations := hooks.Migrations(cfg.Data.Migrate); !migrations.Empty() {
			rep.Begin("migrate", streaming)
			if err := hooks.Run(ctx, m.Proc, migrations, target, rep.Stdout(), rep.Stderr()); err != nil {
				return err
			}
			rep.Done("%s, as the snapshot is from %s", plural(len(migrations.Lines), "migration", "migrations"), short(snap.SHA))
		}
	}

	// Recorded only now: a record written before would describe a
	// state the sandbox is not in.
	writes := m.baseline(ctx, box, rep)
	return m.Store.Update(func(f *state.File) error {
		current, ok := f.Current(box)
		if !ok {
			return errs.New("#%d is no longer recorded", box.PR)
		}
		current.Scenario, current.Snapshot, current.Writes, current.Edited = snap.Scenario, snap.ID, writes, false
		f.Put(current)
		return nil
	})
}

// CheckSnapshot says whether a snapshot can be restored into a sandbox:
// whether its configuration has a restore command for each part. It is
// what a setup asks before building anything.
func CheckSnapshot(box state.Sandbox, snap snapshot.Snapshot) error {
	_, commands, err := snapshotCommands(box)
	if err != nil {
		return err
	}
	_, err = restorePairs(snap, commands)
	return err
}

// restoreParts feeds each part of a snapshot to its restore command.
func (m *Manager) restoreParts(ctx context.Context, box state.Sandbox, snap snapshot.Snapshot, rep Reporter) error {
	_, commands, err := snapshotCommands(box)
	if err != nil {
		return err
	}
	pairs, err := restorePairs(snap, commands)
	if err != nil {
		return err
	}
	target := commandsIn(box)
	for _, pair := range pairs {
		if err := m.restorePart(ctx, box, snap, pair, target, rep); err != nil {
			return errs.Wrap(err, "restoring %s into #%d failed", snap.Label(), box.PR).
				WithHint("the data of #%d may be half replaced; restoring again, or `pit data reset %d`, puts it into a known state", box.PR, box.PR)
		}
	}
	return nil
}

// ownConfig is the configuration a sandbox was built with: the pull
// request's, or the reviewer's when it carries none.
func ownConfig(box state.Sandbox) (*config.Config, error) {
	for _, dir := range []string{box.Worktree, box.RepoRoot} {
		if cfg, _, err := config.LoadFrom(dir); err == nil {
			return cfg, nil
		}
	}
	return nil, errs.New("cannot read the configuration of #%d", box.PR).
		WithHint("the sandbox was created from %s, which has to still be there", box.RepoRoot)
}

// restorePair is one part of a snapshot and the command that reads it
// back.
type restorePair struct {
	part    snapshot.Part
	restore string
}

// restorePairs matches a snapshot's parts to the restore commands the
// configuration has. A snapshot of one database goes to the one restore
// command there is, whatever either calls the service; with several,
// each part goes to its service's.
func restorePairs(snap snapshot.Snapshot, commands config.Snapshot) ([]restorePair, error) {
	pieces, parts := snap.Pieces(), commands.Each()
	if len(pieces) == 1 && len(parts) == 1 {
		return []restorePair{{part: pieces[0], restore: parts[0].Restore}}, nil
	}
	var out []restorePair
	for _, piece := range pieces {
		i := slices.IndexFunc(parts, func(p config.SnapshotPart) bool { return p.Service == piece.Service })
		if i < 0 {
			return nil, errs.New("%s holds %s, which data.snapshot has no restore command for", snap.Label(), orUnnamed(piece.Service)).
				WithHint("it was saved with other snapshot commands than the ones .pit.yaml has now")
		}
		out = append(out, restorePair{part: piece, restore: parts[i].Restore})
	}
	return out, nil
}

func orUnnamed(service string) string {
	if service == "" {
		return "a database without a service name"
	}
	return service
}

func (m *Manager) restorePart(ctx context.Context, box state.Sandbox, snap snapshot.Snapshot, pair restorePair, target hooks.Sandbox, rep Reporter) error {
	data, err := m.Snapshots(box).Open(snap, pair.part.Service)
	if err != nil {
		return err
	}
	defer func() { _ = data.Close() }()
	cmd, err := hooks.Expand(pair.restore, target)
	if err != nil {
		return errs.Wrap(err, "cannot run data.snapshot's restore")
	}
	cmd.Stdin = data
	return m.Proc.Stream(ctx, cmd, rep.Stdout(), rep.Stderr())
}
