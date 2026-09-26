package runtime

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/proc"
)

// serverAnswering starts a server whose status code is decided per
// request, and counts how often it was asked.
func serverAnswering(t *testing.T, status func(n int) int) (*httptest.Server, *atomic.Int64) {
	t.Helper()

	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status(int(calls.Add(1))))
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func fastProbe(url string) Probe {
	return Probe{
		URL:          url,
		ExpectStatus: http.StatusOK,
		Timeout:      2 * time.Second,
		Interval:     10 * time.Millisecond,
	}
}

func TestWaitReadySucceedsOnTheFirstAttempt(t *testing.T) {
	srv, calls := serverAnswering(t, func(int) int { return http.StatusOK })

	start := time.Now()
	if err := (Compose{Runner: &stubRunner{}}).WaitReady(t.Context(), testSandbox(), "web", fastProbe(srv.URL)); err != nil {
		t.Fatalf("WaitReady: %v", err)
	}

	if got := calls.Load(); got != 1 {
		t.Errorf("made %d requests, want 1", got)
	}
	// A service that is already up must not cost an interval of
	// waiting for nothing.
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Errorf("took %v for a service that was already ready", elapsed)
	}
}

func TestWaitReadyKeepsTryingUntilTheServiceComesUp(t *testing.T) {
	srv, calls := serverAnswering(t, func(n int) int {
		if n < 4 {
			return http.StatusServiceUnavailable
		}
		return http.StatusOK
	})

	if err := (Compose{Runner: &stubRunner{}}).WaitReady(t.Context(), testSandbox(), "web", fastProbe(srv.URL)); err != nil {
		t.Fatalf("WaitReady: %v", err)
	}
	if got := calls.Load(); got < 4 {
		t.Errorf("made %d requests, want at least 4", got)
	}
}

// TestWaitReadyTimesOut is half of the acceptance criterion for T-206.
func TestWaitReadyTimesOut(t *testing.T) {
	srv, _ := serverAnswering(t, func(int) int { return http.StatusServiceUnavailable })

	p := fastProbe(srv.URL)
	p.Timeout = 80 * time.Millisecond

	err := Compose{Runner: &stubRunner{}}.WaitReady(t.Context(), testSandbox(), "web", p)
	if err == nil {
		t.Fatal("want an error when the service never becomes ready")
	}

	for _, want := range []string{"web", "did not become ready", "503"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message is missing %q:\n%s", want, err)
		}
	}
	if errs.Hint(err) == "" {
		t.Error("the error carries no hint")
	}
}

// TestWaitReadyStopsImmediatelyOnCancellation is the other half: Ctrl+C
// has to be felt at once, not after the current interval.
func TestWaitReadyStopsImmediatelyOnCancellation(t *testing.T) {
	srv, _ := serverAnswering(t, func(int) int { return http.StatusServiceUnavailable })

	p := fastProbe(srv.URL)
	p.Interval = 5 * time.Second // long enough that waiting would be obvious
	p.Timeout = time.Minute

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		done <- Compose{Runner: &stubRunner{}}.WaitReady(ctx, testSandbox(), "web", p)
	}()

	time.Sleep(30 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		// Cancellation is the user pressing Ctrl+C, not a failure of
		// the service, and must be reported as such.
		if !strings.Contains(err.Error(), "context canceled") {
			t.Errorf("err = %v, want the cancellation", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("WaitReady kept waiting after the context was cancelled")
	}
}

func TestWaitReadyQuotesTheServiceLogs(t *testing.T) {
	// "timed out" on its own says nothing. The reason is nearly always
	// in the container's own last lines.
	srv, _ := serverAnswering(t, func(int) int { return http.StatusBadGateway })

	r := &stubRunner{replies: map[string]string{
		"compose --project-name pit-acme-shop-482 --file docker-compose.yml logs --no-color --tail 30 web": "Error: cannot find module 'express'\n",
	}}
	p := fastProbe(srv.URL)
	p.Timeout = 50 * time.Millisecond

	err := Compose{Runner: r}.WaitReady(t.Context(), testSandbox(), "web", p)
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "cannot find module 'express'") {
		t.Errorf("the failure does not quote the service's own output:\n%s", err)
	}
}

