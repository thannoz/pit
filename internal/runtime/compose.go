// Package runtime owns the container lifecycle of a sandbox: bringing
// services up, tearing them down, reading logs. The first and so far
// only implementation drives Docker Compose.
package runtime

import (
	"context"
	"encoding/json"
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
func (c Compose) Up(ctx context.Context, s Sandbox, services []string, stdout, stderr io.Writer) error {
	args := []string{"up", "--detach", "--build"}
	if len(services) == 0 {
		// Only when the whole project is being brought up: with a
		// list, Compose would take every container that is not on it
		// for an orphan.
		args = append(args, "--remove-orphans")
	}
	args = append(args, services...)

	err := c.Runner.Stream(ctx, c.command(s, args...), stdout, stderr)
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

// ComposeCommand assembles a docker compose invocation for a sandbox.
// The project name comes before the subcommand, because Compose treats
// it as a global flag and rejects it after one.
//
// It is exported because `pit logs` and `pit shell` are little more
// than this call: they should not have to reassemble the isolation
// flags, nor build a string only to have it taken apart again.
func ComposeCommand(s Sandbox, args ...string) proc.Command {
	full := []string{"compose", "--project-name", s.Project}
	for _, f := range s.Files {
		full = append(full, "--file", f)
	}
	return proc.Command{Name: "docker", Args: append(full, args...), Dir: s.Dir}
}

func (c Compose) command(s Sandbox, args ...string) proc.Command {
	return ComposeCommand(s, args...)
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

// composePS mirrors the shape of `docker compose ps --format json`,
// which emits one JSON object per line rather than an array.
type composePS struct {
	Name     string `json:"Name"`
	Service  string `json:"Service"`
	State    string `json:"State"`
	Health   string `json:"Health"`
	ExitCode int    `json:"ExitCode"`
}

// Status reports what each of the sandbox's services is doing. A
// sandbox whose services have quietly exited looks identical to one
// that was never started, and telling them apart is what lets pit
// reconcile its records with reality.
func (c Compose) Status(ctx context.Context, s Sandbox) ([]Status, error) {
	out, err := c.Runner.Output(ctx, c.command(s, "ps", "--all", "--format", "json"))
	if err != nil {
		return nil, errs.Wrap(err, "cannot read the state of %s", s.Project)
	}

	var statuses []Status
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var p composePS
		if err := json.Unmarshal([]byte(line), &p); err != nil {
			return nil, errs.Wrap(err, "cannot read what docker compose reported").
				WithHint("check that `docker compose version` is 2.0 or newer")
		}
		// Listed field by field rather than converted: composePS
		// mirrors Docker's JSON and Status is pit's own. They happen
		// to overlap today, and treating that as a guarantee would
		// break the moment either side gains a field.
		statuses = append(statuses, Status{
			Service:   p.Service,
			Container: p.Name,
			State:     p.State,
			Health:    p.Health,
			ExitCode:  p.ExitCode,
		})
	}
	return statuses, nil
}
