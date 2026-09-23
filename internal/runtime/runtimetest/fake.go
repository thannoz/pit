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
	// Services is what Up was asked to act on: empty means the whole
	// project, which is the difference between a rebuild and an
	// incremental one.
	Services []string
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
	// Prebuilt names the images a registry would hand over. Anything
	// not in it has to be built, which is the ordinary case.
	Prebuilt map[string]bool

	// running holds the projects whose containers exist, and whether
	// they are up. A project that is absent has no containers at all,
	// which is what compose reports after a down.
	running map[string]bool
	calls   []Call
	ready   int
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
		running:   map[string]bool{},
		Fail:      map[string]error{},
		Prebuilt:  map[string]bool{},
	}
}

var _ runtime.Runtime = (*Fake)(nil)

// Up marks the sandbox as running and remembers which services it was
// asked for, which is the whole question an incremental setup turns on.
func (f *Fake) Up(_ context.Context, s runtime.Sandbox, services []string, stdout, _ io.Writer) error {
	f.mu.Lock()
	f.calls = append(f.calls, Call{
		Method:   "Up",
		Project:  s.Project,
		Services: append([]string(nil), services...),
	})
	f.mu.Unlock()

	if err := f.failure("Up"); err != nil {
		return err
	}

	f.mu.Lock()
	f.running[s.Project] = true
	f.mu.Unlock()

	_, _ = io.WriteString(stdout, "Container "+s.Project+"-"+f.Declared[0]+"-1 Started\n")
	return nil
}

// Build records which services it was asked to build, which is the
// whole question an incremental setup turns on.
func (f *Fake) Build(_ context.Context, s runtime.Sandbox, services []string, _, _ io.Writer) error {
	f.mu.Lock()
	f.calls = append(f.calls, Call{
		Method:   "Build",
		Project:  s.Project,
		Services: append([]string(nil), services...),
	})
	f.mu.Unlock()

	return f.failure("Build")
}

// Pull succeeds for the images named in Prebuilt and fails for the
// rest, the way a registry answers for an image nobody pushed.
func (f *Fake) Pull(_ context.Context, s runtime.Sandbox, image string, _, _ io.Writer) error {
	f.record("Pull", s.Project, image)
	if err := f.failure("Pull"); err != nil {
		return err
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.Prebuilt[image] {
		return errors.New("no image named " + image)
	}
	return nil
}

// Down marks it as stopped.
func (f *Fake) Down(_ context.Context, s runtime.Sandbox, _, _ io.Writer) error {
	f.record("Down", s.Project, "")
	if err := f.failure("Down"); err != nil {
		return err
	}

	// Down removes the containers, so the project is not stopped but
	// gone -- which is what makes its volumes gone too.
	f.mu.Lock()
	delete(f.running, s.Project)
	f.mu.Unlock()
	return nil
}

// Stop models `compose stop`: the containers are still there, and none
// of them is running.
func (f *Fake) Stop(project string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.running[project] = false
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

// Status reports what the containers of a project are doing, and
// nothing at all for a project whose containers were removed.
func (f *Fake) Status(_ context.Context, s runtime.Sandbox) ([]runtime.Status, error) {
	f.record("Status", s.Project, "")
	if err := f.failure("Status"); err != nil {
		return nil, err
	}

	f.mu.Lock()
	up, exists := f.running[s.Project]
	f.mu.Unlock()
	if !exists {
		return nil, nil
	}

	state, code := "exited", 0
	if up {
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
	return f.running[project]
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
