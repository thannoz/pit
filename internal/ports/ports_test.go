package ports

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/thannoz/pit/internal/errs"
)

const repo = "github.com/acme/shop"

// TestSamePullRequestAlwaysGetsTheSamePort is the first half of the
// acceptance criterion for T-301.
func TestSamePullRequestAlwaysGetsTheSamePort(t *testing.T) {
	first := Preferred(repo, 482)

	for range 100 {
		if got := Preferred(repo, 482); got != first {
			t.Fatalf("Preferred() returned %d and then %d", first, got)
		}
	}
}

// TestPortIsPinned guards the promise across releases: the port names a
// URL people bookmark, so changing how it is computed silently breaks
// every saved tab.
func TestPortIsPinned(t *testing.T) {
	tests := []struct {
		repo string
		pr   int
		want int
	}{
		{"github.com/acme/shop", 482, 49580},
		{"github.com/acme/shop", 483, 49581},
		{"github.com/thannoz/pit", 1, 45059},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("%s#%d", tt.repo, tt.pr), func(t *testing.T) {
			if got := Preferred(tt.repo, tt.pr); got != tt.want {
				t.Errorf("Preferred(%q, %d) = %d, want %d -- changing this breaks every bookmarked sandbox URL",
					tt.repo, tt.pr, got, tt.want)
			}
		})
	}
}

func TestPortsStayInsideTheRange(t *testing.T) {
	// Below the range sit the ports developers use by habit; above it
	// the kernel's ephemeral range for outgoing connections.
	for pr := 1; pr <= 2000; pr++ {
		p := Preferred(repo, pr)
		if p < Min || p > Max {
			t.Fatalf("Preferred(%d) = %d, outside %d-%d", pr, p, Min, Max)
		}
	}
}

func TestDifferentRepositoriesDoNotShareAPort(t *testing.T) {
	// Two people reviewing pull request 1 in different projects would
	// otherwise collide every time.
	a := Preferred("github.com/acme/shop", 1)
	b := Preferred("github.com/acme/admin", 1)

	if a == b {
		t.Errorf("both repositories got port %d", a)
	}
}

func TestConsecutivePullRequestsGetConsecutivePorts(t *testing.T) {
	// Within one repository the number walks upwards, so two reviews
	// open at once never land on the same port -- and the relationship
	// is obvious enough to recognise at a glance.
	first := Preferred(repo, 1)

	for pr := 1; pr <= 50; pr++ {
		want := Min + (first-Min+pr-1)%Span
		if got := Preferred(repo, pr); got != want {
			t.Fatalf("Preferred(#%d) = %d, want %d", pr, got, want)
		}
	}
}

func TestNoTwoOpenReviewsShareAPort(t *testing.T) {
	seen := map[int]int{}
	for pr := 1; pr <= 500; pr++ {
		p := Preferred(repo, pr)
		if other, clash := seen[p]; clash {
			t.Errorf("#%d and #%d both got port %d", other, pr, p)
		}
		seen[p] = pr
	}
}

// TestReserveAvoidsAPortInUse is the second half of the acceptance
// criterion: the collision is stepped over and recorded.
func TestReserveAvoidsAPortInUse(t *testing.T) {
	preferred := Preferred(repo, 482)

	// Hold the preferred port for real, rather than faking it.
	var lc net.ListenConfig
	l, err := lc.Listen(t.Context(), "tcp", ":"+strconv.Itoa(preferred))
	if err != nil {
		t.Skipf("cannot bind %d on this machine: %v", preferred, err)
	}
	defer l.Close() //nolint:errcheck // the test is over by then

	got, err := Reserve(t.Context(), repo, 482, nil)
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}

	if got.Port == preferred {
		t.Errorf("Reserve returned the occupied port %d", preferred)
	}
	if !got.Moved() {
		t.Error("Moved() is false although the preferred port was taken")
	}
	if got.Preferred != preferred {
		t.Errorf("Preferred = %d, want %d", got.Preferred, preferred)
	}
	// The reviewer should be told why the URL changed.
	if !strings.Contains(got.String(), "was in use") {
		t.Errorf("String() = %q, want it to explain the move", got)
	}
}

func TestReserveUsesThePreferredPortWhenItIsFree(t *testing.T) {
	got, err := Reserve(t.Context(), repo, 482, func(int) bool { return false })
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}

	if got.Moved() {
		t.Errorf("Moved() is true although nothing was in the way")
	}
	if got.String() != strconv.Itoa(got.Port) {
		t.Errorf("String() = %q, want just the port", got)
	}
}

func TestReserveStepsUpwards(t *testing.T) {
	preferred := Preferred(repo, 482)
	blocked := map[int]bool{preferred: true, preferred + 1: true, preferred + 2: true}

	got, err := Reserve(t.Context(), repo, 482, func(p int) bool { return blocked[p] })
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if got.Port != preferred+3 {
		t.Errorf("Port = %d, want %d", got.Port, preferred+3)
	}
}

func TestReserveWrapsAtTheTopOfTheRange(t *testing.T) {
	// A busy machine should still get an answer rather than an error
	// about the last few ports.
	taken := func(p int) bool { return p != Min }

	got, err := Reserve(t.Context(), repo, 482, taken)
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if got.Port != Min {
		t.Errorf("Port = %d, want it to wrap round to %d", got.Port, Min)
	}
}

func TestReserveGivesUpWhenEverythingIsTaken(t *testing.T) {
	_, err := Reserve(t.Context(), repo, 482, func(int) bool { return true })
	if err == nil {
		t.Fatal("want an error when no port is free")
	}
	if errs.Hint(err) == "" {
		t.Error("the error carries no hint")
	}
}

func TestTakenDetectsARealListener(t *testing.T) {
	var lc net.ListenConfig
	l, err := lc.Listen(t.Context(), "tcp", ":0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer l.Close() //nolint:errcheck // the test is over by then

	port := l.Addr().(*net.TCPAddr).Port
	if !Taken(t.Context(), port) {
		t.Errorf("Taken(%d) = false although the port is bound", port)
	}
}

func TestTakenIsFalseForAFreePort(t *testing.T) {
	var lc net.ListenConfig
	l, err := lc.Listen(t.Context(), "tcp", ":0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if err := WaitFree(t.Context(), port, 2*time.Second); err != nil {
		t.Fatalf("WaitFree: %v", err)
	}
	if Taken(t.Context(), port) {
		t.Errorf("Taken(%d) = true although the listener is closed", port)
	}
}

func TestWaitFreeGivesUp(t *testing.T) {
	var lc net.ListenConfig
	l, err := lc.Listen(t.Context(), "tcp", ":0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer l.Close() //nolint:errcheck // the test is over by then

	port := l.Addr().(*net.TCPAddr).Port
	err = WaitFree(t.Context(), port, 100*time.Millisecond)
	if err == nil {
		t.Fatal("want an error while the port is still held")
	}
	if !strings.Contains(errs.Hint(err), "lsof") {
		t.Errorf("hint = %q, want it to suggest how to find the holder", errs.Hint(err))
	}
}

func TestWaitFreeStopsOnCancellation(t *testing.T) {
	// Holding a port can take a while; Ctrl+C has to be felt at once
	// rather than after the next poll.
	var lc net.ListenConfig
	l, err := lc.Listen(t.Context(), "tcp", ":0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer l.Close() //nolint:errcheck // the test is over by then
	port := l.Addr().(*net.TCPAddr).Port

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- WaitFree(ctx, port, time.Minute) }()

	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want the cancellation", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("WaitFree kept waiting after the context was cancelled")
	}
}
