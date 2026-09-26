package cli

import (
	"strings"

	"github.com/spf13/cobra"

	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/proc"
	"github.com/thannoz/pit/internal/runtime"
	"github.com/thannoz/pit/internal/sandbox"
	"github.com/thannoz/pit/internal/ui"
)

func newShellCmd(_ *globalOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "shell <pull request number> [service] [-- command ...]",
		Short: "Open a shell inside a sandbox's service",
		Long: `Run a command inside one of a sandbox's containers, interactively.

Without a service name, this uses the one a reviewer opens. Without a
command, it starts a shell. Anything after -- is run instead:

  pit shell 482                    # a shell in the web service
  pit shell 482 db                 # a shell in the db service
  pit shell 482 db -- psql -U app  # psql instead of a shell`,
		Args: cobra.ArbitraryArgs,
		RunE: runShell,
	}
	return cmd
}

// defaultShells are tried in order. A container built from a slim base
// image often has no bash, and failing with "executable file not found"
// would be a poor answer to "give me a shell".
var defaultShells = []string{"bash", "sh"}

func runShell(c *cobra.Command, args []string) error {
	// Everything after -- is the command to run; cobra hands it back
	// separately so a flag meant for psql is not read as one of ours.
	passed := args
	if at := c.ArgsLenAtDash(); at >= 0 {
		passed = args[:at]
	}
	if len(passed) == 0 {
		return c.Help()
	}

	box, err := sandboxFor(c, passed[0])
	if err != nil {
		return err
	}
	ui.New(c.OutOrStdout(), c.ErrOrStderr()).Notef("%s", box.Describe())

	service := serviceArg(passed, box)

	command := args[len(passed):]
	if box.Processes {
		return shellIn(c, box, command)
	}
	if box.Kubernetes {
		return kubeShell(c, box, service, command)
	}
	if len(command) > 0 {
		return execIn(c, box, service, command)
	}

	// No command given: find a shell before attaching to one.
	//
	// Trying them in turn by attaching would print Docker's "executable
	// file not found" for every miss, which looks like a failure even
	// when the next attempt succeeds. Looking first is quiet.
	sh, err := findShell(c, box, service)
	if err != nil {
		return err
	}
	return execIn(c, box, service, []string{sh})
}

// findShell asks the container which of the usual shells it has.
func findShell(c *cobra.Command, box stateSandbox, service string) (string, error) {
	for _, sh := range defaultShells {
		args := []string{"exec", "-T", service, "test", "-x", "/bin/" + sh}
		run := runtime.ComposeCommand(sandbox.RuntimeSandbox(box), args...)

		_, err := (proc.Exec{}).Output(c.Context(), run)
		if err == nil {
			return sh, nil
		}
		// "there is no such service" and "that shell is not in the
		// container" both come back as a non-zero exit. Telling someone
		// their database has no shell when they mistyped its name sends
		// them looking in the wrong place.
		if notRunning(err) {
			return "", errs.New("%s is not a running service of #%d", service, box.PR).
				WithHint("`pit logs %d` shows which services there are", box.PR)
		}
		if !missingExecutable(err) && !isExitOne(err) {
			return "", errs.Hinted(err, "`pit logs %d` shows which services are running", box.PR)
		}
	}

	return "", errs.New("%s has none of %s", service, strings.Join(defaultShells, ", ")).
		WithHint("run a command instead: `pit shell %d %s -- <command>`", box.PR, service)
}

// isExitOne reports a plain unsuccessful test, which is how `test -x`
// says the file is not there.
func isExitOne(err error) bool {
	return strings.Contains(err.Error(), "code 1")
}

// notRunning reports whether Compose refused because the service is not
// there, rather than because the command inside it failed.
func notRunning(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "is not running") ||
		strings.Contains(msg, "no such service") ||
		strings.Contains(msg, "no container found")
}

// missingExecutable reports whether a failure means the program was not
// in the container, as opposed to the program itself failing.
//
// The exit code is all there is to go on: an attached command writes
// its own message straight to the terminal, so it never reaches the
// error. 127 is the shell's "command not found"; 126 is "found but not
// executable", which for this purpose means the same thing.
func missingExecutable(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "code 127") || strings.Contains(msg, "code 126")
}

func execIn(c *cobra.Command, box stateSandbox, service string, command []string) error {
	args := append([]string{"exec", service}, command...)
	run := runtime.ComposeCommand(sandbox.RuntimeSandbox(box), args...)

	// Attach rather than Stream: a shell needs the real terminal, or
	// Docker will not give it one.
	err := ignoreInterrupt(c.Context(), proc.Exec{}.Attach(c.Context(), run))
	if err != nil && !missingExecutable(err) {
		return errs.Hinted(err, "`pit logs %d` shows which services are running", box.PR)
	}
	return err
}
