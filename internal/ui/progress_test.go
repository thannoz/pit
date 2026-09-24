package ui

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestProgressWritesAStepPerLine(t *testing.T) {
	var out bytes.Buffer
	p := NewProgress(&out)

	p.Begin("fetch", false)
	p.Done("#482 at a3f91c2e4b7d")
	p.Begin("worktree", false)
	p.Done("/state/pit/acme-shop/pr-482")
	p.Blank()

	lines := nonEmptyLines(out.String())
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2:\n%s", len(lines), out.String())
	}
	for i, want := range []string{"fetch", "worktree"} {
		if !strings.Contains(lines[i], want) || !strings.Contains(lines[i], "✓") {
			t.Errorf("line %d = %q, want a tick and %q", i, lines[i], want)
		}
	}
	// The URL is the command's answer and belongs on stdout, not in
	// the narration. Repeating it here printed it three times in a row.
	if strings.Contains(out.String(), "http://") {
		t.Errorf("the narration carries the URL:\n%s", out.String())
	}
}

// TestNoAnimationWithoutATerminal is the requirement from T-314: a
// buffer, a pipe or a log file must come out as plain text.
func TestNoAnimationWithoutATerminal(t *testing.T) {
	var out bytes.Buffer
	p := NewProgress(&out)

	p.Begin("services", false)
	time.Sleep(3 * spinnerInterval) // long enough for several frames
	p.Done("pit-acme-shop-482")

	got := out.String()
	if strings.Contains(got, "\r") {
		t.Errorf("output carries carriage returns:\n%q", got)
	}
	if strings.Contains(got, "\033[") {
		t.Errorf("output carries escape sequences:\n%q", got)
	}
	for _, frame := range spinnerFrames {
		if strings.Contains(got, frame) {
			t.Errorf("output carries a spinner frame %q:\n%q", frame, got)
		}
	}
}

func TestDurationIsShownOnlyWhenItMatters(t *testing.T) {
	tests := []struct {
		name string
		took time.Duration
		want bool
	}{
		{"instant", 10 * time.Millisecond, false},
		{"just under half a second", 400 * time.Millisecond, false},
		{"a second", time.Second, true},
		{"a minute", time.Minute, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatTook(tt.took)
			if (got != "") != tt.want {
				t.Errorf("formatTook(%v) = %q, want shown=%v", tt.took, got, tt.want)
			}
		})
	}
}

func TestDurationReadsAsAPersonWouldSayIt(t *testing.T) {
	tests := []struct {
		took time.Duration
		want string
	}{
		{1500 * time.Millisecond, "(1.5s)"},
		{24 * time.Second, "(24s)"},
		{90 * time.Second, "(90s)"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := formatTook(tt.took); !strings.Contains(got, tt.want) {
				t.Errorf("formatTook(%v) = %q, want it to contain %q", tt.took, got, tt.want)
			}
		})
	}
}

func TestDoneWithoutBeginDoesNotPanic(t *testing.T) {
	// A step that fails before it is announced would otherwise take
	// the process down with it.
	var out bytes.Buffer
	p := NewProgress(&out)

	p.Done("something happened")
	if out.Len() == 0 {
		t.Error("nothing was written")
	}
}

func TestStepsMeasureOnlyTheirOwnTime(t *testing.T) {
	var out bytes.Buffer
	p := NewProgress(&out)

	p.Begin("slow", false)
	time.Sleep(600 * time.Millisecond)
	p.Done("first")

	p.Begin("quick", false)
	p.Done("second")

	lines := nonEmptyLines(out.String())
	if !strings.Contains(lines[0], "(0.6s)") && !strings.Contains(lines[0], "(0.7s)") {
		t.Errorf("the slow step reports %q, want about 0.6s", lines[0])
	}
	// The quick step must not inherit the slow one's duration.
	if strings.Contains(lines[1], "s)") {
		t.Errorf("the quick step reports a duration: %q", lines[1])
	}
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

// TestAnimationOnATerminal is the other half of T-314: on a terminal
// the step is animated while it runs, and the animation is erased
// before the finished line is printed.
func TestAnimationOnATerminal(t *testing.T) {
	out := &syncWriter{}
	p := newProgress(out, true)

	p.Begin("fetch", false)
	time.Sleep(4 * spinnerInterval)
	p.Done("#482 at a3f91c2e4b7d")

	got := out.String()
	if !containsAnyFrame(got) {
		t.Errorf("no spinner frame was written:\n%q", got)
	}
	if !strings.Contains(got, "\r\033[K") {
		t.Errorf("the animation was not erased before the result:\n%q", got)
	}

	// Whatever the animation did, the last line has to be the finished
	// step, not a half-drawn frame.
	last := visibleLast(got)
	if !strings.Contains(last, "✓") || !strings.Contains(last, "fetch") {
		t.Errorf("the last line is %q, want the finished step", last)
	}
	if containsAnyFrame(last) {
		t.Errorf("the last line still carries a spinner frame: %q", last)
	}
}

// TestNoAnimationForAStepThatWritesItsOwnOutput records why Begin takes
// a flag: a spinner drawn under a build's own lines leaves fragments
// between them.
func TestNoAnimationForAStepThatWritesItsOwnOutput(t *testing.T) {
	out := &syncWriter{}
	p := newProgress(out, true)

	p.Begin("services", true)
	time.Sleep(4 * spinnerInterval)
	p.Done("pit-acme-shop-482")

	if containsAnyFrame(out.String()) {
		t.Errorf("a streaming step was animated:\n%q", out.String())
	}
}

func containsAnyFrame(s string) bool {
	for _, f := range spinnerFrames {
		if strings.Contains(s, f) {
			return true
		}
	}
	return false
}

// visibleLast is what a terminal would still be showing: everything
// after the last carriage return, which is how the spinner overwrites
// itself. Splitting on newlines alone would return the whole animation
// as one "line".
func visibleLast(s string) string {
	if i := strings.LastIndex(s, "\r"); i >= 0 {
		s = s[i+1:]
	}
	return strings.ReplaceAll(s, "\033[K", "")
}

// syncWriter is a buffer safe to read while the spinner goroutine is
// writing to it.
type syncWriter struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (w *syncWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *syncWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

func TestSizeReadsAsAPersonWouldSayIt(t *testing.T) {
	for n, want := range map[int64]string{
		0:             "0 B",
		999:           "999 B",
		1000:          "1.0 kB",
		1_234_567:     "1.2 MB",
		7_689_307:     "7.7 MB",
		2_100_000_000: "2.1 GB",
	} {
		if got := Size(n); got != want {
			t.Errorf("Size(%d) = %q, want %q", n, got, want)
		}
	}
}

// Took is shown however short: for a snapshot the time is part of the
// answer, not commentary.
func TestTookIsAlwaysShown(t *testing.T) {
	for d, want := range map[time.Duration]string{
		0:                       "0ms",
		164 * time.Millisecond:  "164ms",
		1400 * time.Millisecond: "1.4s",
		14 * time.Second:        "14s",
	} {
		if got := Took(d); got != want {
			t.Errorf("Took(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestFinishLeavesTheDurationToTheDetail(t *testing.T) {
	var b strings.Builder
	p := NewProgress(&b)
	p.Begin("snapshot", true)
	time.Sleep(600 * time.Millisecond) // long enough that Done would add its own
	p.Finish("sn_7f3a1b  1.4 MB, 600ms")
	if got := b.String(); got != "  ✓ snapshot   sn_7f3a1b  1.4 MB, 600ms\n" {
		t.Errorf("Finish wrote %q", got)
	}
}
