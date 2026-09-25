package runtime

import (
	"context"
	"io"
	"time"
)

// Status is what one service is doing.
type Status struct {
	// Service is the compose service name.
	Service string
	// Container is the container's own name, which is what a reviewer
	// passes to docker commands and what cleanup works from when the
	// only thing left after a crash is a list of names.
	Container string
	// State is Docker's own word for it: running, exited, restarting.
	State string
	// Health is the container's healthcheck verdict, when it has one.
	Health string
	// ExitCode is set once the container has stopped.
	ExitCode int
}

// Running reports whether the service is up.
func (s Status) Running() bool { return s.State == "running" }

// Runtime brings a sandbox's services up, takes them down, and answers
// questions about them.
//
// Docker Compose is the only implementation today. The interface exists
// so that the devcontainer and process runners planned for P10 are new
// files rather than a rewrite -- and, more immediately, so that the
// code above it can be tested without Docker.
//
// The signatures differ from the sketch in the architecture document:
// Up and Down take writers because a build that takes minutes has to be
// watchable, and Logs returns bytes rather than a reader because every
// caller wants the whole tail at once.
type Runtime interface {
	// Up starts the named services, building anything that still has
	// no image, and forwards their output. An empty list starts the
	// whole project; naming some starts only those, which is how a
	// review of eight services can run four.
	Up(ctx context.Context, s Sandbox, services []string, stdout, stderr io.Writer) error
	// Build builds the named services, or every one of them when the
	// list is empty. It is separate from Up because which services
	// have to be built is a question with an interesting answer:
	// only the ones a commit touched, and not the ones whose image a
	// pipeline has already published.
	Build(ctx context.Context, s Sandbox, services []string, stdout, stderr io.Writer) error
	// Pull fetches one image by name, and fails when there is none to
	// fetch -- which is how pit finds out whether it has to build.
	Pull(ctx context.Context, s Sandbox, image string, stdout, stderr io.Writer) error
	// Down stops them and removes everything they brought with them.
	Down(ctx context.Context, s Sandbox, stdout, stderr io.Writer) error
	// Services lists the services the compose files declare.
	Services(ctx context.Context, s Sandbox) ([]string, error)
	// Port reports which host port a container port is published on.
	Port(ctx context.Context, s Sandbox, service string, containerPort int) (string, error)
	// Logs returns the tail of a service's output.
	Logs(ctx context.Context, s Sandbox, service string, tail int) ([]byte, error)
	// LogsSince returns what a service has written since a moment --
	// all of it for the zero time -- each line with when it was
	// written. pit what reads the requests a reviewer made from it.
	LogsSince(ctx context.Context, s Sandbox, service string, since time.Time) ([]LogLine, error)
	// Status reports what each service is doing.
	Status(ctx context.Context, s Sandbox) ([]Status, error)
	// Pause freezes the named services' processes and Unpause lets
	// them carry on. A paused service keeps its memory and its
	// connections; it only stops running for a while, which is what
	// makes it the way to keep an application from writing while its
	// databases are saved.
	Pause(ctx context.Context, s Sandbox, services []string) error
	Unpause(ctx context.Context, s Sandbox, services []string) error
	// Stop ends the named services' processes and leaves their
	// containers. Unlike Pause it closes their connections, which is
	// what makes a database account for everything they wrote.
	Stop(ctx context.Context, s Sandbox, services []string) error
	// WaitReady polls until the sandbox answers as expected.
	WaitReady(ctx context.Context, s Sandbox, service string, p Probe) error
}

// Compose satisfies Runtime. The assertion is here so that a change to
// either side fails at build time rather than at the call site.
var _ Runtime = Compose{}

// LogLine is one line of a service's output, and when it was written.
type LogLine struct {
	At   time.Time
	Text string
}
