//go:build windows

package proc

import (
	"os/exec"
	"sync"
	"syscall"

	"golang.org/x/sys/windows"
)

// Windows keeps no process group a signal could reach, and a console
// event reaches only processes that share pit's console, which a CI
// runner's do not. What it has instead is a job: a process put into one
// takes every process it starts in with it, and ending the job ends
// them all -- also the ones whose parent has already exited, which
// taskkill /T no longer finds.

// jobs holds the job of each running command.
var jobs sync.Map

// setProcessGroup keeps the reviewer's Ctrl+C from reaching the child
// directly, as a process group does on Unix: pit decides how it stops.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
}

// track puts a started command into a job of its own. A child it
// starts in the moment before is not in it; there is no way to start a
// process inside a job from Go.
func track(cmd *exec.Cmd) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return
	}
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		_ = windows.CloseHandle(job)
		return
	}
	defer func() { _ = windows.CloseHandle(h) }()
	if err := windows.AssignProcessToJobObject(job, h); err != nil {
		_ = windows.CloseHandle(job)
		return
	}
	jobs.Store(cmd, job)
}

// untrack lets go of the job. What is still in it keeps running, as
// what a finished command left behind does on Unix.
func untrack(cmd *exec.Cmd) {
	if job, ok := jobs.LoadAndDelete(cmd); ok {
		_ = windows.CloseHandle(job.(windows.Handle))
	}
}

// interrupt stops the command and everything it started. Windows has
// no gentler way that reaches them reliably.
func interrupt(cmd *exec.Cmd) error {
	terminate(cmd)
	return nil
}

// terminate ends the command's job, or the command alone where it has
// none.
func terminate(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	if job, ok := jobs.Load(cmd); ok {
		if windows.TerminateJobObject(job.(windows.Handle), 1) == nil {
			return
		}
	}
	_ = cmd.Process.Kill()
}

// interruptOne stops an interactive child. The console has already
// given it the reviewer's Ctrl+C, so this is for a cancel that did not
// come from the keyboard.
func interruptOne(cmd *exec.Cmd) error {
	return cmd.Process.Kill()
}
