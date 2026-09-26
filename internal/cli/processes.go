package cli

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/proc"
	"github.com/thannoz/pit/internal/sandbox"
	"github.com/thannoz/pit/internal/state"
)

// followEvery is how often `pit logs -f` looks for more of a process's
// output.
var followEvery = 500 * time.Millisecond

// processLogs prints what a process of a sandbox printed, and with
// --follow what it goes on printing.
func processLogs(c *cobra.Command, box state.Sandbox, service string, o *logsOptions) error {
	m, err := manager()
	if err != nil {
		return err
	}
	rs := sandbox.RuntimeSandbox(box)
	lines, err := m.Runtime.LogsSince(c.Context(), rs, service, time.Time{})
	if err != nil {
		return err
	}
	seen := len(lines)
	if o.tail >= 0 && len(lines) > o.tail {
		lines = lines[len(lines)-o.tail:]
	}
	out := c.OutOrStdout()
	for _, l := range lines {
		_, _ = fmt.Fprintf(out, "%s | %s\n", service, l.Text)
	}
	for o.follow {
		select {
		case <-c.Context().Done():
			return c.Context().Err()
		case <-time.After(followEvery):
		}
		lines, err := m.Runtime.LogsSince(c.Context(), rs, service, time.Time{})
		if err != nil {
			return err
		}
		for _, l := range lines[min(seen, len(lines)):] {
			_, _ = fmt.Fprintf(out, "%s | %s\n", service, l.Text)
		}
		seen = len(lines)
	}
	return nil
}

// shellIn runs a command, or a shell, in a sandbox of processes: in its
// worktree, with what its processes are given.
func shellIn(c *cobra.Command, box state.Sandbox, command []string) error {
	if len(command) == 0 {
		sh := os.Getenv("SHELL")
		if sh == "" {
			sh = "/bin/sh"
		}
		command = []string{sh}
	}
	run := proc.Command{Name: command[0], Args: command[1:], Dir: box.Worktree, Env: sandbox.CommandEnv(box)}
	err := ignoreInterrupt(c.Context(), proc.Exec{}.Attach(c.Context(), run))
	if err != nil {
		return errs.Hinted(err, "it ran in %s", box.Worktree)
	}
	return nil
}
