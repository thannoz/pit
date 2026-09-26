package cli

import (
	"os"
	"strconv"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/proc"
	"github.com/thannoz/pit/internal/runtime/kube"
	"github.com/thannoz/pit/internal/sandbox"
	"github.com/thannoz/pit/internal/state"
)

// kubeRunner runs kubectl for pit logs and pit shell of a sandbox in
// Kubernetes; a variable, so that a test runs a kubectl of its own.
var kubeRunner kube.Runner = proc.Exec{}

// kubeLogs prints what a workload of a sandbox in Kubernetes wrote,
// and with --follow what it goes on writing.
func kubeLogs(c *cobra.Command, box state.Sandbox, service string, o *logsOptions) error {
	p, ref, err := kube.Runtime{Runner: kubeRunner}.Target(c.Context(), sandbox.RuntimeSandbox(box), service)
	if err != nil {
		return err
	}
	args := append(p.Flags(), "logs", ref, "--tail", strconv.Itoa(o.tail))
	if o.follow {
		args = append(args, "--follow")
	}
	return kubeRunner.Stream(c.Context(), proc.Command{Name: "kubectl", Args: args}, c.OutOrStdout(), c.ErrOrStderr())
}

// kubeShell runs a command, or a shell, in a workload of a sandbox in
// Kubernetes.
func kubeShell(c *cobra.Command, box state.Sandbox, service string, command []string) error {
	p, ref, err := kube.Runtime{Runner: kubeRunner}.Target(c.Context(), sandbox.RuntimeSandbox(box), service)
	if err != nil {
		return err
	}
	if len(command) == 0 {
		// bash where the image has it, sh everywhere else.
		command = []string{"sh", "-c", "command -v bash >/dev/null && exec bash || exec sh"}
	}
	args := append(p.Flags(), "exec", "--stdin")
	if term.IsTerminal(int(os.Stdin.Fd())) {
		args = append(args, "--tty")
	}
	args = append(append(args, ref, "--"), command...)
	err = ignoreInterrupt(c.Context(), proc.Exec{}.Attach(c.Context(), proc.Command{Name: "kubectl", Args: args}))
	if err != nil {
		return errs.Hinted(err, "`pit logs %d %s` shows what it wrote", box.PR, service)
	}
	return nil
}
