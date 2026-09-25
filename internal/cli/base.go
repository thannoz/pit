package cli

import (
	"github.com/spf13/cobra"

	"github.com/thannoz/pit/internal/errs"
)

func newBaseCmd(opts *globalOptions) *cobra.Command {
	up := &upOptions{base: true}
	cmd := &cobra.Command{
		Use:   "base <pull request number>",
		Short: "Run the commit a pull request goes into, beside the pull request",
		Long: `Bring up the branch a pull request goes into, as it is now, in a
sandbox of its own beside the pull request's: what the application did
before the change, with the same data.

It is pit <n> --base. Other commands reach it with --base too:
pit logs 482 --base, pit down 482 --base.`,
		Example: `  pit base 482
  pit base 482 --scenario=refunded`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return runUp(c, up, args[0])
		},
	}
	f := cmd.Flags()
	f.BoolVar(&up.open, "open", false, "open the sandbox in a browser once it is ready")
	f.StringVar(&up.scenario, "scenario", "", "data state to load (default: the one configured as data.default)")
	f.StringVar(&up.snapshot, "snapshot", "", "load a saved snapshot, by ID or name, instead of a scenario")
	return cmd
}

// notOnBase refuses a command that is about the pull request itself --
// what it changes, what was found in it -- when it is pointed at its
// base.
func notOnBase(c *cobra.Command, what string) error {
	if base, _ := c.Flags().GetBool("base"); base {
		return errs.New("%s is about the pull request itself, not the branch it goes into", what).
			WithHint("leave out --base")
	}
	return nil
}
