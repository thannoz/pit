package doctor

import (
	"context"
	"fmt"
	"strings"

	"github.com/thannoz/pit/internal/config"
	"github.com/thannoz/pit/internal/ports"
	"github.com/thannoz/pit/internal/proc"
	"github.com/thannoz/pit/internal/runtime"
	"github.com/thannoz/pit/internal/state"
)

// Runner runs the external commands the checks ask about.
type Runner interface {
	Output(ctx context.Context, c proc.Command) ([]byte, error)
}

// Environment is what the checks need to know about this machine.
type Environment struct {
	// Runner runs external commands.
	Runner Runner
	// Store is pit's record, or nil when it could not be opened.
	Store *state.Store
	// StateDir is where pit keeps its files.
	StateDir string
	// WorkDir is where the command was run, used to look for a
	// configuration. It may be outside a repository, and that is fine.
	WorkDir string
}

// Default is the list of checks pit doctor runs.
func Default(env Environment) []Check {
	return []Check{
		env.tool("git", "git", "install git"),
		env.tool("docker", "docker", "install Docker from https://docs.docker.com/get-docker/"),
		env.dockerRunning(),
		env.composeVersion(),
		env.tool("gh", "gh", "install the GitHub CLI from https://cli.github.com"),
		env.ghLoggedIn(),
		env.stateWritable(),
		env.noOtherPit(),
		env.portsAvailable(),
		env.configuration(),
		env.strayProjects(),
	}
}

// tool checks that a program exists and reports its version.
func (env Environment) tool(name, bin, fix string) Check {
	return func(ctx context.Context) Finding {
		out, err := env.Runner.Output(ctx, proc.Command{Name: bin, Args: []string{"--version"}})
		if err != nil {
			return Finding{Name: name, Result: Fail, Detail: "not found on PATH", Fix: fix}
		}
		return Finding{Name: name, Result: OK, Detail: firstLine(string(out))}
	}
}

func (env Environment) dockerRunning() Check {
	return func(ctx context.Context) Finding {
		const name = "docker daemon"

		out, err := env.Runner.Output(ctx, proc.Command{
			Name: "docker", Args: []string{"info", "--format", "{{.ServerVersion}}"},
		})
		if err != nil {
			return Finding{
				Name: name, Result: Fail,
				Detail: "not reachable",
				Fix:    "start Docker Desktop, or the docker service",
			}
		}
		return Finding{Name: name, Result: OK, Detail: "server " + firstLine(string(out))}
	}
}

func (env Environment) composeVersion() Check {
	return func(ctx context.Context) Finding {
		const name = "docker compose"

		out, err := env.Runner.Output(ctx, proc.Command{
			Name: "docker", Args: []string{"compose", "version", "--short"},
		})
		if err != nil {
			return Finding{
				Name: name, Result: Fail,
				Detail: "not available",
				Fix:    "pit needs Compose v2, which ships with current Docker versions",
			}
		}

		// v1 was a separate Python program and spells its version
		// "1.x". It does not understand the flags pit relies on.
		version := strings.TrimPrefix(firstLine(string(out)), "v")
		if strings.HasPrefix(version, "1.") {
			return Finding{
				Name: name, Result: Fail,
				Detail: "version " + version + ", which is Compose v1",
				Fix:    "upgrade Docker; pit needs Compose v2 or newer",
			}
		}
		return Finding{Name: name, Result: OK, Detail: "version " + version}
	}
}

func (env Environment) ghLoggedIn() Check {
	return func(ctx context.Context) Finding {
		const name = "gh account"

		if _, err := env.Runner.Output(ctx, proc.Command{Name: "gh", Args: []string{"auth", "status"}}); err != nil {
			return Finding{
				Name: name, Result: Fail,
				Detail: "not logged in",
				Fix:    "run `gh auth login`",
			}
		}
		return Finding{Name: name, Result: OK, Detail: "logged in"}
	}
}

func (env Environment) stateWritable() Check {
	return func(context.Context) Finding {
		const name = "state directory"

		if env.Store == nil {
			return Finding{
				Name: name, Result: Fail,
				Detail: env.StateDir + " is not usable",
				Fix:    "check that the directory exists and is writable",
			}
		}
		return Finding{Name: name, Result: OK, Detail: env.StateDir}
	}
}

