package cli

import (
	"github.com/spf13/cobra"

	"github.com/thannoz/pit/internal/config"
	"github.com/thannoz/pit/internal/data"
	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/state"
	"github.com/thannoz/pit/internal/ui"
)

type dataResetOptions struct {
	scenario string
	yes      bool
}

func newDataCmd(opts *globalOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "data",
		Short: "Work with the data a sandbox is in",
		Long: `Commands for the state of a sandbox's database.

Which state a sandbox was started in is shown by "pit ls"; which states
this repository offers is shown by "pit scenarios".`,
	}
	cmd.AddCommand(newDataResetCmd(opts))
	return cmd
}

func newDataResetCmd(_ *globalOptions) *cobra.Command {
	o := &dataResetOptions{}

	cmd := &cobra.Command{
		Use:   "reset <pull request number>",
		Short: "Load a scenario again into a running sandbox",
		Long: `Run a scenario's commands again against a sandbox that is already up.

A review usually ruins its own data: you fill in a form, delete a row,
click something twice. This puts the sandbox back without rebuilding
it, which takes seconds instead of minutes.

It runs the scenario's own commands and nothing else. pit has no way
of knowing how to empty a database, so whether this arrives back at
the starting point depends on the commands being written to run more
than once.`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return runDataReset(c, o, args[0])
		},
	}

	f := cmd.Flags()
	f.StringVar(&o.scenario, "scenario", "", "load this scenario instead of the one the sandbox was started with")
	f.BoolVarP(&o.yes, "yes", "y", false, "do not ask for confirmation")

	return cmd
}

func runDataReset(c *cobra.Command, o *dataResetOptions, arg string) error {
	box, err := sandboxFor(c, arg)
	if err != nil {
		return err
	}

	scenario, err := scenarioFor(box, o.scenario)
	if err != nil {
		return err
	}

	m, err := manager()
	if err != nil {
		return err
	}
	out := ui.New(c.OutOrStdout(), c.ErrOrStderr())

	// Loading data into containers that are not there fails in the
	// middle of a compose command; saying so first is cheaper to read.
	entry, err := m.Find(c.Context(), box.RepoRef, box.PR)
	if err != nil {
		return err
	}
	if entry.Unreachable != nil {
		return errs.Wrap(entry.Unreachable, "cannot tell whether #%d is running", box.PR)
	}
	if !entry.AnyRunning() {
		return errs.New("nothing is running for #%d", box.PR).
			WithHint("`pit %d` brings the sandbox up again; `pit ls` shows what exists", box.PR)
	}

	if scenario.Empty() {
		out.Printf("Scenario %q runs no commands, so there is nothing to load.\n", scenario.Name)
		return nil
	}

	if !o.yes {
		out.Printf("This runs %q again on #%d. Anything entered by hand since is lost.\n",
			scenario.Describe(), box.PR)
		if !confirm(c, out, "Load it?") {
			out.Println("The data was left alone.")
			return nil
		}
	}

	rep := newStepReporter(out, c.ErrOrStderr())
	return m.ResetData(c.Context(), box, scenario, rep)
}

// scenarioFor works out which data state to load: the one asked for,
// otherwise the one the sandbox was started with.
//
// It deliberately does not fall back to the repository's default. A
// sandbox started without data was started that way on purpose, and
// filling it now would be a surprise rather than a reset.
func scenarioFor(box state.Sandbox, requested string) (data.Scenario, error) {
	cfg, _, err := config.LoadFrom(box.RepoRoot)
	if err != nil {
		return data.Scenario{}, errs.Wrap(err, "cannot read the configuration of #%d", box.PR).
			WithHint("the sandbox was created from %s, which has to still be there", box.RepoRoot)
	}

	name := requested
	if name == "" {
		name = box.Scenario
	}
	if name == "" {
		return data.Scenario{}, errs.New("#%d was not started with a scenario", box.PR).
			WithHint("`pit scenarios` lists what this repository offers; --scenario picks one")
	}
	return data.Select(cfg, name)
}
