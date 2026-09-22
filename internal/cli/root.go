package cli

import (
	"io"
	"log/slog"
	"os"

	"github.com/spf13/cobra"
)

// globalOptions holds the flags every command shares.
type globalOptions struct {
	// verbose turns on diagnostic logging on stderr.
	verbose bool
	// configPath overrides the .pit.yaml that would otherwise be found
	// by walking up from the working directory. Consumed in T-011.
	configPath string
	// jsonOutput switches commands that display something to machine
	// readable output.
	jsonOutput bool
}

const rootLong = `pit brings up the running state of a pull request on your own machine.

A pull request is reviewed as a text diff, which leaves everything that
only appears at runtime invisible. pit checks out the pull request into
an isolated worktree, starts its services, loads the right test data and
hands you a URL, so the review is something you can operate.`

func newRootCmd() *cobra.Command {
	opts := &globalOptions{}

	cmd := &cobra.Command{
		Use:   "pit",
		Short: "Run a pull request locally to review its behaviour",
		Long:  rootLong,
		// Errors are reported by main; usage is not helpful for a
		// failure that happened after the arguments parsed fine.
		SilenceErrors: true,
		SilenceUsage:  true,
		PersistentPreRun: func(_ *cobra.Command, _ []string) {
			configureLogging(opts.verbose)
		},
		// Without a subcommand, show help rather than failing.
		RunE: func(c *cobra.Command, _ []string) error {
			return c.Help()
		},
	}

	f := cmd.PersistentFlags()
	f.BoolVarP(&opts.verbose, "verbose", "v", false, "print diagnostic logging to stderr")
	f.StringVar(&opts.configPath, "config", "", "path to .pit.yaml (default: found from the working directory)")
	f.BoolVar(&opts.jsonOutput, "json", false, "print machine readable output")

	cmd.AddCommand(newVersionCmd(opts))

	return cmd
}

// configureLogging sends diagnostics to stderr when verbose is set and
// discards them otherwise. User facing output never goes through slog;
// that separation is the point (see T-003).
func configureLogging(verbose bool) {
	var h slog.Handler
	if verbose {
		h = slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})
	} else {
		h = slog.NewTextHandler(io.Discard, nil)
	}
	slog.SetDefault(slog.New(h))
}
