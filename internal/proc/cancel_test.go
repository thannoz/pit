package proc

import (
	"bytes"
	"context"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// cancelDeadline is the budget from cancelling the context to Run
// returning. The acceptance criterion for T-005 is 200ms; the process
// gets SIGINT immediately, so anything slower means the signal path is
// broken rather than merely slow.
const cancelDeadline = 200 * time.Millisecond

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
