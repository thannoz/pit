package cli

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thannoz/pit/internal/proc"
	"github.com/thannoz/pit/internal/runtime/kube"
	"github.com/thannoz/pit/internal/state"
)

// kubectlStub answers kubectl kustomize with the manifests and anything
// else with its output, and notes what it was asked.
type kubectlStub struct {
	mu        sync.Mutex
	manifests string
	out       string
	asked     []string
}

func (k *kubectlStub) Output(_ context.Context, c proc.Command) ([]byte, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.asked = append(k.asked, strings.Join(c.Args, " "))
	if len(c.Args) > 0 && c.Args[0] == "kustomize" {
		return []byte(k.manifests), nil
	}
	return []byte(k.out), nil
}

func (k *kubectlStub) Stream(ctx context.Context, c proc.Command, stdout, _ io.Writer) error {
	out, err := k.Output(ctx, c)
	_, _ = stdout.Write(out)
	return err
}

func withKubectl(t *testing.T, k *kubectlStub) {
	t.Helper()
	previous := kubeRunner
	kubeRunner = k
	t.Cleanup(func() { kubeRunner = previous })
}

// kubeBox is a recorded sandbox in Kubernetes, with its plan.
func kubeBox(t *testing.T) state.Sandbox {
	t.Helper()
	id := atRepo(t, "github.com", "acme", "shop")
	box := recorded(7, id.String(), id.Ref(), "feature", time.Minute)
	box.Kubernetes, box.WebService, box.Worktree = true, "web", t.TempDir()
	dir := t.TempDir()
	plan := filepath.Join(dir, "pr-7.kube.json")
	if err := kube.WritePlan(plan, kube.Plan{Project: box.Project, Namespace: box.Project, Tool: kube.Kind,
		Kubeconfig: filepath.Join(dir, "kube", "config-kind"), Context: "kind-pit", Kustomization: filepath.Join(dir, "pr-7.kube"),
		Web: "web", WebPort: 80, Port: 40007}); err != nil {
		t.Fatal(err)
	}
	box.ComposeFiles = []string{plan}
	return box
}

const kubeManifests = "apiVersion: apps/v1\nkind: Deployment\nmetadata: {name: web}\n---\napiVersion: apps/v1\nkind: StatefulSet\nmetadata: {name: db}\n"

func TestLogsInKubernetes(t *testing.T) {
	box := kubeBox(t)
	_, fake := withManager(t, box)
	k := &kubectlStub{manifests: kubeManifests, out: "GET / 200\n"}
	withKubectl(t, k)

	out, err := runCLI(t, "logs", "7", "--tail", "5")
	if err != nil || !strings.Contains(out, "GET / 200") {
		t.Fatalf("%q, %v", out, err)
	}
	last := k.asked[len(k.asked)-1]
	if !strings.Contains(last, "--context kind-pit") || !strings.Contains(last, "--namespace "+box.Project) || !strings.HasSuffix(last, "logs deployment/web --tail 5") {
		t.Errorf("asked %q", last)
	}
	if _, err := runCLI(t, "logs", "7", "db", "-f"); err != nil {
		t.Fatal(err)
	}
	if last := k.asked[len(k.asked)-1]; !strings.HasSuffix(last, "logs statefulset/db --tail 200 --follow") {
		t.Errorf("asked %q", last)
	}
	if _, err := runCLI(t, "logs", "7", "mailer"); err == nil || !strings.Contains(err.Error(), "no workload mailer") {
		t.Errorf("err = %v", err)
	}
	for _, c := range fake.Calls() {
		if c.Method == "Logs" {
			t.Errorf("went through compose: %+v", c)
		}
	}
}

func TestShellInKubernetesNamesTheWorkload(t *testing.T) {
	box := kubeBox(t)
	withManager(t, box)
	withKubectl(t, &kubectlStub{manifests: kubeManifests})
	// A workload that is not there is said before kubectl is started.
	if _, err := runCLI(t, "shell", "7", "mailer"); err == nil || !strings.Contains(err.Error(), "no workload mailer") {
		t.Errorf("err = %v", err)
	}
}
