package runtime

import (
	"context"
	"io"
	"time"
)

// Either runs a sandbox with Compose, or as processes when it is one of
// processes: which one is the sandbox's to say, so that every command
// takes a sandbox down the way it was brought up.
type Either struct {
	Compose   Runtime
	Processes Runtime
}

var _ Runtime = Either{}

// of is the sandbox's runtime's.
func (e Either) of(s Sandbox) Runtime {
	if s.Processes {
		return e.Processes
	}
	return e.Compose
}

// Up is the sandbox's runtime's.
func (e Either) Up(ctx context.Context, s Sandbox, services []string, stdout, stderr io.Writer) error {
	return e.of(s).Up(ctx, s, services, stdout, stderr)
}

// Build is the sandbox's runtime's.
func (e Either) Build(ctx context.Context, s Sandbox, services []string, stdout, stderr io.Writer) error {
	return e.of(s).Build(ctx, s, services, stdout, stderr)
}

// Pull is the sandbox's runtime's.
func (e Either) Pull(ctx context.Context, s Sandbox, image string, stdout, stderr io.Writer) error {
	return e.of(s).Pull(ctx, s, image, stdout, stderr)
}

// Down is the sandbox's runtime's.
func (e Either) Down(ctx context.Context, s Sandbox, stdout, stderr io.Writer) error {
	return e.of(s).Down(ctx, s, stdout, stderr)
}

// Services is the sandbox's runtime's.
func (e Either) Services(ctx context.Context, s Sandbox) ([]string, error) {
	return e.of(s).Services(ctx, s)
}

// Port is the sandbox's runtime's.
func (e Either) Port(ctx context.Context, s Sandbox, service string, containerPort int) (string, error) {
	return e.of(s).Port(ctx, s, service, containerPort)
}

// Logs is the sandbox's runtime's.
func (e Either) Logs(ctx context.Context, s Sandbox, service string, tail int) ([]byte, error) {
	return e.of(s).Logs(ctx, s, service, tail)
}

// LogsSince is the sandbox's runtime's.
func (e Either) LogsSince(ctx context.Context, s Sandbox, service string, since time.Time) ([]LogLine, error) {
	return e.of(s).LogsSince(ctx, s, service, since)
}

// Status is the sandbox's runtime's.
func (e Either) Status(ctx context.Context, s Sandbox) ([]Status, error) {
	return e.of(s).Status(ctx, s)
}

// Pause is the sandbox's runtime's.
func (e Either) Pause(ctx context.Context, s Sandbox, services []string) error {
	return e.of(s).Pause(ctx, s, services)
}

// Unpause is the sandbox's runtime's.
func (e Either) Unpause(ctx context.Context, s Sandbox, services []string) error {
	return e.of(s).Unpause(ctx, s, services)
}

// Stop is the sandbox's runtime's.
func (e Either) Stop(ctx context.Context, s Sandbox, services []string) error {
	return e.of(s).Stop(ctx, s, services)
}

// WaitReady is the sandbox's runtime's.
func (e Either) WaitReady(ctx context.Context, s Sandbox, service string, p Probe) error {
	return e.of(s).WaitReady(ctx, s, service, p)
}
