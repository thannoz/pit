package sandbox

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/thannoz/pit/internal/config"
	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/hooks"
	"github.com/thannoz/pit/internal/proc"
	"github.com/thannoz/pit/internal/runtime/local"
)

// PlanPathIn is where the plan for a sandbox of processes lives, beside
// the worktree like a compose override.
func PlanPathIn(repoDir string, pr int, slot string) string {
	name := "pr-" + strconv.Itoa(pr)
	if slot != "" {
		name += "-" + slot
	}
	return filepath.Join(repoDir, name+".processes.json")
}

// processTarget is what the commands of a sandbox of processes are
// given: what its processes are, with the web process's port, and what
// they run in. files are the Procfile and the plan.
func processTarget(files []string) (env, wrap []string) {
	if len(files) < 2 {
		return nil, nil
	}
	procs, err := local.ReadProcfile(files[0])
	if err != nil {
		return nil, nil
	}
	plan, err := local.ReadPlan(files[1])
	if err != nil {
		return nil, nil
	}
	return plan.Environment(procs, ""), plan.Wrap
}

// wrapFor is what the processes and commands of a sandbox run in: the
// dev shell of the pull request's flake or devenv, from its worktree,
// so that a pull request that changes the tools brings them along.
func wrapFor(p config.Processes, worktree string) []string {
	if shell, ok := p.Nix(); ok {
		ref := worktree
		if shell != "" {
			ref += "#" + shell
		}
		// Flakes are still experimental to Nix itself; a lock file
		// the flake lacks is made in memory, not in the checkout.
		return []string{"nix", "--extra-experimental-features", "nix-command flakes",
			"develop", ref, "--no-write-lock-file", "--command"}
	}
	if p.Environment == config.DevenvEnvironment {
		// Without the --, devenv takes the command's own flags.
		return []string{"devenv", "shell", "--"}
	}
	return nil
}

// planFor is what pit decides for a sandbox's processes. {port} and
// {project} in env.set are the sandbox's own: a database of its own is
// "postgres://localhost/shop_{project}".
func planFor(c *config.Config, project string, port int, worktree string) local.Plan {
	r := strings.NewReplacer("{port}", strconv.Itoa(port), "{project}", project)
	env := map[string]string{}
	for k, v := range c.Env.Set {
		env[k] = r.Replace(v)
	}
	return local.Plan{Project: project, Web: c.Web.Service, Port: port, Env: env, Wrap: wrapFor(c.Processes, worktree)}
}

// setUpProcesses writes the plan, enters the environment the processes
// run in, and runs the setup commands.
func (m *Manager) setUpProcesses(ctx context.Context, c *config.Config, files []string, project string, port int, dir string, rep Reporter) (string, error) {
	if err := local.WritePlan(files[1], planFor(c, project, port, dir)); err != nil {
		return "", err
	}
	h := hooks.Sandbox{Project: project, Files: files, Dir: dir}
	h.Env, h.Wrap = processTarget(files)
	var said []string
	if len(h.Wrap) > 0 {
		// Once before anything else: the first time fetches and builds
		// the tools, which the web process's wait for readiness is not
		// meant to cover, and a flake that does not evaluate is said
		// here rather than as a process that ended.
		enter := proc.Command{Name: h.Wrap[0], Args: append(slices.Clone(h.Wrap[1:]), "true"), Dir: dir, Env: h.Env}
		if err := m.Proc.Stream(ctx, enter, rep.Stdout(), rep.Stderr()); err != nil {
			return "", errs.Wrap(err, "cannot enter the pull request's %s environment", c.Processes.Environment).
				WithHint("the output above says why; `%s` in %s reproduces it", enter.String(), dir)
		}
		said = append(said, "the "+c.Processes.Environment+" environment")
	}
	setup := hooks.List{Path: "processes.setup", Lines: c.Processes.Setup}
	if !setup.Empty() {
		if err := hooks.Run(ctx, m.Proc, setup, h, rep.Stdout(), rep.Stderr()); err != nil {
			return "", err
		}
		said = append(said, plural(len(setup.Lines), "command", "commands"))
	}
	if len(said) == 0 {
		return "nothing to set up", nil
	}
	return strings.Join(said, " and "), nil
}

