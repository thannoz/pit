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
	"time"

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
	// Processes says the services are processes on this machine, from
	// a Procfile, and Files are the Procfile and pit's plan for them.
	Processes bool
}

// Compose drives `docker compose`.
type Compose struct {
	Runner Runner
}

// Up builds and starts the sandbox's services in the background,
// forwarding Compose's own output so the reviewer can watch a build
// that takes minutes rather than staring at nothing.
func (c Compose) Up(ctx context.Context, s Sandbox, services []string, stdout, stderr io.Writer) error {
	// Without --build: what had to be built was built already, by
	// Build, and anything still missing an image Compose builds here
	// on its own. Passing --build as well would rebuild the services
	// this setup deliberately left alone.
	args := []string{"up", "--detach"}
	if len(services) == 0 {
		// Only when the whole project is being started: with a list,
		// Compose takes every container that is not on it for an
		// orphan and removes it.
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

// Build builds the named services. An empty list builds every service
// that has something to build.
func (c Compose) Build(ctx context.Context, s Sandbox, services []string, stdout, stderr io.Writer) error {
	args := append([]string{"build"}, services...)

	err := c.Runner.Stream(ctx, c.command(s, args...), stdout, stderr)
	if err != nil {
		return errs.Wrap(err, "cannot build %s", listOrAll(services)).
			WithHint("the build output above says what went wrong; `docker compose -p %s build` reproduces it", s.Project)
	}
	return nil
}

// Pull fetches one image by name.
//
// By name rather than by service, because this runs before the
// override that would name the image exists -- and one image at a
// time, because the answer pit needs is per service: this one arrived,
// that one has to be built.
func (c Compose) Pull(ctx context.Context, _ Sandbox, image string, stdout, stderr io.Writer) error {
	// An image that is already here is already the right one: pit only
	// ever asks for names that carry the commit, and a given commit's
	// image does not change. Asking the registry to confirm that costs
	// a round trip per service and answers nothing.
	local := proc.Command{Name: "docker", Args: []string{"image", "inspect", "--format", "{{.Id}}", image}}
	if _, err := c.Runner.Output(ctx, local); err == nil {
		return nil
	}

	// --quiet: a pull that finds nothing is an ordinary outcome here,
	// and progress bars for one that succeeds are not worth the
	// reviewer's screen.
	cmd := proc.Command{Name: "docker", Args: []string{"pull", "--quiet", image}}

	if err := c.Runner.Stream(ctx, cmd, stdout, stderr); err != nil {
		return errs.Wrap(err, "cannot pull %s", image)
	}
	return nil
}

// listOrAll names what was being built, for a message.
func listOrAll(services []string) string {
	if len(services) == 0 {
		return "the services"
	}
	return strings.Join(services, ", ")
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

// Pause freezes the named services.
func (c Compose) Pause(ctx context.Context, s Sandbox, services []string) error {
	if _, err := c.Runner.Output(ctx, c.command(s, append([]string{"pause"}, services...)...)); err != nil {
		return errs.Wrap(err, "cannot pause %s", strings.Join(services, ", "))
	}
	return nil
}

// Unpause lets the named services carry on.
func (c Compose) Unpause(ctx context.Context, s Sandbox, services []string) error {
	if _, err := c.Runner.Output(ctx, c.command(s, append([]string{"unpause"}, services...)...)); err != nil {
		return errs.Wrap(err, "cannot unpause %s", strings.Join(services, ", ")).
			WithHint("they are still frozen; `docker compose -p %s unpause` lets them go on", s.Project)
	}
	return nil
}

// Stop ends the named services and keeps their containers.
func (c Compose) Stop(ctx context.Context, s Sandbox, services []string) error {
	if _, err := c.Runner.Output(ctx, c.command(s, append([]string{"stop"}, services...)...)); err != nil {
		return errs.Wrap(err, "cannot stop %s", strings.Join(services, ", "))
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

// LogsSince implements Runtime. Docker stamps each line itself, so the
// time is when the line was written whether or not the service prints
// one.
func (c Compose) LogsSince(ctx context.Context, s Sandbox, service string, since time.Time) ([]LogLine, error) {
	args := []string{"logs", "--no-color", "--no-log-prefix", "--timestamps"}
	if !since.IsZero() {
		args = append(args, "--since", since.UTC().Format(time.RFC3339Nano))
	}
	args = append(args, service)

	out, err := c.Runner.Output(ctx, c.command(s, args...))
	if err != nil {
		return nil, errs.Wrap(err, "cannot read the logs of %s", service)
	}
	var lines []LogLine
	for _, raw := range strings.Split(string(out), "\n") {
		stamp, text, ok := strings.Cut(raw, " ")
		if !ok {
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, stamp)
		if err != nil {
			continue
		}
		lines = append(lines, LogLine{At: at, Text: text})
	}
	return lines, nil
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
