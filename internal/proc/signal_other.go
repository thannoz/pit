//go:build !unix && !windows

package proc

import "os/exec"

// A system that is neither Unix nor Windows gets the plainest stop
// there is: the child is killed, and whatever it started is left.

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

func interruptOne(cmd *exec.Cmd) error { return cmd.Process.Kill() }

// track and untrack have nothing to do where a process group is enough.
func track(*exec.Cmd)   {}
func untrack(*exec.Cmd) {}
