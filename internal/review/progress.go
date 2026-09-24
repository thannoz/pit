package review

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/proc"
	"github.com/thannoz/pit/internal/state"
	"github.com/thannoz/pit/internal/workspace"
)

// Mark is how far a reviewer has got with one address.
type Mark int

const (
	// Open is an address not looked at yet.
	Open Mark = iota
	// Looked is one looked at, and nothing that leads to it has changed
	// since.
	Looked
	// Again is one looked at, but the pull request has since changed a
	// file that leads to it. A check that survives a change it never saw is
	// the false confidence a checklist exists to prevent.
	Again
)

// Address is how a check names an item: the methods and the path,
// "GET /orders/{id}", or the path alone. Numbers move when the list
// does; an address does not.
func (it Item) Address() string {
	return strings.TrimSpace(strings.Join(it.Methods, ",") + " " + it.Path)
}

// Looked counts the items looked at, for a line that says how far the
// review has got.
func (c Checklist) Looked() int {
	n := 0
	for _, it := range c.Items {
		if it.Mark == Looked {
			n++
		}
	}
	return n
}

// Record marks the items with these numbers as looked at -- or, with
// done false, as not -- and keeps it in the sandbox's state. It returns
// the sandbox as recorded.
func Record(store *state.Store, box state.Sandbox, list Checklist, numbers []int, done bool) (state.Sandbox, error) {
	var addresses []string
	for _, n := range numbers {
		if n < 1 || n > len(list.Items) {
			return box, errs.New("there is no item %d on the list of #%d", n, box.PR).
				WithHint("the list has %d; `pit what %d` shows them", len(list.Items), box.PR)
		}
		addresses = append(addresses, list.Items[n-1].Address())
	}

	err := store.Update(func(f *state.File) error {
		current, ok := f.Find(box.RepoRef, box.PR)
		if !ok {
			return errs.New("#%d has no sandbox any more", box.PR)
		}
		current.Checked = slices.DeleteFunc(current.Checked, func(c state.Check) bool {
			return slices.Contains(addresses, c.Address)
		})
		// Taking a mark back is kept too, so that a visit from before
		// does not put it back on the next pit what.
		now := time.Now()
		for _, a := range addresses {
			current.Checked = append(current.Checked, state.Check{Address: a, SHA: list.Head, At: now, Undone: !done})
		}
		f.Put(current)
		box = current
		return nil
	})
	return box, err
}

// Progress applies what a reviewer has looked at to a list. A check made
// at an earlier commit still counts unless one of the files that lead
// to the address has changed since; git says which have, once for each
// commit checks were made at.
func Progress(ctx context.Context, git workspace.Runner, repoRoot string, checks []state.Check, list *Checklist) {
	changedSince := map[string][]string{} // commit -> files changed from it to the head
	known := map[string]bool{}
	for _, c := range checks {
		if c.SHA == list.Head || known[c.SHA] {
			continue
		}
		known[c.SHA] = true
		out, err := git.Output(ctx, proc.Command{
			Name: "git", Args: []string{"diff", "--name-only", "-z", c.SHA, list.Head}, Dir: repoRoot,
		})
		if err != nil {
			changedSince[c.SHA] = nil // a commit git no longer has
			continue
		}
		changedSince[c.SHA] = strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
	}

	for i := range list.Items {
		it := &list.Items[i]
		at := slices.IndexFunc(checks, func(c state.Check) bool { return c.Address == it.Address() })
		if at < 0 {
			continue
		}
		c := checks[at]
		if c.Undone {
			continue
		}
		it.CheckedAt, it.Visited = c.SHA, c.Visited
		if c.SHA == list.Head {
			it.Mark = Looked
			continue
		}
		changed, ok := changedSince[c.SHA]
		if changed == nil && ok {
			// The commit is gone -- rebased away. Nothing says what
			// changed, so everything might have.
			it.Mark, it.ChangedSince = Again, it.Files
			continue
		}
		for _, f := range it.Files {
			if slices.Contains(changed, f) {
				it.ChangedSince = append(it.ChangedSince, f)
			}
		}
		it.Mark = Looked
		if len(it.ChangedSince) > 0 {
			it.Mark = Again
		}
	}
}
