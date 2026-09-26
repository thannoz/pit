//go:build windows

package proc

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// The test binary plays the child and the grandchild: Windows has no sh
// that reports a pid Windows knows.
func TestMain(m *testing.M) {
	switch os.Getenv("PIT_PROC_TEST") {
	case "child":
		grandchild := exec.CommandContext(context.Background(), os.Args[0])
		grandchild.Env = append(os.Environ(), "PIT_PROC_TEST=grandchild")
		if err := grandchild.Start(); err != nil {
			os.Exit(2)
		}
		fmt.Println(grandchild.Process.Pid)
		_ = grandchild.Wait()
		os.Exit(0)
	case "grandchild":
		time.Sleep(30 * time.Second)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// stillActive is what GetExitCodeProcess says of a process that has
// not ended.
const stillActive = 259

func alive(pid int) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer func() { _ = windows.CloseHandle(h) }()
	var code uint32
	if windows.GetExitCodeProcess(h, &code) != nil {
		return false
	}
	return code == stillActive
}

func TestCancelKillsGrandchildren(t *testing.T) {
	// Without a group of its own to stop, cancelling would end the
	// child and leave what it started running.
	stdout := &syncBuffer{}
	ctx, cancel := context.WithCancel(t.Context())

	done := make(chan error, 1)
	go func() {
		done <- Exec{GracePeriod: 100 * time.Millisecond}.Stream(
			ctx, Command{Name: os.Args[0], Env: []string{"PIT_PROC_TEST=child"}},
			stdout, &bytes.Buffer{},
		)
	}()

	grandchild := waitForPID(t, stdout)
	if !alive(grandchild) {
		t.Fatalf("grandchild %d was never running", grandchild)
	}

	cancel()
	<-done

	deadline := time.Now().Add(5 * time.Second)
	for alive(grandchild) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if alive(grandchild) {
		t.Errorf("grandchild %d survived cancellation", grandchild)
	}
}