func TestWaitReadyReportsNothingListening(t *testing.T) {
	// A closed port is the ordinary case while a container starts, and
	// "connection refused" buried in a Go transport error is noise.
	srv, _ := serverAnswering(t, func(int) int { return http.StatusOK })
	url := srv.URL
	srv.Close()

	p := fastProbe(url)
	p.Timeout = 50 * time.Millisecond

	err := Compose{Runner: &stubRunner{}}.WaitReady(t.Context(), testSandbox(), "web", p)
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "nothing was listening") {
		t.Errorf("message = %q, want it to say the port was closed", err)
	}
}

func TestWaitReadyRejectsAZeroInterval(t *testing.T) {
	// Without this the ticker panics, which is a worse way to find out.
	p := fastProbe("http://127.0.0.1:1")
	p.Interval = 0

	if err := (Compose{Runner: &stubRunner{}}).WaitReady(t.Context(), testSandbox(), "web", p); err == nil {
		t.Fatal("want an error for an interval of zero")
	}
}

func TestExpandURL(t *testing.T) {
	tests := []struct {
		name     string
		template string
		want     string
	}{
		{"both placeholders", "http://{host}:{port}/", "http://localhost:41482/"},
		{"a path as well", "http://{host}:{port}/healthz", "http://localhost:41482/healthz"},
		{"only the port", "http://127.0.0.1:{port}/", "http://127.0.0.1:41482/"},
		{"no placeholders", "http://example.test/", "http://example.test/"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ExpandURL(tt.template, "localhost", 41482); got != tt.want {
				t.Errorf("ExpandURL(%q) = %q, want %q", tt.template, got, tt.want)
			}
		})
	}
}

func TestLogsAsksForATail(t *testing.T) {
	r := &stubRunner{replies: map[string]string{}}

	if _, err := (Compose{Runner: r}).Logs(t.Context(), testSandbox(), "web", 30); err != nil {
		t.Fatalf("Logs: %v", err)
	}

	args := strings.Join(r.calls[0].Args, " ")
	for _, want := range []string{"logs", "--no-color", "--tail 30", "web"} {
		if !strings.Contains(args, want) {
			t.Errorf("args %q are missing %q", args, want)
		}
	}
}

// psRunner answers `ps` with a service's state and `logs` with its
// last words.
type psRunner struct {
	stubRunner
	state string
}

func (r *psRunner) Output(_ context.Context, c proc.Command) ([]byte, error) {
	args := strings.Join(c.Args, " ")
	switch {
	case strings.Contains(args, " ps "):
		return []byte(`{"Service": "web", "Name": "p-web-1", "State": "` + r.state + `", "ExitCode": 3}` + "\n"), nil
	case strings.Contains(args, " logs "):
		return []byte("web-1  | Error: Cannot find module 'express'\n"), nil
	}
	return nil, nil
}

// A container that has ended will not answer: it is said at once.
func TestWaitReadyStopsWhenTheContainerHasExited(t *testing.T) {
	srv, _ := serverAnswering(t, func(int) int { return http.StatusBadGateway })
	for _, state := range []string{"exited", "dead"} {
		p := fastProbe(srv.URL)
		p.Timeout = time.Minute
		started := time.Now()
		err := Compose{Runner: &psRunner{state: state}}.WaitReady(t.Context(), testSandbox(), "web", p)
		if err == nil || !strings.Contains(err.Error(), "web exited with code 3 before it answered") ||
			!strings.Contains(err.Error(), "Cannot find module 'express'") || !strings.Contains(errs.Hint(err), "logs web") {
			t.Errorf("%s: err = %v", state, err)
		}
		if time.Since(started) > 10*time.Second {
			t.Errorf("%s: waited %v", state, time.Since(started))
		}
	}
	// One that is starting again may still answer.
	p := fastProbe(srv.URL)
	p.Timeout = 100 * time.Millisecond
	err := Compose{Runner: &psRunner{state: "restarting"}}.WaitReady(t.Context(), testSandbox(), "web", p)
	if err == nil || !strings.Contains(err.Error(), "did not become ready") {
		t.Errorf("restarting: err = %v", err)
	}
}
