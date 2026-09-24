package cli

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/sandbox"
	"github.com/thannoz/pit/internal/snapshot"
	"github.com/thannoz/pit/internal/state"
	"github.com/thannoz/pit/internal/ui"
)

// located is a snapshot together with the store it is kept in.
type located struct {
	snapshot.Snapshot
	store snapshot.Store
}

// repo names the repository a snapshot belongs to, the way pit ls does.
// One saved before pit recorded it has only the directory it is in.
func (l located) repo() string {
	if l.Repo != "" {
		return state.Sandbox{Repo: l.Repo}.ShortRepo()
	}
	return filepath.Base(l.store.Dir)
}

// allSnapshots are the snapshots of every repository, newest first.
func allSnapshots(m *sandbox.Manager) ([]located, error) {
	stores, err := m.SnapshotStores()
	if err != nil {
		return nil, err
	}
	var out []located
	for _, st := range stores {
		list, err := st.List()
		if err != nil {
			return nil, err
		}
		for _, snap := range list {
			out = append(out, located{Snapshot: snap, store: st})
		}
	}
	slices.SortStableFunc(out, func(a, b located) int { return b.CreatedAt.Compare(a.CreatedAt) })
	return out, nil
}

func newSnapLsCmd(opts *globalOptions) *cobra.Command {
	return &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List the snapshots",
		Long: `List every snapshot, newest first, with the pull request it was saved
from, its size and its age.

Like pit ls, this shows the snapshots of every repository, not only the
one you are standing in.`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			m, err := manager()
			if err != nil {
				return err
			}
			snaps, err := allSnapshots(m)
			if err != nil {
				return err
			}
			out := ui.New(c.OutOrStdout(), c.ErrOrStderr())
			if opts.jsonOutput {
				rows := make([]snapJSON, 0, len(snaps))
				for _, l := range snaps {
					rows = append(rows, toSnapJSON(l.Snapshot))
				}
				enc := json.NewEncoder(out.Out())
				enc.SetIndent("", "  ")
				return enc.Encode(rows)
			}
			if len(snaps) == 0 {
				out.Println("No snapshots. `pit snap save <pull request number>` makes one.")
				return out.Err()
			}
			return writeSnapTable(out, snaps)
		},
	}
}

func writeSnapTable(out *ui.Printer, snaps []located) error {
	// Counted by where they are kept, not by the name shown: two
	// repositories can share a short name, as pit ls shows too.
	repos := map[string]bool{}
	for _, l := range snaps {
		repos[l.store.Dir] = true
	}
	showRepo := len(repos) > 1

	w := tabwriter.NewWriter(out.Out(), 0, 0, 2, ' ', 0)
	header := []string{"ID", "NAME", "PR", "SIZE", "AGE"}
	if showRepo {
		header = append([]string{"REPO"}, header...)
	}
	_, _ = fmt.Fprintln(w, strings.Join(header, "\t"))
	for _, l := range snaps {
		row := []string{
			l.ID,
			orDash(l.Name),
			"#" + strconv.Itoa(l.PR),
			ui.Size(l.Size),
			shortDuration(time.Since(l.CreatedAt)),
		}
		if showRepo {
			row = append([]string{truncate(l.repo(), maxRepo)}, row...)
		}
		_, _ = fmt.Fprintln(w, strings.Join(row, "\t"))
	}
	if err := w.Flush(); err != nil {
		return errs.Wrap(err, "cannot write the listing")
	}
	return out.Err()
}

