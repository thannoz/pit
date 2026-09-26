package sandbox

import (
	"context"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/thannoz/pit/internal/config"
	"github.com/thannoz/pit/internal/proc"
	"github.com/thannoz/pit/internal/runtime/kube"
)

// kubeRunner renders manifests for pit to read which workloads they
// have; a variable, so that a test renders none.
var kubeRunner kube.Runner = proc.Exec{}

// KubePlanPathIn is where the plan for a sandbox in Kubernetes lives,
// beside the worktree like a compose override.
func KubePlanPathIn(repoDir string, pr int, slot string) string {
	name := "pr-" + strconv.Itoa(pr)
	if slot != "" {
		name += "-" + slot
	}
	return filepath.Join(repoDir, name+".kube.json")
}

// kustomizationFor is the directory pit renders a sandbox's manifests
// from, beside its plan.
func kustomizationFor(plan string) string { return strings.TrimSuffix(plan, ".json") }

// kubeTarget is what a sandbox's commands are given: kubectl's flags,
// and an environment for a kubectl of their own. files are the plan.
func kubeTarget(files []string) (flags, env []string) {
	if len(files) < 1 {
		return nil, nil
	}
	p, err := kube.ReadPlan(files[0])
	if err != nil {
		return nil, nil
	}
	return p.Flags(), p.Environment()
}

// imageTag is what pit builds an image of the manifests as: named after
// the sandbox, tagged with the commit, and in a registry that does not
// exist, so that nothing ever pulls it from anywhere.
func imageTag(project, name, sha string) string {
	base := strings.ToLower(path.Base(strings.TrimSpace(name)))
	if i := strings.IndexAny(base, ":@"); i >= 0 {
		base = base[:i]
	}
	return "pit.local/" + project + "/" + base + ":" + short(sha)
}

// setUpKube writes the plan and the kustomization for a sandbox in
// Kubernetes, choosing the cluster's tool if the sandbox has none yet.
func (m *Manager) setUpKube(ctx context.Context, c *config.Config, planPath, project string, port int, worktree, sha string) (kube.Plan, error) {
	cluster, err := kube.Choose(ctx, kubeRunner, filepath.Join(m.StateDir, "kube"))
	if err != nil {
		return kube.Plan{}, err
	}
	var manifests []string
	for _, f := range c.Kubernetes.Manifests {
		manifests = append(manifests, inWorktree(worktree, f))
	}
	resources, err := kube.Resources(manifests)
	if err != nil {
		return kube.Plan{}, err
	}
	p := kube.Plan{
		Project: project, Namespace: project, Tool: cluster.Tool,
		Kubeconfig: cluster.Kubeconfig(), Context: cluster.Context(),
		Kustomization: kustomizationFor(planPath),
		Web:           c.Web.Service, WebPort: c.Web.Port, Port: port,
	}
	for _, im := range c.Kubernetes.Images {
		p.Images = append(p.Images, kube.Image{
			Name: im.Name, Tag: imageTag(project, im.Name, sha),
			Context: inWorktree(worktree, im.Context), Dockerfile: im.Dockerfile,
		})
	}
	if err := kube.WriteKustomization(p.Kustomization, p.Namespace, resources, p.Images); err != nil {
		return kube.Plan{}, err
	}
	return p, kube.WritePlan(planPath, p)
}
