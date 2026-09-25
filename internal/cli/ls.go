package cli

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/sandbox"
	"github.com/thannoz/pit/internal/state"
	"github.com/thannoz/pit/internal/ui"
)

func newLsCmd(opts *globalOptions) *cobra.Command {
	return &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List the sandboxes that exist",
		Long: `List every sandbox pit knows about, with what it is actually doing.

The state is global, so this shows sandboxes from every repository you
have reviewed, not only the one you are standing in.`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return runLs(c, opts)
		},
	}
}

func runLs(c *cobra.Command, opts *globalOptions) error {
	m, err := manager()
	if err != nil {
		return err
	}

	entries, err := m.List(c.Context())
	if err != nil {
		return err
	}

	out := ui.New(c.OutOrStdout(), c.ErrOrStderr())
	if opts.jsonOutput {
		return writeLsJSON(out, entries)
	}
	return writeLsTable(out, entries)
}

// lsRow is the shape `--json` promises. It is a type of its own rather
// than the internal one, so that renaming a field inside pit does not
// silently break someone's script.
type lsRow struct {
	PR       int    `json:"pr"`
	Repo     string `json:"repo"`
	Branch   string `json:"branch,omitempty"`
	Title    string `json:"title,omitempty"`
	Status   string `json:"status"`
	URL      string `json:"url,omitempty"`
	Port     int    `json:"port,omitempty"`
	Scenario string `json:"scenario,omitempty"`
	Snapshot string `json:"snapshot,omitempty"`
	// Edited is whether the data was written to since it was loaded;
	// absent where pit cannot tell.
	Edited    *bool    `json:"edited,omitempty"`
	Base      bool     `json:"base,omitempty"`
	CreatedAt string   `json:"createdAt"`
	Services  []string `json:"services,omitempty"`
}

func writeLsJSON(out *ui.Printer, entries []sandbox.Entry) error {
	rows := make([]lsRow, 0, len(entries))
	for _, e := range entries {
		services := make([]string, 0, len(e.Services))
		for _, s := range e.Services {
			services = append(services, s.Service+"="+s.State)
		}
		rows = append(rows, lsRow{
			PR:        e.PR,
			Repo:      e.Repo,
			Branch:    e.Branch,
			Title:     e.Title,
			Status:    e.Status(),
			URL:       e.URL,
			Port:      e.Port,
			Scenario:  e.Scenario,
			Snapshot:  e.Snapshot,
			Edited:    edited(e.Edited),
			Base:      e.Base,
			CreatedAt: e.CreatedAt.UTC().Format(time.RFC3339),
			Services:  services,
		})
	}

	enc := json.NewEncoder(out.Out())
	enc.SetIndent("", "  ")
	return enc.Encode(rows)
}

func writeLsTable(out *ui.Printer, entries []sandbox.Entry) error {
	if len(entries) == 0 {
		out.Println("No sandboxes. Run `pit <pull request number>` to start one.")
		return nil
	}

	// The repository only earns a column when there is more than one:
	// with a single project it is the same word on every line.
	showRepo := len(distinctRepos(entries)) > 1
	// The data state only earns one when something loaded one. Most
	// projects have no scenarios at all, and a column of dashes is
	// width spent on nothing.
	showScenario := anyScenario(entries)

	w := tabwriter.NewWriter(out.Out(), 0, 0, 2, ' ', 0)
	header := []string{"PR", "TITLE", "BRANCH", "STATUS", "URL", "AGE"}
	if showScenario {
		header = slices.Insert(header, 3, "SCENARIO")
	}
	if showRepo {
		header = append([]string{"REPO"}, header...)
	}
	// tabwriter buffers, so these cannot fail in a way worth checking
	// here; a broken pipe or a full disk surfaces at Flush below.
	_, _ = fmt.Fprintln(w, strings.Join(header, "\t"))

	for _, e := range entries {
		number, branch := "#"+strconv.Itoa(e.PR), e.Branch
		if e.Base {
			// The branch it runs is the one the pull request goes into.
			number, branch = number+" base", orElse(e.BaseBranch, "default branch")
		}
		row := []string{
			number,
			orDash(truncate(e.Title, maxTitle)),
			orDash(branch),
			e.Status(),
			orDash(e.URL),
			shortDuration(time.Since(e.CreatedAt)),
		}
		if showScenario {
			origin := orDash(truncate(dataOrigin(e.Sandbox), maxScenario))
			if e.Edited == sandbox.Edited {
				origin += " +edited"
			}
			row = slices.Insert(row, 3, origin)
		}
		if showRepo {
			row = append([]string{truncate(e.ShortRepo(), maxRepo)}, row...)
		}
		_, _ = fmt.Fprintln(w, strings.Join(row, "\t"))
	}
	if err := w.Flush(); err != nil {
		return errs.Wrap(err, "cannot write the listing")
	}

	// A runtime that could not be reached is worth saying once, below
	// the table, rather than in every row.
	for _, e := range entries {
		if e.Unreachable != nil {
			out.Warnf("could not ask the runtime about #%d: %v", e.PR, e.Unreachable)
		}
	}

	// A reboot takes the containers but leaves the record, the worktree
	// and the generated files. Saying so, and saying what to do about
	// it, is the difference between a stale listing and a useful one.
	if stale := sandbox.Stale(entries); len(stale) > 0 {
		out.Printf("\n%s no longer running, but %s worktree and files are still on disk.\n",
			plural(len(stale), "sandbox is", "sandboxes are"),
			pick(len(stale), "its", "their"))
		out.Printf("Remove %s with `pit down --gone`.\n", pick(len(stale), "it", "them"))
	}
	return nil
}

// dataOrigin is where a sandbox's data came from: the snapshot it was
// restored from, which is more than the scenario that snapshot was
// taken on, or else the scenario.
func dataOrigin(box state.Sandbox) string {
	if box.Snapshot != "" {
		return box.Snapshot
	}
	return box.Scenario
}

// edited is an Edit as JSON has it: true, false, or nothing where pit
// cannot tell.
func edited(e sandbox.Edit) *bool {
	switch e {
	case sandbox.Edited:
		return new(true)
	case sandbox.Unedited:
		return new(false)
	}
	return nil
}

func anyScenario(entries []sandbox.Entry) bool {
	for _, e := range entries {
		if dataOrigin(e.Sandbox) != "" {
			return true
		}
	}
	return false
}

func distinctRepos(entries []sandbox.Entry) map[string]bool {
	repos := map[string]bool{}
	for _, e := range entries {
		repos[e.Repo] = true
	}
	return repos
}

// maxTitle keeps the table narrow enough to read in a normal terminal.
// A pull request title can be a paragraph; the first few words are what
// makes it recognisable.
const (
	maxTitle    = 32
	maxRepo     = 24
	maxScenario = 16
)

func truncate(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max-1]) + "…"
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// shortDuration renders an age the way a person would say it.
func shortDuration(d time.Duration) string {
	switch {
	case d < 0:
		return "0s"
	case d < time.Minute:
		return strconv.Itoa(int(d.Seconds())) + "s"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + "m"
	case d < 24*time.Hour:
		return strconv.Itoa(int(d.Hours())) + "h"
	default:
		return strconv.Itoa(int(d.Hours()/24)) + "d"
	}
}