func newSnapRmCmd(_ *globalOptions) *cobra.Command {
	var (
		olderThan string
		yes       bool
	)
	cmd := &cobra.Command{
		Use:   "rm [snapshot ...]",
		Short: "Remove snapshots",
		Long: `Remove snapshots, by ID or name, or every one older than an age.

A snapshot a sandbox was restored from can be removed; the sandbox keeps
its data, only restoring it again is no longer possible.

--older-than takes a number with d for days, w for weeks, or any Go
duration: 30d, 2w, 12h. It lists what it would remove and asks first.`,
		Example: `  pit snap rm cart-with-voucher
  pit snap rm sn_7f3a1b sn_09c2d4
  pit snap rm --older-than=30d`,
		RunE: func(c *cobra.Command, args []string) error {
			switch {
			case olderThan != "" && len(args) > 0:
				return errs.New("snapshots and --older-than ask for different things").
					WithHint("use either `pit snap rm %s` or --older-than", args[0])
			case olderThan == "" && len(args) == 0:
				return errs.New("nothing to remove").
					WithHint("name snapshots by ID or name, or pass --older-than=30d; `pit snap ls` lists them")
			}
			m, err := manager()
			if err != nil {
				return err
			}
			all, err := allSnapshots(m)
			if err != nil {
				return err
			}
			out := ui.New(c.OutOrStdout(), c.ErrOrStderr())

			var targets []located
			if olderThan != "" {
				age, err := parseAge(olderThan)
				if err != nil {
					return err
				}
				cutoff := time.Now().Add(-age)
				for _, l := range all {
					if l.CreatedAt.Before(cutoff) {
						targets = append(targets, l)
					}
				}
				if len(targets) == 0 {
					out.Printf("No snapshots older than %s.\n", olderThan)
					return out.Err()
				}
				if !yes {
					out.Printf("This removes %s older than %s:\n", plural(len(targets), "snapshot", "snapshots"), olderThan)
					for _, l := range targets {
						out.Printf("  %s  #%d, %s, %s old\n", l.Label(), l.PR, ui.Size(l.Size), shortDuration(time.Since(l.CreatedAt)))
					}
					if !confirm(c, out, "Remove "+pick(len(targets), "it", "them")+"?") {
						out.Println("Nothing was removed.")
						return out.Err()
					}
				}
			} else {
				// Every name is resolved before anything is removed: a
				// typo in the third one should not leave the first two
				// gone and the command failed.
				for _, ref := range args {
					l, err := findSnapshot(all, ref)
					if err != nil {
						return err
					}
					if !slices.ContainsFunc(targets, func(t located) bool { return t.ID == l.ID && t.store.Dir == l.store.Dir }) {
						targets = append(targets, l)
					}
				}
			}

			var freed int64
			for _, l := range targets {
				if err := l.store.Remove(l.Snapshot); err != nil {
					return err
				}
				freed += l.Size
				out.Printf("Removed %s\n", l.Label())
			}
			if len(targets) > 1 {
				out.Printf("%s freed.\n", ui.Size(freed))
			}
			return out.Err()
		},
	}
	cmd.Flags().StringVar(&olderThan, "older-than", "", "remove every snapshot older than this: 30d, 2w, 12h")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	return cmd
}

// findSnapshot looks a snapshot up by ID or name across repositories.
// A name two repositories both use is not guessed between.
func findSnapshot(all []located, ref string) (located, error) {
	var found []located
	for _, l := range all {
		if l.ID == ref || (l.Name != "" && l.Name == ref) {
			found = append(found, l)
		}
	}
	switch len(found) {
	case 0:
		return located{}, errs.New("there is no snapshot %q", ref).
			WithHint("`pit snap ls` lists them")
	case 1:
		return found[0], nil
	}
	var ids []string
	for _, l := range found {
		ids = append(ids, fmt.Sprintf("%s (%s)", l.ID, l.repo()))
	}
	return located{}, errs.New("%q names snapshots in %d repositories", ref, len(found)).
		WithHint("name the one you mean by its ID: %s", strings.Join(ids, ", "))
}

// parseAge reads an age the way people say one: 30d, 2w, or a Go
// duration.
func parseAge(s string) (time.Duration, error) {
	bad := errs.New("%q is not an age", s).WithHint("write it like 30d, 2w or 12h")
	unit := map[byte]time.Duration{'d': 24 * time.Hour, 'w': 7 * 24 * time.Hour}
	if len(s) > 1 {
		if u, ok := unit[s[len(s)-1]]; ok {
			n, err := strconv.Atoi(s[:len(s)-1])
			if err != nil || n <= 0 {
				return 0, bad
			}
			return time.Duration(n) * u, nil
		}
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, bad
	}
	return d, nil
}

// toSnapJSON is the shape --json promises for a snapshot.
func toSnapJSON(s snapshot.Snapshot) snapJSON {
	return snapJSON{
		ID: s.ID, Name: s.Name, Repo: s.Repo, PR: s.PR, SHA: s.SHA, Scenario: s.Scenario, Service: s.Service,
		Size: s.Size, Raw: s.Raw, TookMS: s.Took.Milliseconds(), CreatedAt: s.CreatedAt.Format(time.RFC3339),
	}
}
