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
	// Getenv reads the environment the checks run in. It is a field so
	// that a test can describe a machine without becoming one.
	Getenv func(string) string
	// FindBrowser looks for the browser pit inspect drives; nil leaves
	// the check out.
	FindBrowser func() (string, error)
	// GitHubLogin is pit's own login to github.com: whose it is, and
	// whether there is one. nil has none.
	GitHubLogin func() (user string, ok bool)
}

// Default is the list of checks pit doctor runs.
func Default(env Environment) []Check {
	return []Check{
		env.tool("git", "git", "install git"),
		env.tool("docker", "docker", "install Docker from https://docs.docker.com/get-docker/"),
		env.dockerRunning(),
		env.composeVersion(),
		env.gh(),
		env.githubAccount(),
		env.stateWritable(),
		env.noOtherPit(),
		env.portsAvailable(),
		env.buildCache(),
		env.configuration(),
		env.kubernetes(),
		env.devShell(),
		env.strayProjects(),
		env.browser(),
	}
}

// browser says whether pit inspect has a browser to drive. Only a
// warning: everything else pit does works without one.
func (env Environment) browser() Check {
	const name = "browser"
	return func(context.Context) Finding {
		if env.FindBrowser == nil {
			return Finding{Name: name, Result: OK, Detail: "not checked"}
		}
		path, err := env.FindBrowser()
		if err != nil {
			return Finding{Name: name, Result: Warn, Detail: "no Chrome or Chromium found, so pit inspect cannot load pages",
				Fix: "install Chrome, or set PIT_BROWSER to a Chrome or Chromium executable"}
		}
		return Finding{Name: name, Result: OK, Detail: path}
	}
}

// buildCache warns when the one setting that makes pit slow is set.
//
// pit checks every pull request out into a worktree of its own, so
// every build sees files that were written a moment ago. BuildKit keys
// its cache on what is in a file and does not care when it was
// written, which is why a second sandbox of the same repository
// reuses the first one's layers. The builder Docker used before it
// keys COPY on timestamps, and so rebuilds everything, every time, for
// every pull request.
//
// Measured on a project with a twelve-second dependency layer: one
// second with BuildKit, fourteen without.
func (env Environment) buildCache() Check {
	return func(context.Context) Finding {
		const name = "build cache"

		if env.Getenv != nil && env.Getenv("DOCKER_BUILDKIT") == "0" {
			return Finding{
				Name:   name,
				Result: Warn,
				Detail: "DOCKER_BUILDKIT=0 makes every sandbox rebuild from nothing, because each one is a fresh worktree",
				Fix:    "unset DOCKER_BUILDKIT, or set it to 1",
			}
		}
		return Finding{Name: name, Result: OK, Detail: "shared between sandboxes of the same repository"}
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

// ownGitHub says how pit reaches github.com without gh: a token in the
// environment, or a login of its own. "" is neither.
func (env Environment) ownGitHub() string {
	if env.Getenv != nil {
		for _, name := range []string{"GH_TOKEN", "GITHUB_TOKEN"} {
			if env.Getenv(name) != "" {
				return name + " from the environment"
			}
		}
	}
	if env.GitHubLogin != nil {
		if user, ok := env.GitHubLogin(); ok {
			if user == "" {
				return "pit's own login"
			}
			return "pit's own login, as " + user
		}
	}
	return ""
}

// gh is needed only where pit has no way of its own to GitHub.
func (env Environment) gh() Check {
	return func(ctx context.Context) Finding {
		const name = "gh"
		out, err := env.Runner.Output(ctx, proc.Command{Name: "gh", Args: []string{"--version"}})
		switch {
		case err == nil:
			return Finding{Name: name, Result: OK, Detail: firstLine(string(out))}
		case env.ownGitHub() != "":
			return Finding{Name: name, Result: OK, Detail: "not found on PATH, and not needed: pit uses " + env.ownGitHub()}
		}
		return Finding{Name: name, Result: Fail, Detail: "not found on PATH",
			Fix: "run `pit auth login`, or install the GitHub CLI from https://cli.github.com"}
	}
}

func (env Environment) githubAccount() Check {
	return func(ctx context.Context) Finding {
		const name = "GitHub account"
		if own := env.ownGitHub(); own != "" {
			return Finding{Name: name, Result: OK, Detail: own}
		}
		if _, err := env.Runner.Output(ctx, proc.Command{Name: "gh", Args: []string{"auth", "status"}}); err != nil {
			return Finding{
				Name: name, Result: Fail,
				Detail: "neither pit nor gh is logged in",
				Fix:    "run `pit auth login` (or `gh auth login`)",
			}
		}
		return Finding{Name: name, Result: OK, Detail: "gh is logged in"}
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
			// The whole report, not its first line: doctor exists to
			// say what is wrong, and an earlier version pointed at
			// `pit ls` for the detail -- which does not read the
			// configuration at all.
			return Finding{Name: name, Result: Fail, Detail: err.Error()}
		}
		return Finding{Name: name, Result: OK, Detail: path}
	}
}

// kubernetes checks the tools a project in Kubernetes needs: kubectl,
// and kind or k3d for pit's cluster. Anywhere else there is nothing to
// check.
func (env Environment) kubernetes() Check {
	return func(ctx context.Context) Finding {
		const name = "kubernetes"
		path, err := config.Find(env.WorkDir)
		if err != nil {
			return Finding{Name: name, Result: OK, Detail: "not used here"}
		}
		c, err := config.Load(path)
		if err != nil || !c.Kubernetes.On() {
			return Finding{Name: name, Result: OK, Detail: "not used here"}
		}
		if _, err := env.Runner.Output(ctx, proc.Command{Name: "kubectl", Args: []string{"version", "--client"}}); err != nil {
			return Finding{Name: name, Result: Fail, Detail: "kubectl not found on PATH",
				Fix: "install kubectl: https://kubernetes.io/docs/tasks/tools/"}
		}
		for _, tool := range []string{"kind", "k3d"} {
			if _, err := env.Runner.Output(ctx, proc.Command{Name: tool, Args: []string{"version"}}); err == nil {
				return Finding{Name: name, Result: OK, Detail: "kubectl, and " + tool + " for pit's cluster"}
			}
		}
		return Finding{Name: name, Result: Fail, Detail: "neither kind nor k3d found on PATH",
			Fix: "install kind (https://kind.sigs.k8s.io) or k3d (https://k3d.io); pit makes its own cluster with one"}
	}
}

// devShell checks the tool whose dev shell a project's processes run
// in, nix or devenv, where they run in one.
func (env Environment) devShell() Check {
	return func(ctx context.Context) Finding {
		const name = "dev shell"
		path, err := config.Find(env.WorkDir)
		if err != nil {
			return Finding{Name: name, Result: OK, Detail: "not used here"}
		}
		c, err := config.Load(path)
		if err != nil || c.Processes.Environment == "" {
			return Finding{Name: name, Result: OK, Detail: "not used here"}
		}
		tool, fix := "devenv", "install devenv: https://devenv.sh/getting-started/"
		if _, ok := c.Processes.Nix(); ok {
			tool, fix = "nix", "install Nix: https://nixos.org/download/"
		}
		out, err := env.Runner.Output(ctx, proc.Command{Name: tool, Args: []string{"--version"}})
		if err != nil {
			return Finding{Name: name, Result: Fail, Detail: tool + " not found on PATH", Fix: fix}
		}
		return Finding{Name: name, Result: OK, Detail: strings.TrimSpace(string(out))}
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
