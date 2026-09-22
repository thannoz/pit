// Package runtime owns the container lifecycle of a sandbox: bringing
// services up, tearing them down, reading logs. The first and so far
// only implementation drives Docker Compose.
package runtime

import (
	"context"
	"io"
	"strconv"
	"strings"

	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/proc"
)

// Runner runs external commands. internal/proc.Exec satisfies it.
type Runner interface {
	Output(ctx context.Context, c proc.Command) ([]byte, error)
	Stream(ctx context.Context, c proc.Command, stdout, stderr io.Writer) error
}

// Sandbox is one pull request's set of services.
type Sandbox struct {
	// Project is the Compose project name. It is the only isolation
	// mechanism needed: Docker derives network and volume names from
	// it, so two sandboxes never share either.
	Project string
	// Dir is the worktree the services are defined and built in.
	Dir string
	// Files are the compose files to use, relative to Dir. Empty means
	// Compose looks for its own defaults.
	Files []string
}

// Compose drives `docker compose`.
type Compose struct {
	Runner Runner
}

// Up builds and starts the sandbox's services in the background,
// forwarding Compose's own output so the reviewer can watch a build
// that takes minutes rather than staring at nothing.
func (c Compose) Up(ctx context.Context, s Sandbox, stdout, stderr io.Writer) error {
	err := c.Runner.Stream(ctx, c.command(s, "up", "--detach", "--build", "--remove-orphans"), stdout, stderr)
	if err != nil {
		return errs.Wrap(err, "cannot start the services").
			WithHint("check the compose file in %s, or run `docker compose -p %s logs`", s.Dir, s.Project)
	}
	return nil
}

// Down stops the services and removes everything they brought with
// them. Volumes go too: a sandbox that leaves its database behind is
// not a sandbox.
func (c Compose) Down(ctx context.Context, s Sandbox, stdout, stderr io.Writer) error {
	err := c.Runner.Stream(ctx, c.command(s, "down", "--volumes", "--remove-orphans"), stdout, stderr)
	if err != nil {
		return errs.Wrap(err, "cannot stop the services").
			WithHint("remove them by hand with `docker compose -p %s down -v`", s.Project)
	}
	return nil
}

// Services lists the names of the services the compose files define.
func (c Compose) Services(ctx context.Context, s Sandbox) ([]string, error) {
	out, err := c.Runner.Output(ctx, c.command(s, "config", "--services"))
	if err != nil {
		return nil, errs.Wrap(err, "cannot read the compose configuration in %s", s.Dir).
			WithHint("check that the compose file is valid: `docker compose config`")
	}
	return strings.Fields(string(out)), nil
}

// Port reports which host port a service's container port is published
// on. Compose answers in the form "0.0.0.0:41482".
func (c Compose) Port(ctx context.Context, s Sandbox, service string, containerPort int) (string, error) {
	out, err := c.Runner.Output(ctx, c.command(s, "port", service, strconv.Itoa(containerPort)))
	if err != nil {
		return "", errs.Wrap(err, "cannot find the published port for %s", service)
	}

	mapping := strings.TrimSpace(string(out))
	if mapping == "" {
		return "", errs.New("%s does not publish port %d", service, containerPort).
			WithHint("add a ports entry for %s in the compose file", service)
	}

	// Only the port matters; the bind address is an implementation
	// detail, and an IPv6 address would break a naive split.
	if i := strings.LastIndex(mapping, ":"); i >= 0 {
		return mapping[i+1:], nil
	}
	return mapping, nil
}

// command assembles a docker compose invocation. The project name comes
// before the subcommand, because Compose treats it as a global flag.
func (c Compose) command(s Sandbox, args ...string) proc.Command {
	full := []string{"compose", "--project-name", s.Project}
	for _, f := range s.Files {
		full = append(full, "--file", f)
	}
	return proc.Command{Name: "docker", Args: append(full, args...), Dir: s.Dir}
}

// Logs returns the tail of a service's output. It is what a failure
// report needs: the reason a container never became ready is almost
// always in its own last few lines.
func (c Compose) Logs(ctx context.Context, s Sandbox, service string, tail int) ([]byte, error) {
	args := []string{"logs", "--no-color", "--tail", strconv.Itoa(tail)}
	if service != "" {
		args = append(args, service)
	}

	out, err := c.Runner.Output(ctx, c.command(s, args...))
	if err != nil {
		return nil, errs.Wrap(err, "cannot read the logs of %s", service)
	}
	return out, nil
}
