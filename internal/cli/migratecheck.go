package cli

import (
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/thannoz/pit/internal/analysis"
	"github.com/thannoz/pit/internal/config"
	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/sandbox"
	"github.com/thannoz/pit/internal/ui"
)

// checkMigrations is Manager.CheckMigrations; a variable so that a test
// of what is printed needs no Docker.
var checkMigrations = func(p *upPlan, scenario string) (sandbox.MigrationCheck, error) {
	cfg, _, err := config.LoadFrom(p.repo.Root)
	if err != nil {
		return sandbox.MigrationCheck{}, err
	}
	return p.m.CheckMigrations(p.c.Context(), sandbox.UpRequest{
		Repo: p.repo, PR: p.pull, Config: cfg, Scenario: scenario,
	}, p.rep)
}

func newMigrateCheckCmd(opts *globalOptions) *cobra.Command {
	o := &upOptions{}
	cmd := &cobra.Command{
		Use:   "migrate-check <pull request number>",
		Short: "Run a pull request's migrations on the data of the branch it goes into",
		Long: `Run a pull request's migrations where they will run once it is merged:
on a database of the branch it goes into, with data in it.

The base is brought up in a sandbox of its own and loaded with a
scenario; then it is moved to the pull request and only the migrations
run, timed. The sandbox is taken down afterwards. Every migration passes
on an empty database; this is the one that has rows.

The sandboxes you review in are left alone.`,
		Example: `  pit migrate-check 482
  pit migrate-check 482 --scenario=bulk`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if opts.base {
				return errs.New("pit migrate-check starts from the base already").WithHint("leave out --base")
			}
			plan, err := planUp(c, o, args[0])
			if err != nil {
				return err
			}
			check, err := checkMigrations(plan, o.scenario)
			if err != nil {
				return err
			}
			out := plan.out
			plan.rep.Blank()
			if opts.jsonOutput {
				if err := writeJSON(out, migrationJSON(plan.pull.Number, plan.pull.BaseBranch, check)); err != nil {
					return err
				}
			} else {
				writeMigrationCheck(out, plan.pull.Number, plan.pull.BaseBranch, check)
			}
			if check.Failed != nil {
				return errs.New("the migrations of #%d fail on the data of its base", plan.pull.Number)
			}
			return out.Err()
		},
	}
	cmd.Flags().StringVar(&o.scenario, "scenario", "", "the data to migrate (default: the one configured as data.default)")
	return cmd
}

// writeMigrationCheck says what was migrated, from what, and how it
// went -- in that order, the way docs/03 draws it.
func writeMigrationCheck(out *ui.Printer, pr int, baseBranch string, c sandbox.MigrationCheck) {
	if !c.Ran() {
		out.Printf("#%d adds no migrations and changes none; there is nothing to check.\n", pr)
		if len(c.Migrations.Removed) > 0 {
			out.Printf("  It removes %s.\n", fileNames(c.Migrations.Removed))
		}
		return
	}
	data := "no scenario"
	if c.Scenario != "" {
		data = "scenario " + quote(c.Scenario)
	}
	if c.Unseen == "" {
		data += " (" + plural(int(c.Rows), "row", "rows") + ")"
	}
	out.Printf("  Base:        %s @ %s, %s\n", orElse(baseBranch, "default branch"), short(c.BaseSHA), data)
	if len(c.Migrations.New) > 0 {
		out.Printf("  %s %s\n", pad(pick(len(c.Migrations.New), "Migration:", "Migrations:"), 12), fileNames(c.Migrations.New))
	}
	if len(c.Migrations.Changed) > 0 {
		out.Printf("  %s %s  (existing, changed: a database that ran them will not again)\n", pad("Changed:", 12), fileNames(c.Migrations.Changed))
	}
	out.Printf("\n")
	if c.Failed != nil {
		out.Printf("  ✗ failed after %s\n", seconds(c.Took))
		out.Printf("    %s\n", firstLine(c.Failed.Error()))
		return
	}
	out.Printf("  ✓ ran through  %s\n", seconds(c.Took))
	for _, l := range c.Locks {
		out.Printf("  ! %s locked %s (%s)\n", l.Table, heldFor(l.Held), waits(l.Mode))
	}
	for _, l := range c.Losses {
		switch {
		case l.Dropped:
			out.Printf("  ! table %s removed — %s gone\n", l.Table, plural(int(l.Rows), "row", "rows"))
		case l.Column != "":
			out.Printf("  ! column %s.%s removed — %s had it\n", l.Table, l.Column, plural(int(l.Rows), "row", "rows"))
		default:
			out.Printf("  ! %s lost %s\n", l.Table, plural(int(l.Rows), "row", "rows"))
		}
	}
	switch {
	case c.Unseen != "":
		out.Printf("  ? %s\n", c.Unseen)
	case len(c.Losses) == 0:
		out.Printf("  ✓ no data lost\n")
	}
	writeRollback(out, c.Rollback)
}

