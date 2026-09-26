package runtime

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/thannoz/pit/internal/errs"
)

// logTailOnFailure is how many lines of a service's output to quote
// when it never became ready. Enough to show a stack trace, few enough
// to read.
const logTailOnFailure = 30

// Probe says how to tell whether a sandbox is ready to be reviewed.
type Probe struct {
	// URL is polled until it answers as expected.
	URL string
	// ExpectStatus is the HTTP status that counts as ready.
	ExpectStatus int
	// Timeout is how long to keep trying in total.
	Timeout time.Duration
	// Interval is how long to wait between attempts.
	Interval time.Duration
	// Client is used for the requests; nil means a sensible default.
	Client *http.Client
}

// ExpandURL fills in the placeholders of a configured healthcheck URL.
// The port is pit's choice, one per sandbox, so it cannot be written
// into the repository's configuration.
func ExpandURL(template, host string, port int) string {
	r := strings.NewReplacer(
		"{host}", host,
		"{port}", strconv.Itoa(port),
	)
	return r.Replace(template)
}

// WaitReady polls until the probe succeeds, the timeout passes, or the
// context is cancelled.
//
// The first attempt happens immediately. A service that is already up
// should not cost the reviewer an interval of waiting for no reason.
func (c Compose) WaitReady(ctx context.Context, s Sandbox, service string, p Probe) error {
	attempts, err := Poll(ctx, p, nil)
	var late TimedOut
	if errors.As(err, &late) {
		return c.notReady(ctx, s, service, p, attempts, late.Last)
	}
	return err
}

// TimedOut is what Poll returns when the timeout passed: Last is why
// the last attempt failed.
type TimedOut struct{ Last error }

func (t TimedOut) Error() string { return "timed out: " + DescribeAttempt(t.Last) }

// Poll asks the probe's URL until it answers as expected, and returns
// how many times it asked. It stops early when ctx ends, and when
// gone, asked between attempts, says the service will not answer any
// more.
func Poll(ctx context.Context, p Probe, gone func() error) (int, error) {
	if p.Interval <= 0 {
		return 0, errs.New("the healthcheck interval must be positive")
	}

	client := p.Client
	if client == nil {
		// Each attempt gets its own deadline: a request that hangs
		// must not eat the whole budget in one go.
		client = &http.Client{Timeout: min(p.Interval*2, 10*time.Second)}
	}

	deadline := time.Now().Add(p.Timeout)
	ticker := time.NewTicker(p.Interval)
	defer ticker.Stop()

	attempts := 0
	for {
		attempts++
		last := attempt(ctx, client, p)
		if last == nil {
			return attempts, nil
		}

		// Cancellation is the user pressing Ctrl+C. It is not a
		// failure of the service and must not be reported as one.
		if ctx.Err() != nil {
			return attempts, ctx.Err()
		}
		if gone != nil {
			if err := gone(); err != nil {
				return attempts, err
			}
		}
		if time.Now().After(deadline) {
			return attempts, TimedOut{Last: last}
		}

		select {
		case <-ctx.Done():
			return attempts, ctx.Err()
		case <-ticker.C:
		}
	}
}

// attempt performs one request and reports whether it answered as the
// probe expects.
func attempt(ctx context.Context, client *http.Client, p Probe) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.URL, nil)
	if err != nil {
		return err
	}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close() //nolint:errcheck // nothing useful to do with a close failure here

	if resp.StatusCode != p.ExpectStatus {
		return fmt.Errorf("answered %d, want %d", resp.StatusCode, p.ExpectStatus)
	}
	return nil
}

// notReady builds the failure a reviewer sees. On its own "timed out"
// says nothing, so the service's own last words come with it.
func (c Compose) notReady(ctx context.Context, s Sandbox, service string, p Probe, attempts int, lastErr error) error {
	msg := NotReady(service, p, attempts, lastErr)

	if logs, err := c.Logs(ctx, s, service, logTailOnFailure); err == nil {
		if tail := strings.TrimSpace(string(logs)); tail != "" {
			msg += "\n\nthe last output from " + service + ":\n" + IndentLines(tail)
		}
	}

	return errs.New("%s", msg).
		WithHint("look at %s, or run `docker compose -p %s logs -f %s`", p.URL, s.Project, service)
}

// NotReady says that a service did not become ready, and how the last
// attempt went.
func NotReady(service string, p Probe, attempts int, last error) string {
	return fmt.Sprintf("%s did not become ready within %v (%s after %s)",
		service, p.Timeout, DescribeAttempt(last), plural(attempts, "attempt"))
}

// DescribeAttempt turns the last failure into a phrase that fits into
// a sentence.
func DescribeAttempt(err error) string {
	var target interface{ Timeout() bool }
	switch {
	case err == nil:
		return "no answer"
	case errors.As(err, &target) && target.Timeout():
		return "the request timed out"
	case strings.Contains(err.Error(), "connection refused"):
		return "nothing was listening"
	default:
		// Transport errors read as "Get \"http://...\": reason"; the
		// URL is already in the message around this.
		if _, reason, ok := strings.Cut(err.Error(), ": "); ok {
			return reason
		}
		return err.Error()
	}
}

// IndentLines indents each line, for output quoted in a message.
func IndentLines(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = "    " + l
	}
	return strings.Join(lines, "\n")
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}
