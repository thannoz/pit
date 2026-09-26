package kube

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	goruntime "runtime"
	"slices"
	"strings"

	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/proc"
)

// The tools that make pit's cluster.
const (
	Kind = "kind"
	K3d  = "k3d"
)

// Runner runs kind, k3d, kubectl and docker. internal/proc.Exec is one.
type Runner interface {
	Output(ctx context.Context, c proc.Command) ([]byte, error)
	Stream(ctx context.Context, c proc.Command, stdout, stderr io.Writer) error
}

// Cluster is pit's cluster, made by Tool, with its kubeconfig in Dir.
type Cluster struct {
	Tool   string
	Dir    string
	Runner Runner
}

// Kubeconfig is pit's own, one per tool: never the reviewer's, whose
// current context may be the cluster a team deploys to.
func (c Cluster) Kubeconfig() string { return filepath.Join(c.Dir, "config-"+c.Tool) }

// Context is the cluster's context in it.
func (c Cluster) Context() string { return c.Tool + "-" + ClusterName }

// goos is the system pit runs on; a test pretends another.
var goos = goruntime.GOOS

// Choose finds the tool for pit's cluster: the one that made it, if
// one did, or the first of kind and k3d that is installed.
func Choose(ctx context.Context, r Runner, dir string) (Cluster, error) {
	if goos == "windows" {
		// The forward to the web workload runs under pit's process
		// supervisor, which needs process groups and signals.
		return Cluster{}, errs.New("pit runs Kubernetes projects on macOS and Linux only")
	}
	var installed []string
	for _, tool := range []string{Kind, K3d} {
		c := Cluster{Tool: tool, Dir: dir, Runner: r}
		exists, err := c.Exists(ctx)
		if err != nil {
			continue
		}
		if exists {
			return c, nil
		}
		installed = append(installed, tool)
	}
	if len(installed) == 0 {
		return Cluster{}, errs.New("pit runs Kubernetes projects in a local cluster of kind or k3d, and neither is installed").
			WithHint("install kind (https://kind.sigs.k8s.io) or k3d (https://k3d.io), and kubectl")
	}
	return Cluster{Tool: installed[0], Dir: dir, Runner: r}, nil
}

// Exists asks the tool whether pit's cluster is there. An error says
// the tool itself is not.
func (c Cluster) Exists(ctx context.Context) (bool, error) {
	switch c.Tool {
	case Kind:
		out, err := c.Runner.Output(ctx, proc.Command{Name: "kind", Args: []string{"get", "clusters"}})
		if err != nil {
			return false, err
		}
		return slices.Contains(strings.Fields(string(out)), ClusterName), nil
	case K3d:
		out, err := c.Runner.Output(ctx, proc.Command{Name: "k3d", Args: []string{"cluster", "list", "--output", "json"}})
		if err != nil {
			return false, err
		}
		var clusters []struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(out, &clusters); err != nil {
			return false, errs.Wrap(err, "cannot read what k3d says about its clusters")
		}
		return slices.ContainsFunc(clusters, func(k struct {
			Name string `json:"name"`
		}) bool {
			return k.Name == ClusterName
		}), nil
	}
	return false, errs.New("pit does not know how to make a cluster with %q", c.Tool)
}

// Ensure makes the cluster if it is not there, and pit's kubeconfig for
// it if that is not. It says whether it made the cluster.
func (c Cluster) Ensure(ctx context.Context, stdout, stderr io.Writer) (bool, error) {
	exists, err := c.Exists(ctx)
	if err != nil {
		return false, errs.Wrap(err, "cannot ask %s about pit's cluster", c.Tool).
			WithHint("check that %s is installed and Docker is running", c.Tool)
	}
	if err := os.MkdirAll(c.Dir, 0o700); err != nil {
		return false, errs.Wrap(err, "cannot create %s", c.Dir)
	}
	kubeconfig := c.Kubeconfig()
	if !exists {
		var create proc.Command
		switch c.Tool {
		case Kind:
			create = proc.Command{Name: "kind", Args: []string{"create", "cluster", "--name", ClusterName, "--kubeconfig", kubeconfig, "--wait", "120s"}}
		case K3d:
			create = proc.Command{Name: "k3d", Args: []string{"cluster", "create", ClusterName,
				"--kubeconfig-update-default=false", "--kubeconfig-switch-context=false", "--wait"}}
		}
		if err := c.Runner.Stream(ctx, create, stdout, stderr); err != nil {
			return false, errs.Wrap(err, "cannot make pit's cluster with %s", c.Tool).
				WithHint("the output above says why; `%s` reproduces it", create.String())
		}
	}
	if _, err := os.Stat(kubeconfig); err == nil && exists {
		return false, nil
	}
	// A cluster made before, or by k3d, which writes it nowhere of
	// pit's on its own.
	var write proc.Command
	switch c.Tool {
	case Kind:
		write = proc.Command{Name: "kind", Args: []string{"export", "kubeconfig", "--name", ClusterName, "--kubeconfig", kubeconfig}}
	case K3d:
		write = proc.Command{Name: "k3d", Args: []string{"kubeconfig", "write", ClusterName, "--output", kubeconfig, "--overwrite"}}
	}
	if _, err := c.Runner.Output(ctx, write); err != nil {
		return false, errs.Wrap(err, "cannot write pit's kubeconfig for its cluster")
	}
	return !exists, nil
}

// Load puts an image of this machine's Docker into the cluster, whose
// nodes pull from nowhere else for it.
func (c Cluster) Load(ctx context.Context, image string, stdout, stderr io.Writer) error {
	var load proc.Command
	switch c.Tool {
	case Kind:
		load = proc.Command{Name: "kind", Args: []string{"load", "docker-image", image, "--name", ClusterName}}
	case K3d:
		load = proc.Command{Name: "k3d", Args: []string{"image", "import", image, "--cluster", ClusterName}}
	default:
		return errs.New("pit does not know how to load an image with %q", c.Tool)
	}
	if err := c.Runner.Stream(ctx, load, stdout, stderr); err != nil {
		return errs.Wrap(err, "cannot load %s into pit's cluster", image)
	}
	return nil
}