func (env Environment) noOtherPit() Check {
	return func(context.Context) Finding {
		const name = "no other pit running"

		if env.Store == nil {
			return Finding{Name: name, Result: Warn, Detail: "could not tell"}
		}
		release, free := env.Store.TryLock()
		if !free {
			return Finding{
				Name: name, Result: Warn,
				Detail: "another pit holds the lock",
				Fix:    "wait for it to finish, or look for a stuck process",
			}
		}
		release()
		return Finding{Name: name, Result: OK, Detail: "the lock is free"}
	}
}

// portsAvailable samples the range rather than testing all ten thousand
// ports, which would take long enough to be annoying and say nothing
// more.
func (env Environment) portsAvailable() Check {
	return func(ctx context.Context) Finding {
		const (
			name    = "port range"
			samples = 40
		)

		free := 0
		for i := range samples {
			if !ports.Taken(ctx, ports.Min+i*(ports.Span/samples)) {
				free++
			}
		}

		detail := fmt.Sprintf("%d-%d, %d of %d sampled ports free", ports.Min, ports.Max, free, samples)
		switch {
		case free == 0:
			return Finding{
				Name: name, Result: Fail, Detail: detail,
				Fix: "something is using the whole range; `pit down --all` frees pit's own",
			}
		case free < samples/2:
			return Finding{
				Name: name, Result: Warn, Detail: detail,
				Fix: "more than half the range is in use; `pit ls` shows pit's own sandboxes",
			}
		default:
			return Finding{Name: name, Result: OK, Detail: detail}
		}
	}
}

func (env Environment) configuration() Check {
	return func(context.Context) Finding {
		const name = ".pit.yaml"

		path, err := config.Find(env.WorkDir)
		if err != nil {
			// Not being in a configured repository is fine: doctor is
			// also run to find out why something else is wrong.
			return Finding{
				Name: name, Result: Warn,
				Detail: "none found from " + env.WorkDir,
				Fix:    "run `pit init` in a repository to create one",
			}
		}
		if _, err := config.Load(path); err != nil {
			return Finding{
				Name: name, Result: Fail,
				Detail: firstLine(err.Error()),
				Fix:    "run `pit ls` in that repository for the full report",
			}
		}
		return Finding{Name: name, Result: OK, Detail: path}
	}
}

// strayProjects finds containers pit created that no record knows
// about. A second Ctrl+C during cleanup leaves exactly this, and
// nothing else would ever mention them again.
func (env Environment) strayProjects() Check {
	return func(ctx context.Context) Finding {
		const name = "stray containers"

		out, err := env.Runner.Output(ctx, proc.Command{
			Name: "docker",
			Args: []string{
				"ps", "--all",
				"--filter", "name=" + runtime.ProjectPrefix,
				"--format", `{{.Label "com.docker.compose.project"}}`,
			},
		})
		if err != nil {
			return Finding{Name: name, Result: Warn, Detail: "could not ask Docker"}
		}

		// TryLoad, not List: List waits for the lock, and the check
		// just before this one may have found it held by another pit.
		// A diagnosis that blocks on the problem it is diagnosing is
		// worse than one that says it could not look.
		known := map[string]bool{}
		if env.Store != nil {
			recorded, ok := env.Store.TryLoad()
			if !ok {
				return Finding{
					Name: name, Result: Warn,
					Detail: "could not read the record; another pit may be running",
					Fix:    "try again once it has finished",
				}
			}
			for _, box := range recorded.Sandboxes {
				known[box.Project] = true
			}
		}

		var stray []string
		seen := map[string]bool{}
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			project := strings.TrimSpace(line)
			if project == "" || seen[project] || !runtime.IsPitProject(project) || known[project] {
				continue
			}
			seen[project] = true
			stray = append(stray, project)
		}

		if len(stray) == 0 {
			return Finding{Name: name, Result: OK, Detail: "none"}
		}
		return Finding{
			Name: name, Result: Warn,
			Detail: fmt.Sprintf("%s pit does not know about: %s",
				plural(len(stray), "project", "projects"), strings.Join(stray, ", ")),
			Fix: "remove with `docker compose -p <project> down -v`",
		}
	}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}