// writeRollback says whether the migrations can be undone.
func writeRollback(out *ui.Printer, r sandbox.Rollback) {
	switch {
	case !r.Ran:
		out.Printf("  ? data.rollback is not configured, so pit did not try to undo the migrations\n")
	case r.Failed != nil:
		out.Printf("  ✗ the rollback failed after %s\n    %s\n", seconds(r.Took), firstLine(r.Failed.Error()))
	default:
		out.Printf("  ✓ rolls back  %s\n", seconds(r.Took))
		for _, l := range r.Leftover {
			out.Printf("  ! after the rollback, %s\n", l)
		}
		if !r.Compared {
			out.Printf("  ? without data.check, pit cannot tell whether the rollback left the schema as it was\n")
		}
	}
	for _, f := range r.MissingDown {
		out.Printf("  ! %s has no down migration beside it\n", f)
	}
}

// heldFor says how long pit saw a lock held: a lock seen at one look
// only was held for less than the time between two.
func heldFor(d time.Duration) string {
	if d <= 0 {
		return "briefly"
	}
	return "for " + seconds(d)
}

// waits says who waits for a lock of a mode: PostgreSQL's names.
func waits(mode string) string {
	if mode == "AccessExclusiveLock" {
		return "reads and writes wait"
	}
	return "writes wait"
}

type migrationCheckJSON struct {
	PR         int          `json:"pr"`
	BaseBranch string       `json:"baseBranch,omitempty"`
	BaseSHA    string       `json:"baseSha"`
	HeadSHA    string       `json:"headSha"`
	Scenario   string       `json:"scenario,omitempty"`
	New        []string     `json:"new"`
	Changed    []string     `json:"changed"`
	Removed    []string     `json:"removed"`
	Ran        bool         `json:"ran"`
	TookMS     int64        `json:"tookMs"`
	Failed     string       `json:"failed,omitempty"`
	Rows       int64        `json:"rows"`
	Losses     []lossJSON   `json:"losses"`
	Locks      []lockJSON   `json:"locks"`
	Unseen     string       `json:"unseen,omitempty"`
	Rollback   rollbackJSON `json:"rollback"`
}

type rollbackJSON struct {
	Ran         bool     `json:"ran"`
	TookMS      int64    `json:"tookMs"`
	Failed      string   `json:"failed,omitempty"`
	Leftover    []string `json:"leftover"`
	Compared    bool     `json:"compared"`
	MissingDown []string `json:"missingDown"`
	Reversible  bool     `json:"reversible"`
}

type lossJSON struct {
	Table   string `json:"table"`
	Column  string `json:"column,omitempty"`
	Rows    int64  `json:"rows"`
	Dropped bool   `json:"dropped,omitempty"`
}

type lockJSON struct {
	Table  string `json:"table"`
	Mode   string `json:"mode"`
	HeldMS int64  `json:"heldMs"`
}

func migrationJSON(pr int, baseBranch string, c sandbox.MigrationCheck) migrationCheckJSON {
	j := migrationCheckJSON{PR: pr, BaseBranch: baseBranch, BaseSHA: c.BaseSHA, HeadSHA: c.HeadSHA, Scenario: c.Scenario,
		New: pathsOf(c.Migrations.New), Changed: pathsOf(c.Migrations.Changed), Removed: pathsOf(c.Migrations.Removed),
		Ran: c.Ran(), TookMS: c.Took.Milliseconds()}
	if c.Failed != nil {
		j.Failed = c.Failed.Error()
	}
	j.Rows, j.Unseen = c.Rows, c.Unseen
	j.Losses, j.Locks = []lossJSON{}, []lockJSON{}
	for _, l := range c.Losses {
		j.Losses = append(j.Losses, lossJSON{Table: l.Table, Column: l.Column, Rows: l.Rows, Dropped: l.Dropped})
	}
	for _, l := range c.Locks {
		j.Locks = append(j.Locks, lockJSON{Table: l.Table, Mode: l.Mode, HeldMS: l.Held.Milliseconds()})
	}
	r := c.Rollback
	j.Rollback = rollbackJSON{Ran: r.Ran, TookMS: r.Took.Milliseconds(), Leftover: append([]string{}, r.Leftover...),
		Compared: r.Compared, MissingDown: append([]string{}, r.MissingDown...), Reversible: r.Reversible()}
	if r.Failed != nil {
		j.Rollback.Failed = r.Failed.Error()
	}
	return j
}

func pathsOf(files []analysis.File) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, f.Path)
	}
	return out
}

func fileNames(files []analysis.File) string {
	return strings.Join(pathsOf(files), ", ")
}

func seconds(d time.Duration) string {
	return d.Round(100 * time.Millisecond).String()
}

func pad(s string, width int) string {
	return s + strings.Repeat(" ", max(0, width-len(s)))
}

func quote(s string) string { return `"` + s + `"` }
