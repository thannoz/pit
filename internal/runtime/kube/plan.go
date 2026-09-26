// Package kube runs a sandbox in Kubernetes: the project's own
// manifests, applied into a namespace of its own in pit's own local
// cluster.
//
// The cluster is pit's, made with kind or k3d and addressed through a
// kubeconfig of pit's own, so that a pull request's manifests never
// reach a cluster the reviewer's kubectl is pointed at -- the one a
// team deploys to is never more than a context switch away.
package kube

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"

	"github.com/thannoz/pit/internal/errs"
)

// ClusterName is the cluster pit makes, and the only one it uses.
const ClusterName = "pit"

// Plan is what pit decides for a sandbox in Kubernetes, the way an
// override does for a compose project. It is written beside the
// worktree, and the runner reads everything it needs from it.
type Plan struct {
	// Project names the sandbox, and Namespace is where it runs: the
	// same name, which Kubernetes accepts as it is.
	Project   string `json:"project"`
	Namespace string `json:"namespace"`
	// Tool made the cluster: kind or k3d.
	Tool string `json:"tool"`
	// Kubeconfig is pit's own, and Context the cluster's in it.
	Kubeconfig string `json:"kubeconfig"`
	Context    string `json:"context"`
	// Kustomization is the directory pit renders the manifests from:
	// the project's, in the namespace, with pit's images.
	Kustomization string `json:"kustomization"`
	// Web is the workload a reviewer opens, WebPort the port its
	// container listens on, and Port the one on this machine.
	Web     string `json:"web"`
	WebPort int    `json:"web_port"`
	Port    int    `json:"port"`
	// Images are the images pit builds, by the name the manifests give
	// them.
	Images []Image `json:"images,omitempty"`
}

// Image is one image pit builds for a sandbox.
type Image struct {
	// Name is how the manifests refer to it: shop-web, ghcr.io/acme/web.
	Name string `json:"name"`
	// Tag is what pit builds it as and loads into the cluster.
	Tag string `json:"tag"`
	// Context and Dockerfile are where it is built from; Dockerfile is
	// relative to Context, and empty for its Dockerfile.
	Context    string `json:"context"`
	Dockerfile string `json:"dockerfile,omitempty"`
}

// Flags point kubectl at the sandbox: pit's kubeconfig, its cluster,
// the sandbox's namespace.
func (p Plan) Flags() []string {
	return append(p.ClusterFlags(), "--namespace", p.Namespace)
}

// ClusterFlags point kubectl at pit's cluster. The cache is pit's too:
// kubectl keeps one in ~/.kube otherwise, whichever cluster it asks.
func (p Plan) ClusterFlags() []string {
	return []string{"--kubeconfig", p.Kubeconfig, "--context", p.Context,
		"--cache-dir", filepath.Join(filepath.Dir(p.Kubeconfig), "cache")}
}

// Environment is what a command run for the sandbox is given, so that
// a kubectl of its own finds the sandbox too.
func (p Plan) Environment() []string {
	return []string{
		"KUBECONFIG=" + p.Kubeconfig,
		"KUBECACHEDIR=" + filepath.Join(filepath.Dir(p.Kubeconfig), "cache"),
		"PIT_KUBE_CONTEXT=" + p.Context,
		"PIT_NAMESPACE=" + p.Namespace,
		"PIT_PROJECT=" + p.Project,
	}
}

// WritePlan writes a plan to path.
func WritePlan(path string, p Plan) error {
	sort.Slice(p.Images, func(i, j int) bool { return p.Images[i].Name < p.Images[j].Name })
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
