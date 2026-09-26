package sandbox

import (
	"context"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/thannoz/pit/internal/config"
	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/hooks"
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

// processEnv is what the commands of a sandbox of processes are given:
// what its processes are, with the web process's port. files are the
// Procfile and the plan.
func processEnv(files []string) []string {
	if len(files) < 2 {
		return nil
	}
	procs, err := local.ReadProcfile(files[0])
	if err != nil {
		return nil
	}
	plan, err := local.ReadPlan(files[1])
	if err != nil {
		return nil
	}
	return plan.Environment(procs, "")
}

// planFor is what pit decides for a sandbox's processes. {port} and
// {project} in env.set are the sandbox's own: a database of its own is
// "postgres://localhost/shop_{project}".
func planFor(c *config.Config, project string, port int) local.Plan {
	r := strings.NewReplacer("{port}", strconv.Itoa(port), "{project}", project)
	env := map[string]string{}
	for k, v := range c.Env.Set {
		env[k] = r.Replace(v)
	}
	return local.Plan{Project: project, Web: c.Web.Service, Port: port, Env: env}
}

// setUpProcesses writes the plan and runs the setup commands.
func (m *Manager) setUpProcesses(ctx context.Context, c *config.Config, files []string, project string, port int, dir string, rep Reporter) (string, error) {
	if err := local.WritePlan(files[1], planFor(c, project, port)); err != nil {
		return "", err
	}
	setup := hooks.List{Path: "processes.setup", Lines: c.Processes.Setup}
	if setup.Empty() {
		return "nothing to set up", nil
	}
	h := hooks.Sandbox{Project: project, Files: files, Dir: dir, Env: processEnv(files)}
	if err := hooks.Run(ctx, m.Proc, setup, h, rep.Stdout(), rep.Stderr()); err != nil {
		return "", err
	}
	return plural(len(setup.Lines), "command", "commands"), nil
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

// confirmProcesses asks before running processes the reviewer's
// checkout does not have.
func (m *Manager) confirmProcesses(req UpRequest, mine *config.Config, worktree string, rep Reporter) error {
	fresh, err := newProcesses(req.Repo.Root, worktree, mine, req.Config)
	if err != nil || len(fresh) == 0 {
		return err
	}
	rep.Note("#%d runs %s on this machine that your checkout does not:", req.PR.Number, plural(len(fresh), "process", "processes"))
	for _, p := range fresh {
		rep.Note("    %s", p)
	}
	if req.Confirm == nil {
		return errs.New("#%d wants to run %s on this machine", req.PR.Number, plural(len(fresh), "process", "processes")).
			WithHint("run pit yourself and answer the question")
	}
	if !req.Confirm("Run " + pick(len(fresh), "it", "them") + "?") {
		return errs.New("stopped before running #%d's processes", req.PR.Number).
			WithHint("nothing was started; run pit from a terminal to answer")
	}
	return nil
}