// newProcesses are the Procfile's commands in the pull request that the
// reviewer's own Procfile does not have. They run on this machine, as
// the commands of .pit.yaml without compose do, and a pull request that
// changes them is asking for more than a review.
func newProcesses(root, worktree string, mine, theirs *config.Config) ([]string, error) {
	procs, err := local.ReadProcfile(filepath.Join(worktree, theirs.Processes.File))
	if err != nil {
		return nil, errs.Wrap(err, "the pull request's Procfile cannot be read")
	}
	var known []string
	if mine.Processes.On() {
		if own, err := local.ReadProcfile(filepath.Join(root, mine.Processes.File)); err == nil {
			for _, p := range own {
				known = append(known, p.Command)
			}
		}
	}
	var fresh []string
	for _, p := range procs {
		if !slices.Contains(known, p.Command) {
			fresh = append(fresh, p.Name+": "+p.Command)
		}
	}
	return fresh, nil
}

// environmentFiles are what makes a flake's or devenv's dev shell. Its
// shell hook runs on this machine before every process and command.
var environmentFiles = []string{"*.nix", "flake.lock", "devenv.yaml", "devenv.lock"}

// newEnvironment are the files of the dev shell the processes run in
// that differ in the pull request from the reviewer's checkout.
func (m *Manager) newEnvironment(ctx context.Context, root, worktree string, p config.Processes) ([]string, error) {
	if p.Environment == "" {
		return nil, nil
	}
	out, err := m.Git.Output(ctx, proc.Command{Name: "git", Args: append([]string{"ls-files", "-z", "--"}, environmentFiles...), Dir: worktree})
	if err != nil {
		return nil, errs.Wrap(err, "cannot list the pull request's %s files", p.Environment)
	}
	var changed []string
	for _, f := range strings.Split(string(out), "\x00") {
		if f == "" {
			continue
		}
		theirs, err := os.ReadFile(filepath.Join(worktree, f))
		if err != nil {
			return nil, errs.Wrap(err, "the pull request's %s cannot be read", f)
		}
		if own, err := os.ReadFile(filepath.Join(root, f)); err != nil || !bytes.Equal(own, theirs) {
			changed = append(changed, f)
		}
	}
	return changed, nil
}

// confirmProcesses asks before running processes the reviewer's
// checkout does not have, or running them in a dev shell it does not
// have.
func (m *Manager) confirmProcesses(ctx context.Context, req UpRequest, mine *config.Config, worktree string, rep Reporter) error {
	fresh, err := newProcesses(req.Repo.Root, worktree, mine, req.Config)
	if err != nil {
		return err
	}
	shell, err := m.newEnvironment(ctx, req.Repo.Root, worktree, req.Config.Processes)
	if err != nil || len(fresh)+len(shell) == 0 {
		return err
	}
	if len(fresh) > 0 {
		rep.Note("#%d runs %s on this machine that your checkout does not:", req.PR.Number, plural(len(fresh), "process", "processes"))
		for _, p := range fresh {
			rep.Note("    %s", p)
		}
	}
	if len(shell) > 0 {
		rep.Note("#%d runs its processes in a %s environment your checkout does not have, which runs code of its own:", req.PR.Number, req.Config.Processes.Environment)
		for _, f := range shell {
			rep.Note("    %s", f)
		}
	}
	what := plural(len(fresh), "process", "processes")
	if len(fresh) == 0 {
		what = "its processes in a " + req.Config.Processes.Environment + " environment of its own"
	}
	if req.Confirm == nil {
		return errs.New("#%d wants to run %s on this machine", req.PR.Number, what).
			WithHint("run pit yourself and answer the question")
	}
	if !req.Confirm("Run " + pick(len(fresh), "it", "them") + "?") {
		return errs.New("stopped before running #%d's processes", req.PR.Number).
			WithHint("nothing was started; run pit from a terminal to answer")
	}
	return nil
}
