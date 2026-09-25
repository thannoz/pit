package cli

import (
	"github.com/spf13/cobra"

	"github.com/thannoz/pit/internal/data"
	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/state"
	"github.com/thannoz/pit/internal/ui"
)

// planUp and bringUp are prepareUp and upPlan.up; variables so that a
// test of what compare asks for needs no forge and no Docker.
var (
	planUp  = prepareUp
	bringUp = func(p *upPlan, base bool, scenario string) (state.Sandbox, error) { return p.up(base, scenario) }
)

func newCompareCmd(opts *globalOptions) *cobra.Command {
	o := &upOptions{}
	cmd := &cobra.Command{
		Use:   "compare <pull request number>",
		Short: "Run a pull request and the branch it goes into side by side, on the same data",
		Long: `Bring up a pull request and the branch it goes into, each in a
sandbox of its own, with the same data: the question "is this new, or
was it always like that?" is answered by opening both.

The pull request's sandbox comes first; the base gets the scenario it
has, or the one --scenario names, or the snapshot --snapshot names.`,
		Example: `  pit compare 482
  pit compare 482 --scenario=refunded`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if opts.base {
				return errs.New("pit compare brings up the base already").WithHint("leave out --base")
			}
			plan, err := planUp(c, o, args[0])
			if err != nil {
				return err
			}
			own, err := bringUp(plan, false, o.scenario)
			if err != nil {
				return err
			}
			// The same data in both: the one the pull request's sandbox
			// has, unless it was named.
			scenario := o.scenario
			if scenario == "" && plan.snap == nil {
				scenario = own.Scenario
			}
			base, err := bringUp(plan, true, scenario)
			switch {
			case err != nil && data.IsUnknownScenario(err):
				// A scenario the pull request brings with it: the base
				// has neither it nor, as often as not, the schema it
				// fills.
				return errs.Wrap(err, "#%d is running, but its base cannot have the same data", own.PR).
					WithHint("scenario %q comes with the pull request; --scenario names one the base has too (%s)", scenario, errs.Hint(err))
			case err != nil:
				return errs.Wrap(err, "#%d is running, but its base could not be brought up", own.PR).
					WithHint("`pit %d` alone is still there; `pit down %d --base` clears what is left of the base", own.PR, own.PR)
			}

			out := plan.out
			plan.rep.Blank()
			if opts.jsonOutput {
				return writeJSON(out, compareJSON{PR: own.PR, URL: own.URL, BaseURL: base.URL,
					Scenario: base.Scenario, Snapshot: base.Snapshot, SHA: own.SHA, BaseSHA: base.SHA})
			}
			writeComparison(out, own, base)
			if o.open {
				openInBrowser(c.Context(), out, own.URL)
				openInBrowser(c.Context(), out, base.URL)
			}
			return out.Err()
		},
	}
	f := cmd.Flags()
	f.BoolVar(&o.open, "open", false, "open both in a browser once they are ready")
	f.StringVar(&o.scenario, "scenario", "", "data state to load in both (default: the one the pull request's sandbox has)")
	f.StringVar(&o.snapshot, "snapshot", "", "load a saved snapshot, by ID or name, into both")
	return cmd
}

type compareJSON struct {
	PR       int    `json:"pr"`
	URL      string `json:"url"`
	BaseURL  string `json:"baseUrl"`
	Scenario string `json:"scenario,omitempty"`
	Snapshot string `json:"snapshot,omitempty"`
	SHA      string `json:"sha"`
	BaseSHA  string `json:"baseSha"`
}

// writeComparison says where each is, and what they run on: the URLs on
// stdout, one to a line, the pull request first.
func writeComparison(out *ui.Printer, own, base state.Sandbox) {
	out.Printf("#%d       %s  %s\n", own.PR, own.URL, short(own.SHA))
	out.Printf("#%d base  %s  %s\n", own.PR, base.URL, short(base.SHA))
	switch {
	case base.Snapshot != "":
		out.Notef("both with snapshot %s", base.Snapshot)
	case base.Scenario != "":
		out.Notef("both with scenario %s", base.Scenario)
	}
	if own.Edited {
		out.Warnf("#%d's data was changed by hand since it was loaded; the base has it as loaded", own.PR)
	}
}
