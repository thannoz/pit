//go:build unix

package proc

import (
	"bytes"
	"context"
	"os"
	"syscall"
	"testing"
	"time"
)

// alive reports whether a pid still exists. Signal 0 performs the
// permission and existence checks without delivering anything.
func alive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}

func TestCancelKillsGrandchildren(t *testing.T) {
	// `sh` spawns sleep and reports its pid. Without a process group,
	// cancelling would reap sh and leave sleep running — which in the
	// real tool means an orphaned container or a held lock.
	// The test reads this while os/exec is still writing to it, so it
	// needs a lock of its own.
	stdout := &syncBuffer{}
	ctx, cancel := context.WithCancel(t.Context())

	done := make(chan error, 1)
	go func() {
		done <- Exec{GracePeriod: 100 * time.Millisecond}.Stream(
			ctx,
			Command{Name: "sh", Args: []string{"-c", "sleep 30 & echo $!; wait"}},
			stdout, &bytes.Buffer{},
		)
	}()

	grandchild := waitForPID(t, stdout)
	if !alive(grandchild) {
		t.Fatalf("grandchild %d was never running", grandchild)
	}

	cancel()
	<-done

	// Signal delivery and reaping are not instantaneous.
	deadline := time.Now().Add(2 * time.Second)
	for alive(grandchild) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if alive(grandchild) {
		t.Errorf("grandchild %d survived cancellation", grandchild)
	}
}
