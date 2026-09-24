package cli

import (
	"io"
	"log/slog"
	"os"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/thannoz/pit/internal/errs"
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
	up := &upOptions{}

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
		// `pit 482` is the command people reach for, so the root
		// takes a pull request number directly. Anything else, or
		// nothing, shows the help.
		Args: cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if len(args) == 0 {
				return c.Help()
			}
			// Since the root takes an argument of its own, cobra no
			// longer reports an unknown subcommand -- it hands it
			// here. A mistyped command must not be answered with
			// "that is not a pull request number".
			if _, err := strconv.Atoi(args[0]); err != nil {
				return unknownCommand(c, args[0])
			}
			return runUp(c, up, args[0])
		},
	}

	// cobra reports an unknown command without saying what to do next.
	cmd.SetFlagErrorFunc(func(c *cobra.Command, err error) error {
		return errs.Hinted(err, "run %q to see the available flags", c.CommandPath()+" --help")
	})

	// cobra only fills this in during its own Execute path, which the
	// root's RunE runs ahead of; without it SuggestionsFor never
	// matches anything.
	cmd.SuggestionsMinimumDistance = 2

	cmd.Flags().BoolVar(&up.open, "open", false, "open the sandbox in a browser once it is ready")
	cmd.Flags().StringVar(&up.scenario, "scenario", "", "data state to load (default: the one configured as data.default)")

	f := cmd.PersistentFlags()
	f.BoolVarP(&opts.verbose, "verbose", "v", false, "print diagnostic logging to stderr")
	f.StringVar(&opts.configPath, "config", "", "path to .pit.yaml (default: found from the working directory)")
	f.BoolVar(&opts.jsonOutput, "json", false, "print machine readable output")

	cmd.AddCommand(
		newDataCmd(opts),
		newDoctorCmd(opts),
		newDownCmd(opts),
		newInitCmd(opts),
		newLogsCmd(opts),
		newLsCmd(opts),
		newOpenCmd(opts),
		newScenariosCmd(opts),
		newShellCmd(opts),
		newSnapCmd(opts),
		newTimingCmd(opts),
		newWhatCmd(opts),
		newVersionCmd(opts),
	)

	return cmd
}

// unknownCommand explains a mistyped command, with cobra's own
// suggestions when it has any.
func unknownCommand(c *cobra.Command, arg string) error {
	err := errs.New("%s %q for %q", unknownCommandPrefix, arg, c.CommandPath())

	if near := c.SuggestionsFor(arg); len(near) > 0 {
		return err.WithHint("did you mean %q?", near[0])
	}
	return err.WithHint("run %q to see the available commands, or pass a pull request number",
		c.CommandPath()+" --help")
}

// configureLogging points the default logger at stderr when verbose is
// set. User facing output never goes through slog; that separation is
// what keeps stdout parsable.
func configureLogging(verbose bool) {
	slog.SetDefault(slog.New(newLogHandler(os.Stderr, verbose)))
}

// newLogHandler builds the diagnostic handler. Without verbose it
// discards everything: diagnostics are opt-in, and a tool that chatters
// on stderr by default is a tool people redirect to /dev/null.
func newLogHandler(w io.Writer, verbose bool) slog.Handler {
	if !verbose {
		return slog.NewTextHandler(io.Discard, nil)
	}
	return slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelDebug})
}
