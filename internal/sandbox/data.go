package sandbox

import (
	"context"

	"github.com/thannoz/pit/internal/data"
	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/state"
)

// ResetData puts a running sandbox back into a named data state.
//
// It re-runs the scenario's commands against the containers that are
// already up. It does not empty the database first, because nothing in
// a .pit.yaml says how to: whether this arrives back at the starting
// point is therefore a property of the commands. One that begins by
// clearing what it is about to fill works any number of times; one
// that only inserts does not.
//
// The sandbox itself is left alone. Rebuilding it would take minutes
// and throw away the one thing that is certainly still correct.
func (m *Manager) ResetData(ctx context.Context, box state.Sandbox, sc data.Scenario, rep Reporter) error {
	rep.Begin("data", streaming)

	target := data.Sandbox{Project: box.Project, Files: box.ComposeFiles, Dir: box.Worktree}
	if err := m.Data.Apply(ctx, target, sc, rep.Stdout(), rep.Stderr()); err != nil {
		return err
	}
	rep.Done("scenario %s", sc.Describe())

	// Recorded only now, because a record written before the commands
	// ran would describe a state the sandbox is not in.
	writes := m.baseline(ctx, box, rep)
	return m.Store.Update(func(f *state.File) error {
		current, ok := f.Find(box.RepoRef, box.PR)
		if !ok {
			return errs.New("#%d is no longer recorded", box.PR)
		}
		current.Scenario, current.Snapshot, current.Writes, current.Edited = sc.Name, "", writes, false
		f.Put(current)
		return nil
	})
}
