package hooks

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/proc"
)

// recordingRunner remembers what it was asked to run, and fails on
// demand.
type recordingRunner struct {
	mu     sync.Mutex
	calls  []proc.Command
	failAt int // 1-based; 0 means never
}

func (r *recordingRunner) Stream(_ context.Context, c proc.Command, _, _ io.Writer) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.calls = append(r.calls, c)
	if r.failAt == len(r.calls) {
		return errors.New("exit status 1")
	}
	return nil
}

func TestRunExecutesInOrder(t *testing.T) {
	// A seed that runs before its migration fails in a way that is
	// tedious to diagnose, so the author's order is the order.
	r := &recordingRunner{}
	lines := []string{"migrate", "seed", "warm-cache"}

	if err := Run(t.Context(), r, lines, sandbox(), io.Discard, io.Discard); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(r.calls) != 3 {
		t.Fatalf("ran %d hooks, want 3", len(r.calls))
	}
	for i, want := range lines {
		if r.calls[i].Name != want {
			t.Errorf("hook %d was %q, want %q", i+1, r.calls[i].Name, want)
		}
	}
}

func TestRunStopsAtTheFirstFailure(t *testing.T) {
	// Carrying on would produce a sandbox that looks ready and is not,
	// which is worse than no sandbox.
	r := &recordingRunner{failAt: 2}

	err := Run(t.Context(), r, []string{"first", "second", "third"}, sandbox(), io.Discard, io.Discard)
	if err == nil {
		t.Fatal("want an error")
	}
	if len(r.calls) != 2 {
		t.Errorf("ran %d hooks, want it to stop after 2", len(r.calls))
	}
}

func TestRunNamesTheFailingHook(t *testing.T) {
	r := &recordingRunner{failAt: 2}
	lines := []string{"compose exec -T db true", "compose exec -T api npm run migrate"}

	err := Run(t.Context(), r, lines, sandbox(), io.Discard, io.Discard)
	if err == nil {
		t.Fatal("want an error")
	}

	msg := err.Error()
	if !strings.Contains(msg, "hook 2") {
		t.Errorf("message does not say which hook failed:\n%s", msg)
	}
	if !strings.Contains(msg, "npm run migrate") {
		t.Errorf("message does not quote the failing line:\n%s", msg)
	}
	if errs.Hint(err) == "" {
		t.Error("the error carries no hint")
	}
}

func TestRunRejectsABrokenLineBeforeRunningAnything(t *testing.T) {
	// Expanding everything up front means a typo in the last hook does
	// not leave the first three already applied.
	r := &recordingRunner{}

	err := Run(t.Context(), r, []string{"ok", `broken "quote`}, sandbox(), io.Discard, io.Discard)
	if err == nil {
		t.Fatal("want an error")
	}
	if len(r.calls) != 0 {
		t.Errorf("ran %d hooks although one line could not be parsed", len(r.calls))
	}
}

func TestRunForwardsOutput(t *testing.T) {
	var out bytes.Buffer
	r := writingRunner{text: "migrating...\n"}

	if err := Run(t.Context(), r, []string{"migrate"}, sandbox(), &out, io.Discard); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := out.String(); got != "migrating...\n" {
		t.Errorf("stdout = %q, want the hook's output", got)
	}
}

type writingRunner struct{ text string }

func (w writingRunner) Stream(_ context.Context, _ proc.Command, stdout, _ io.Writer) error {
	_, err := io.WriteString(stdout, w.text)
	return err
}

func TestRunWithNoHooks(t *testing.T) {
	r := &recordingRunner{}

	if err := Run(t.Context(), r, nil, sandbox(), io.Discard, io.Discard); err != nil {
		t.Errorf("Run with no hooks: %v", err)
	}
	if len(r.calls) != 0 {
		t.Errorf("ran %d hooks although none are configured", len(r.calls))
	}
}
