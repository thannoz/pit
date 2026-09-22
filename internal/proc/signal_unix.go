//go:build unix

package proc

import (
	"os/exec"
	"syscall"
)

// setProcessGroup puts the child into its own process group, so signals
// can reach everything it spawns rather than only the child itself.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// interrupt asks the whole group to stop. A negative pid addresses the
// group; because the child leads its own group, its pid is the group id.
func interrupt(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGINT)
}

// terminate kills whatever is left of the group. It runs after the grace
// period, so anything still alive has had its chance.
func terminate(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	// The error is deliberately dropped: by this point the group is
	// usually already gone, which is the outcome we wanted.
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
