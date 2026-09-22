// Package proc runs external programs. pit drives git, docker compose
// and gh as processes rather than through their libraries, so this is
// the one place that deals with cancellation, timeouts and turning a
// non-zero exit into an error a human can read.
package proc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/thannoz/pit/internal/errs"
)

// DefaultGracePeriod is how long a cancelled process may take to shut
// down on its own before it is killed. Compose needs a moment to stop
// containers; killing it immediately leaves them running.
const DefaultGracePeriod = 5 * time.Second

// Command describes one external program to run.
type Command struct {
	// Name is the executable, looked up on PATH.
	Name string
	// Args are passed to the executable, not through a shell: nothing
	// here is word-split, glob-expanded or interpreted.
	Args []string
	// Dir is the working directory. Empty means the current one.
	Dir string
	// Env is added to the environment the process inherits.
	Env []string
	// Stdin is fed to the process. Nil means no input.
	Stdin io.Reader
}

// String renders the command the way a user would type it, for error
// messages and logs. Arguments containing spaces are quoted.
func (c Command) String() string {
	parts := make([]string, 0, len(c.Args)+1)
	parts = append(parts, c.Name)
	for _, a := range c.Args {
		if strings.ContainsAny(a, " \t\"'") {
			parts = append(parts, fmt.Sprintf("%q", a))
			continue
		}
		parts = append(parts, a)
	}
	return strings.Join(parts, " ")
}

// Exec runs commands as real processes.
type Exec struct {
	// GracePeriod overrides DefaultGracePeriod when set.
	GracePeriod time.Duration
}

func (e Exec) grace() time.Duration {
	if e.GracePeriod > 0 {
		return e.GracePeriod
	}
	return DefaultGracePeriod
}

// Output runs c and returns what it wrote to stdout. Anything on stderr
// is folded into the error, where it is what the user needs to see.
func (e Exec) Output(ctx context.Context, c Command) ([]byte, error) {
	var stdout, stderr bytes.Buffer
	err := e.run(ctx, c, &stdout, &stderr)
	return stdout.Bytes(), decorate(c, err, stderr.String())
}

// Stream runs c and forwards its output as it arrives. Use it for
// long-running commands whose progress the user should see; use Output
// when the result is data to work with.
//
// Both channels are written under one lock. os/exec copies stdout and
// stderr on separate goroutines, so without it two half-lines can
// interleave — and a caller passing the same writer twice would race.
func (e Exec) Stream(ctx context.Context, c Command, stdout, stderr io.Writer) error {
	// The tail is kept so a failure can quote what went wrong even
	// though the output already scrolled past the user.
	tail := &tailBuffer{limit: 4096}

	var mu sync.Mutex
	out := lockedWriter{mu: &mu, w: stdout}
	errOut := lockedWriter{mu: &mu, w: io.MultiWriter(stderr, tail)}

	err := e.run(ctx, c, out, errOut)

	mu.Lock()
	captured := tail.String()
	mu.Unlock()

	return decorate(c, err, captured)
}

func (e Exec) run(ctx context.Context, c Command, stdout, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, c.Name, c.Args...)
	cmd.Dir = c.Dir
	cmd.Stdin = c.Stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Env = environment(c.Env)

	// Children of children have to die too: `docker compose up` and
	// `git fetch` both spawn helpers, and killing only the direct child
	// leaves those behind holding ports and locks.
	setProcessGroup(cmd)
	cmd.Cancel = func() error { return interrupt(cmd) }
	cmd.WaitDelay = e.grace()

	started := time.Now()
	slog.DebugContext(ctx, "running command", "cmd", c.String(), "dir", c.Dir)

	err := cmd.Run()

	slog.DebugContext(ctx, "command finished",
		"cmd", c.String(), "took", time.Since(started), "err", err)

	if ctx.Err() != nil {
		// The grace period is over either way by now; make sure no
		// grandchild outlived it.
		terminate(cmd)
	}
	return err
}

// stableLocale forces programs into the C locale. git, docker and gh all
// translate their messages, so the same failure reads differently on a
// German laptop and in CI -- and anything pit parses out of their output
// would work on one machine and not the other.
//
// LANGUAGE is listed because gettext gives it precedence over LC_ALL;
// setting the other two without clearing it leaves the messages
// translated.
var stableLocale = []string{"LC_ALL=C", "LANG=C", "LANGUAGE="}

// environment builds the environment a child process sees: the parent's,
// then the stable locale, then whatever the caller asked for. Later
// entries win, so a caller can still override the locale deliberately.
func environment(extra []string) []string {
	env := os.Environ()
	env = append(env, stableLocale...)
	return append(env, extra...)
}

// decorate turns a process failure into an error that says which command
// failed, how, and what the user might do about it.
func decorate(c Command, err error, stderr string) error {
	if err == nil {
		return nil
	}

	if errors.Is(err, exec.ErrNotFound) {
		return errs.Wrap(err, "%s is not installed or not on PATH", c.Name).
			WithHint("install %s and make sure it is on your PATH", c.Name)
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		msg := fmt.Sprintf("%s exited with code %d", c.Name, exitErr.ExitCode())
		if s := strings.TrimSpace(stderr); s != "" {
			msg += "\n" + indent(s)
		}
		return errs.Wrap(err, "%s", msg)
	}

	return errs.Wrap(err, "cannot run %s", c)
}

// indent offsets captured output so it reads as quoted material rather
// than as pit's own words.
func indent(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = "    " + l
	}
	return strings.Join(lines, "\n")
}

// lockedWriter serialises writes. Several of them may share one mutex,
// which is how stdout and stderr are kept from interleaving.
type lockedWriter struct {
	mu *sync.Mutex
	w  io.Writer
}

func (l lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// tailBuffer keeps only the last limit bytes written to it. It is not
// safe on its own; Stream guards it with the shared lock.
type tailBuffer struct {
	limit int
	buf   []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.limit {
		t.buf = t.buf[len(t.buf)-t.limit:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string { return string(t.buf) }
