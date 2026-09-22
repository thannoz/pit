package cli

import (
	"context"
	"errors"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/proc"
	"github.com/thannoz/pit/internal/runtime"
	"github.com/thannoz/pit/internal/sandbox"
	"github.com/thannoz/pit/internal/state"
)

// stateSandbox is an alias so the helpers below read without repeating
// the package name on every signature.
type stateSandbox = state.Sandbox

type logsOptions struct {
	follow bool
	tail   int
}

func newLogsCmd(_ *globalOptions) *cobra.Command {
	o := &logsOptions{}

	cmd := &cobra.Command{
		Use:   "logs <pull request number> [service]",
		Short: "Show what a sandbox's services are saying",
		Long: `Show the output of a sandbox's services.

Without a service name, this shows the one a reviewer opens; pass a name
to see another, or "" together with --all-services for every one.`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(c *cobra.Command, args []string) error {
			return runLogs(c, o, args)
		},
	}

	f := cmd.Flags()
	f.BoolVarP(&o.follow, "follow", "f", false, "keep printing new output until interrupted")
	f.IntVar(&o.tail, "tail", 200, "how many lines of history to show")

	return cmd
}

func runLogs(c *cobra.Command, o *logsOptions, args []string) error {
	box, err := sandboxFor(c, args[0])
	if err != nil {
		return err
	}
	service := serviceArg(args, box)

	cmdArgs := []string{"logs", "--no-color", "--tail", strconv.Itoa(o.tail)}
	if o.follow {
		cmdArgs = append(cmdArgs, "--follow")
	}
	if service != "" {
		cmdArgs = append(cmdArgs, service)
	}

	run := runtime.ComposeCommand(sandbox.RuntimeSandbox(box), cmdArgs...)
	err = proc.Exec{}.Stream(c.Context(), run, c.OutOrStdout(), c.ErrOrStderr())
	return ignoreInterrupt(c.Context(), err)
}

// ignoreInterrupt turns "the user pressed Ctrl+C" into success. For a
// command whose whole job is to keep printing until interrupted, being
// interrupted is how it ends, not a failure to report.
func ignoreInterrupt(ctx context.Context, err error) error {
	if err != nil && ctx.Err() != nil && errors.Is(ctx.Err(), context.Canceled) {
		return nil
	}
	return err
}

// sandboxFor finds the recorded sandbox a command was given a number
// for.
func sandboxFor(c *cobra.Command, arg string) (state.Sandbox, error) {
	pr, err := strconv.Atoi(arg)
	if err != nil || pr <= 0 {
		return state.Sandbox{}, errs.New("%q is not a pull request number", arg)
	}

	repo, err := currentRepo(c.Context())
	if err != nil {
		return state.Sandbox{}, err
	}
	m, err := manager()
	if err != nil {
		return state.Sandbox{}, err
	}
	f, err := m.Store.Load()
	if err != nil {
		return state.Sandbox{}, err
	}

	box, ok := f.Find(repo.Identity.Ref(), pr)
	if !ok {
		return state.Sandbox{}, errs.New("there is no sandbox for #%d in %s", pr, repo.Identity).
			WithHint("`pit ls` shows what exists; `pit %d` creates one", pr)
	}
	return box, nil
}

// serviceArg is the service the command should act on: the one given,
// or the one a reviewer opens.
func serviceArg(args []string, box state.Sandbox) string {
	if len(args) > 1 {
		return args[1]
	}
	return box.WebService
}
