package local

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/thannoz/pit/internal/errs"
)

// Plan is what pit decides for a sandbox's processes, the way an
// override decides it for a compose project: which process a reviewer
// opens, its port, and the environment all of them run with. It is
// written beside the worktree, and the runner reads it with the
// Procfile.
type Plan struct {
	// Project names the sandbox. The processes see it as PIT_PROJECT,
	// which is what a database of their own can be named after.
	Project string `json:"project"`
	// Web is the process a reviewer opens, and Port the port it is
	// given as PORT.
	Web  string `json:"web"`
	Port int    `json:"port"`
	// Env is set for every process, over this machine's environment.
	Env map[string]string `json:"env,omitempty"`
}

// PortStep is how far apart the ports of a sandbox's processes are:
// foreman's spacing, so that the web process has the sandbox's port and
// the others, which rarely listen at all, have one far from it.
const PortStep = 100

// PortOf is the port a process is given: the sandbox's for the web
// process, one PortStep further for each other in the Procfile's order.
func (p Plan) PortOf(procs []Process, name string) int {
	if name == p.Web {
		return p.Port
	}
	k := 0
	for _, pr := range procs {
		if pr.Name == p.Web {
			continue
		}
		k++
		if pr.Name == name {
			return p.Port + k*PortStep
		}
	}
	return 0
}

// Environment is what a process, or a command run for the sandbox, is
// given on top of this machine's environment. An empty name is a
// command's: it sees the web process's port.
func (p Plan) Environment(procs []Process, name string) []string {
	port := p.Port
	if name != "" {
		port = p.PortOf(procs, name)
	}
	keys := make([]string, 0, len(p.Env))
	for k := range p.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys)+3)
	for _, k := range keys {
		out = append(out, k+"="+p.Env[k])
	}
	return append(out,
		"PORT="+strconv.Itoa(port),
		"PIT_PORT="+strconv.Itoa(p.Port),
		"PIT_PROJECT="+p.Project,
	)
}

// WritePlan writes a plan to path.
func WritePlan(path string, p Plan) error {
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return errs.Wrap(err, "cannot create %s", filepath.Dir(path))
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		return errs.Wrap(err, "cannot write %s", path)
	}
	return nil
}

// ReadPlan reads the plan at path.
func ReadPlan(path string) (Plan, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Plan{}, errs.Wrap(err, "cannot read %s", path)
	}
	var p Plan
	if err := json.Unmarshal(data, &p); err != nil {
		return Plan{}, errs.Wrap(err, "cannot read %s", path)
	}
	return p, nil
}
