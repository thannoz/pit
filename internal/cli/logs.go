package cli

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/proc"
	"github.com/thannoz/pit/internal/runtime"
	"github.com/thannoz/pit/internal/sandbox"
	"github.com/thannoz/pit/internal/state"
	"github.com/thannoz/pit/internal/ui"
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
	// Named once, on stderr, so it survives `pit logs 7 > out.log` and
	// still tells a reviewer with three reviews open which one this is.
	ui.New(c.OutOrStdout(), c.ErrOrStderr()).Notef("%s", box.Describe())

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

// sandboxFor finds the sandbox a command was given a reference for.
//
// The record is global, so a number alone can name more than one
// sandbox. Standing in a repository resolves it; when that is not
// possible, a number that is unique across every repository is
// accepted anyway. Only a genuine ambiguity asks the reviewer
// anything, and then it hands them something they can type.
func sandboxFor(c *cobra.Command, arg string) (state.Sandbox, error) {
	ref, err := state.ParseRef(arg)
	if err != nil {
		return state.Sandbox{}, errs.New("%s", err.Error()).
			WithHint("pass a number, or `<repo>#<number>` when several repositories have one")
	}

	m, err := manager()
	if err != nil {
		return state.Sandbox{}, err
	}
	f, err := m.Store.Load()
	if err != nil {
		return state.Sandbox{}, err
	}

	base, _ := c.Flags().GetBool("base")
	var matches []state.Sandbox
	for _, box := range f.Sandboxes {
		if ref.Matches(box) && box.Base == base {
			matches = append(matches, box)
		}
	}

	switch len(matches) {
	case 0:
		if base {
			return state.Sandbox{}, errs.New("there is no sandbox for the base of #%d", ref.PR).
				WithHint("`pit base %d` creates one", ref.PR)
		}
		return state.Sandbox{}, errs.New("there is no sandbox for #%d", ref.PR).
			WithHint("`pit ls` shows what exists; `pit %d` creates one", ref.PR)
	case 1:
		return matches[0], nil
	}

	// Several: the repository the command was run in decides, if it is
	// one of them.
	if repo, err := currentRepo(c.Context()); err == nil {
		for _, box := range matches {
			if box.RepoRef == repo.Identity.Ref() {
				return box, nil
			}
		}
	}
	return state.Sandbox{}, ambiguous(ref, matches)
}

// ambiguous lists the candidates as references that can be typed back
// in. Telling someone to go and stand in the right directory is not an
// answer when they are looking at a list that spans directories.
func ambiguous(ref state.Ref, matches []state.Sandbox) error {
	names := make([]string, 0, len(matches))
	for _, box := range matches {
		names = append(names, box.QualifiedRef())
	}
	return errs.New("#%d exists in %d repositories", ref.PR, len(matches)).
		WithHint("name one of them: %s", strings.Join(names, ", "))
}

// serviceArg is the service the command should act on: the one given,
// or the one a reviewer opens.
func serviceArg(args []string, box state.Sandbox) string {
	if len(args) > 1 {
		return args[1]
	}
	return box.WebService
}
