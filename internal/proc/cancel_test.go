package proc

import (
	"bytes"
	"context"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// cancelDeadline is the budget from cancelling the context to Run
// returning. The acceptance criterion for T-005 is 200ms; the process
// gets SIGINT immediately, so anything slower means the signal path is
// broken rather than merely slow.
const cancelDeadline = 200 * time.Millisecond

// alive reports whether a pid still exists. Signal 0 performs the
// permission and existence checks without delivering anything.
func alive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}

func TestCancelStopsTheProcessPromptly(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())

	done := make(chan error, 1)
	go func() {
		done <- Exec{}.Stream(ctx, Command{Name: "sleep", Args: []string{"30"}}, &bytes.Buffer{}, &bytes.Buffer{})
	}()

	// Give the process a moment to actually exist before cancelling.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(cancelDeadline):
		t.Fatalf("Stream did not return within %v of cancelling", cancelDeadline)
	}
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

func TestDeadlineExceededIsReported(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()

	_, err := Exec{}.Output(ctx, Command{Name: "sleep", Args: []string{"30"}})
	if err == nil {
		t.Fatal("want an error when the deadline passes")
	}
	if ctx.Err() == nil {
		t.Error("the context should have expired")
	}
}

func TestAlreadyCancelledContextDoesNotRun(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err := (Exec{}).Output(ctx, Command{Name: "echo", Args: []string{"nope"}}); err == nil {
		t.Error("want an error for an already cancelled context")
	}
}

// syncBuffer is a bytes.Buffer that may be read while it is written.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// waitForPID reads the pid the shell echoed, retrying until it appears.
func waitForPID(t *testing.T, out *syncBuffer) int {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if line := strings.TrimSpace(out.String()); line != "" {
			pid, err := strconv.Atoi(strings.Fields(line)[0])
			if err != nil {
				t.Fatalf("cannot parse pid from %q: %v", line, err)
			}
			return pid
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the shell never reported a pid")
	return 0
}
