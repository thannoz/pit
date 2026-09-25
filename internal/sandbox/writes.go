package sandbox

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/hooks"
	"github.com/thannoz/pit/internal/state"
)

// Edit is what pit can tell about a sandbox's data since it was loaded.
type Edit int

const (
	// EditUnknown is a sandbox pit cannot ask: it counts no writes,
	// is not running, or its database was restarted and began
	// counting again.
	EditUnknown Edit = iota
	// Unedited data is still what was loaded: a scenario, a snapshot.
	Unedited
	// Edited data has been written to since: the reviewer entered
	// something, or the application wrote on its own.
	Edited
)

// writesTimeout bounds asking a database for its count, which pit ls
// does for every running sandbox. A database that does not answer
// within it is one pit says nothing about, rather than a listing that
// hangs.
const writesTimeout = 10 * time.Second

// countWrites asks each database of a sandbox how many writes it has
// counted, with data.snapshot's writes commands. A sandbox whose
// configuration has none gives nil.
//
// The commands are the repository's own, like save and restore: pit
// knows no database. What it asks of them is a number that grows with
// every write and with nothing else -- reads, dumps and the database's
// own housekeeping leave it alone.
func (m *Manager) countWrites(ctx context.Context, box state.Sandbox) ([]int64, error) {
	_, commands, err := snapshotCommands(box)
	if err != nil {
		// No snapshot commands, so none that count either.
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, writesTimeout)
	defer cancel()

	target := hooks.Sandbox{Project: box.Project, Files: box.ComposeFiles, Dir: box.Worktree}
	var counts []int64
	for i, part := range commands.Each() {
		if part.Writes == "" {
			continue
		}
		path := "data.snapshot.writes"
		if len(commands.Parts) > 0 {
			path = fmt.Sprintf("data.snapshot[%d].writes", i)
		}
		var out, stderr bytes.Buffer
		if err := hooks.Run(ctx, m.Proc, hooks.List{Path: path, Lines: []string{part.Writes}}, target, &out, &stderr); err != nil {
			if msg := strings.TrimSpace(stderr.String()); msg != "" {
				return nil, errs.Wrap(err, "%s", msg)
			}
			return nil, err
		}
		n, err := strconv.ParseInt(strings.TrimSpace(out.String()), 10, 64)
		if err != nil {
			return nil, errs.New("%s printed %q, which is not a number", path, strings.TrimSpace(out.String())).
				WithHint("it should print one number that grows with every write, like a count of the rows written")
		}
		counts = append(counts, n)
	}
	return counts, nil
}

// baseline counts the writes of data that was just loaded, for later
// comparison. A count that fails is said once, here, where it can be
// fixed; pit ls then says nothing about the sandbox.
func (m *Manager) baseline(ctx context.Context, box state.Sandbox, rep Reporter) []int64 {
	counts, err := m.countWrites(ctx, box)
	if err != nil {
		rep.Note("could not count the writes to #%d's data, so `pit ls` cannot tell when it is changed: %v", box.PR, err)
		return nil
	}
	return counts
}

// editedSince says whether a sandbox's data was written to since it was
// loaded: recorded as such, or counted now.
func (m *Manager) editedSince(ctx context.Context, box state.Sandbox) Edit {
	if box.Edited {
		return Edited
	}
	return m.edited(ctx, box)
}

// EditedNow says whether a running sandbox's data was changed since it
// was loaded, as pit ls would. Nothing is stopped for it, so a write
// PostgreSQL has not accounted for yet can be missing; for a sandbox
// that is about to go, EditedBeforeDown is exact.
func (m *Manager) EditedNow(ctx context.Context, box state.Sandbox) Edit {
	return m.editedSince(ctx, box)
}

// edited compares a sandbox's count of writes now with the one taken
// when its data was loaded, or since then when an update kept it.
func (m *Manager) edited(ctx context.Context, box state.Sandbox) Edit {
	if len(box.Writes) == 0 {
		return EditUnknown
	}
	now, err := m.countWrites(ctx, box)
	if err != nil {
		return EditUnknown
	}
	return compareWrites(box.Writes, now)
}

// compareWrites says whether counts taken now show writes since the
// baseline. A count that went down belongs to a database that was
// restarted and counts from zero again: whatever it counted before is
// gone, and pit cannot tell -- unless another database's count shows
// writes anyway.
func compareWrites(baseline, now []int64) Edit {
	if len(baseline) != len(now) {
		return EditUnknown
	}
	result := Unedited
	for i := range now {
		switch {
		case now[i] > baseline[i]:
			return Edited
		case now[i] < baseline[i]:
			result = EditUnknown
		}
	}
	return result
}

// EditedBeforeDown says whether removing a sandbox loses data written
// since it was loaded, and whether it is running, which saving it
// needs.
//
// The services that are not its databases are stopped first. An
// application keeps its connections open, and a database may account
// for what a connection wrote only later: PostgreSQL within ten seconds
// of the connection going idle, or when it closes. A count taken while
// the application runs could miss the order entered a moment ago, and
// that is exactly the one someone removing a sandbox forgets. The
// sandbox is about to go, so stopping them costs nothing, and a
// snapshot saved afterwards is of one moment.
func (m *Manager) EditedBeforeDown(ctx context.Context, box state.Sandbox) (Edit, bool) {
	statuses, err := m.Runtime.Status(ctx, RuntimeSandbox(box))
	running := err == nil && (Entry{Services: statuses}).AnyRunning()
	switch {
	case box.Edited:
		return Edited, running
	case len(box.Writes) == 0 || !running:
		return EditUnknown, running
	}
	if keep, ok := databaseServices(box); ok {
		var stop []string
		for _, st := range statuses {
			if st.Running() && !keep[st.Service] {
				stop = append(stop, st.Service)
			}
		}
		if len(stop) > 0 {
			// Counting without stopping them is still worth more than
			// not counting.
			_ = m.Runtime.Stop(ctx, RuntimeSandbox(box), stop)
		}
	}
	return m.edited(ctx, box), running
}

// databaseServices are the services data.snapshot works on, when each
// of its entries says which.
func databaseServices(box state.Sandbox) (map[string]bool, bool) {
	_, commands, err := snapshotCommands(box)
	if err != nil {
		return nil, false
	}
	keep := map[string]bool{}
	for _, part := range commands.Each() {
		svc, ok := part.Target()
		if !ok {
			return nil, false
		}
		keep[svc] = true
	}
	return keep, len(keep) > 0
}
