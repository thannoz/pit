package runtime

import (
	"context"
	"io"
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
	// Up builds and starts the services, forwarding their output.
	// A nil list means all of them; naming some leaves the rest
	// alone, which is what makes a second setup of the same pull
	// request cheap.
	Up(ctx context.Context, s Sandbox, services []string, stdout, stderr io.Writer) error
	// Down stops them and removes everything they brought with them.
	Down(ctx context.Context, s Sandbox, stdout, stderr io.Writer) error
	// Services lists the services the compose files declare.
	Services(ctx context.Context, s Sandbox) ([]string, error)
	// Port reports which host port a container port is published on.
	Port(ctx context.Context, s Sandbox, service string, containerPort int) (string, error)
	// Logs returns the tail of a service's output.
	Logs(ctx context.Context, s Sandbox, service string, tail int) ([]byte, error)
	// Status reports what each service is doing.
	Status(ctx context.Context, s Sandbox) ([]Status, error)
	// WaitReady polls until the sandbox answers as expected.
	WaitReady(ctx context.Context, s Sandbox, service string, p Probe) error
}

// Compose satisfies Runtime. The assertion is here so that a change to
// either side fails at build time rather than at the call site.
var _ Runtime = Compose{}
