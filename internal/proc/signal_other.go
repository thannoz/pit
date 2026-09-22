//go:build !unix

package proc

import "os/exec"

// Windows has no process groups in the POSIX sense. pit does not target
// it yet (see P10); these keep the package compiling so that porting is
// a matter of filling them in rather than untangling the call sites.

func setProcessGroup(*exec.Cmd) {}

func interrupt(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}

func terminate(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
}
