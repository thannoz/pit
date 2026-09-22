// Package runtimetest provides a Runtime that needs no Docker, so the
// code above it can be tested for behaviour rather than for whether a
// daemon happened to be running.
package runtimetest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"

	"github.com/thannoz/pit/internal/runtime"
)

// Call records one thing that was asked of the Fake.
type Call struct {
	Method  string
	Project string
	Service string
}

// Fake is an in-memory Runtime.
//
// It models just enough to be wrong in useful ways: a sandbox that was
// never started, services that are not running, a port that is not
// published. Tests that only ever see success prove very little.
type Fake struct {
	mu sync.Mutex

	// Declared are the services the compose files would declare.
	Declared []string
	// Published maps "service:containerPort" to the host port.
	Published map[string]int
	// Tail is what Logs returns.
	Tail string
	// ReadyAfter is how many WaitReady calls fail before one succeeds.
	ReadyAfter int
	// Fail maps a method name to the error it should return.
	Fail map[string]error

	up    map[string]bool
	calls []Call
	ready int
}

// New returns a Fake that behaves like a working single-service
// project, which is the starting point most tests want.
func New(services ...string) *Fake {
	if len(services) == 0 {
		services = []string{"web"}
	}
	return &Fake{
		Declared:  services,
		Published: map[string]int{services[0] + ":80": 49580},
		up:        map[string]bool{},
		Fail:      map[string]error{},
	}
}

var _ runtime.Runtime = (*Fake)(nil)

// Up marks the sandbox as running.
func (f *Fake) Up(_ context.Context, s runtime.Sandbox, stdout, _ io.Writer) error {
	f.record("Up", s.Project, "")
	if err := f.failure("Up"); err != nil {
		return err
	}

	f.mu.Lock()
	f.up[s.Project] = true
	f.mu.Unlock()

	_, _ = io.WriteString(stdout, "Container "+s.Project+"-"+f.Declared[0]+"-1 Started\n")
	return nil
}

// Down marks it as stopped.
func (f *Fake) Down(_ context.Context, s runtime.Sandbox, _, _ io.Writer) error {
	f.record("Down", s.Project, "")
	if err := f.failure("Down"); err != nil {
		return err
	}

	f.mu.Lock()
	delete(f.up, s.Project)
	f.mu.Unlock()
	return nil
}

// Services lists the declared services.
func (f *Fake) Services(_ context.Context, s runtime.Sandbox) ([]string, error) {
	f.record("Services", s.Project, "")
	if err := f.failure("Services"); err != nil {
		return nil, err
	}
	return f.Declared, nil
}

// Port answers only for services that are up, the way Docker does.
func (f *Fake) Port(_ context.Context, s runtime.Sandbox, service string, containerPort int) (string, error) {
	f.record("Port", s.Project, service)
	if err := f.failure("Port"); err != nil {
		return "", err
	}
	if !f.IsUp(s.Project) {
		return "", errors.New("no container is running for " + service)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	port, ok := f.Published[service+":"+strconv.Itoa(containerPort)]
	if !ok {
		return "", nil // Docker answers with nothing, not an error
	}
	return strconv.Itoa(port), nil
}

// Logs returns whatever the test put in Tail.
func (f *Fake) Logs(_ context.Context, s runtime.Sandbox, service string, _ int) ([]byte, error) {
	f.record("Logs", s.Project, service)
	if err := f.failure("Logs"); err != nil {
		return nil, err
	}
	return []byte(f.Tail), nil
}

// Status reports every declared service as running while the sandbox
// is up, and as exited once it is not.
func (f *Fake) Status(_ context.Context, s runtime.Sandbox) ([]runtime.Status, error) {
	f.record("Status", s.Project, "")
	if err := f.failure("Status"); err != nil {
		return nil, err
	}

	state, code := "exited", 0
	if f.IsUp(s.Project) {
		state = "running"
	}

	out := make([]runtime.Status, 0, len(f.Declared))
	for _, svc := range f.Declared {
		out = append(out, runtime.Status{Service: svc, State: state, ExitCode: code})
	}
	return out, nil
}

// WaitReady succeeds after ReadyAfter failures.
func (f *Fake) WaitReady(_ context.Context, s runtime.Sandbox, service string, _ runtime.Probe) error {
	f.record("WaitReady", s.Project, service)
	if err := f.failure("WaitReady"); err != nil {
		return err
	}
	if !f.IsUp(s.Project) {
		return errors.New(service + " is not running")
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	f.ready++
	if f.ready <= f.ReadyAfter {
		return fmt.Errorf("%s did not become ready (attempt %d)", service, f.ready)
	}
	return nil
}

// IsUp reports whether a project is currently running.
func (f *Fake) IsUp(project string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.up[project]
}

// Calls returns what the Fake was asked to do, in order.
func (f *Fake) Calls() []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Call(nil), f.calls...)
}

// Methods returns just the method names, which is what most assertions
// are about.
func (f *Fake) Methods() []string {
	out := make([]string, 0, len(f.Calls()))
	for _, c := range f.Calls() {
		out = append(out, c.Method)
	}
	return out
}

func (f *Fake) record(method, project, service string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, Call{Method: method, Project: project, Service: service})
}

func (f *Fake) failure(method string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Fail[method]
}
