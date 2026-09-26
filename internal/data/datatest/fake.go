// Package datatest provides a Store that touches no database, so the
// code above it can be tested for what it asks for rather than for
// whether Postgres happened to be running.
package datatest

import (
	"context"
	"io"
	"sync"

	"github.com/thannoz/pit/internal/data"
)

// Call records one Apply.
type Call struct {
	Project  string
	Scenario string
	// Commands are every command the scenario would have run, base
	// first and flattened: what a test asserts about is usually that
	// the right data was asked for, not how it was layered.
	Commands []string
	// Steps keeps the layering for the tests that are about it.
	Steps []data.Step
	// Env is what the commands would have been given.
	Env []string
}

// Fake is an in-memory Store.
type Fake struct {
	mu sync.Mutex

	// Err is what Apply returns, so a test can see what a fixture that
	// fails does to a setup.
	Err error
	// Output is written to stdout, the way a real apply command
	// reports what it loaded.
	Output string

	calls []Call
}

// New returns a Fake that accepts every scenario.
func New() *Fake { return &Fake{} }

var _ data.Store = (*Fake)(nil)

// Apply records the request and returns whatever the test asked for.
func (f *Fake) Apply(_ context.Context, s data.Sandbox, sc data.Scenario, stdout, _ io.Writer) error {
	f.mu.Lock()
	f.calls = append(f.calls, Call{
		Project:  s.Project,
		Scenario: sc.Name,
		Commands: sc.Commands(),
		Steps:    append([]data.Step(nil), sc.Steps...),
		Env:      s.Env,
	})
	f.mu.Unlock()

	if f.Output != "" {
		if _, err := io.WriteString(stdout, f.Output); err != nil {
			return err
		}
	}
	return f.Err
}

// Calls returns what the Fake was asked to apply, in order.
func (f *Fake) Calls() []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Call(nil), f.calls...)
}

// Applied returns just the scenario names, which is what most
// assertions are about.
func (f *Fake) Applied() []string {
	out := make([]string, 0, len(f.Calls()))
	for _, c := range f.Calls() {
		out = append(out, c.Scenario)
	}
	return out
}
