package sandbox_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	goruntime "runtime"
	"slices"
	"strings"
	"testing"

	"github.com/thannoz/pit/internal/config"
	"github.com/thannoz/pit/internal/data/datatest"
	"github.com/thannoz/pit/internal/proc"
	"github.com/thannoz/pit/internal/runtime"
	"github.com/thannoz/pit/internal/runtime/kube"
	"github.com/thannoz/pit/internal/runtime/runtimetest"
	"github.com/thannoz/pit/internal/sandbox"
)

// kindThere answers as kind does with pit's cluster made, and notes
// what else it is asked.
type kindThere struct{ asked []string }

func (k *kindThere) Output(_ context.Context, c proc.Command) ([]byte, error) {
	k.asked = append(k.asked, c.String())
	if c.Name == "kind" {
		return []byte("pit\n"), nil
	}
	return nil, os.ErrNotExist
}

func (k *kindThere) Stream(ctx context.Context, c proc.Command, stdout, _ io.Writer) error {
	out, err := k.Output(ctx, c)
	_, _ = stdout.Write(out)
	return err
}

const kubeYAML = `kubernetes:
  manifests: [k8s]
  images:
    - name: shop-web
web:
  service: web
  port: 8080
data:
  migrate:
    - "sh -c 'echo $PIT_NAMESPACE $PIT_KUBE_CONTEXT > migrated.txt'"
  scenarios:
    - name: standard
      apply: ["sh -c 'echo $PIT_NAMESPACE > scenario.txt'"]
  default: standard
`

func kubeFixture(t *testing.T) (*sandbox.Manager, sandbox.UpRequest, *runtimetest.Fake, *kindThere) {
	t.Helper()
	if goruntime.GOOS == "windows" {
		t.Skip("skipping: pit runs Kubernetes projects on macOS and Linux only")
	}
	m, req, _ := upFixture(t)
	pushToPullRequest(t, req.Repo.Root, 7, map[string]string{
		"k8s/web.yaml": "apiVersion: apps/v1\nkind: Deployment\nmetadata: {name: web}\n",
		"Dockerfile":   "FROM nginx:alpine\n",
	})
	cfg, err := config.Parse([]byte(kubeYAML))
	if err != nil {
		t.Fatal(err)
	}
	req.Config = cfg
	cluster := runtimetest.New("web")
	m.Runtime = runtime.Either{Compose: runtimetest.New("web"), Processes: runtimetest.New("web"), Kubernetes: cluster}
	kind := &kindThere{}
	sandbox.SetKubeRunner(t.Cleanup, kind)
	return m, req, cluster, kind
}

func TestUpRunsInKubernetes(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	m, req, cluster, _ := kubeFixture(t)
	rep := &quietReporter{}
	box, err := m.Up(t.Context(), req, rep)
	if err != nil {
		t.Fatalf("Up: %v", err)
	}
	methods := cluster.Methods()
	for _, want := range []string{"Build", "Up", "WaitReady"} {
		if !slices.Contains(methods, want) {
			t.Errorf("the cluster was not asked to %s: %q", want, methods)
		}
	}
	if !box.Kubernetes || len(box.ComposeFiles) != 1 || !strings.HasSuffix(box.ComposeFiles[0], "pr-7.kube.json") {
		t.Fatalf("box = %+v", box)
	}

	plan, err := kube.ReadPlan(box.ComposeFiles[0])
	if err != nil {
		t.Fatal(err)
	}
	stateKube := filepath.Join(m.StateDir, "kube")
	if plan.Namespace != box.Project || plan.Tool != kube.Kind || plan.Context != "kind-pit" ||
		plan.Kubeconfig != filepath.Join(stateKube, "config-kind") || plan.Web != "web" || plan.WebPort != 8080 || plan.Port != box.Port {
		t.Errorf("plan = %+v", plan)
	}
	// The image is the commit's, named after the sandbox.
	if len(plan.Images) != 1 || plan.Images[0].Tag != "pit.local/"+box.Project+"/shop-web:"+box.SHA[:12] || plan.Images[0].Context != box.Worktree {
		t.Errorf("images = %+v", plan.Images)
	}
	k, err := os.ReadFile(filepath.Join(plan.Kustomization, "kustomization.yaml"))
	if err != nil || !strings.Contains(string(k), "namespace: "+box.Project) || !strings.Contains(string(k), filepath.Join(box.Worktree, "k8s", "web.yaml")) {
		t.Errorf("kustomization = %s, %v", k, err)
	}

	// The commands are given the sandbox, as a kubectl of theirs needs.
	got, err := os.ReadFile(filepath.Join(box.Worktree, "migrated.txt"))
	if err != nil || strings.TrimSpace(string(got)) != box.Project+" kind-pit" {
		t.Errorf("migrated.txt = %q, %v", got, err)
	}
	if calls := m.Data.(*datatest.Fake).Calls(); len(calls) != 1 || !slices.Contains(calls[0].Env, "PIT_NAMESPACE="+box.Project) {
		t.Errorf("data calls = %+v", calls)
	}
	if i := slices.Index(rep.begun, "build"); i < 0 || rep.steps[i] != "1 image, in pit's cluster (kind)" {
		t.Errorf("steps = %q / %q", rep.begun, rep.steps)
	}
	if env := sandbox.CommandEnv(box); !slices.Contains(env, "PIT_NAMESPACE="+box.Project) {
		t.Errorf("env = %q", env)
	}

	// The record says so, and Down takes the plan away.
	f, _ := m.Store.Load()
	if rec, ok := f.Find(req.Repo.Identity.Ref(), 7); !ok || !rec.Kubernetes {
		t.Errorf("record = %+v", rec)
	}
	if err := m.Down(t.Context(), box, nil, nil); err != nil {
		t.Fatal(err)
	}
	for _, left := range []string{box.ComposeFiles[0], plan.Kustomization} {
		if _, err := os.Stat(left); !os.IsNotExist(err) {
			t.Errorf("%s is left: %v", left, err)
		}
	}
	if !slices.Contains(cluster.Methods(), "Down") {
		t.Errorf("the cluster was not asked to take it down: %q", cluster.Methods())
	}
}

func TestAKubernetesSandboxThatFailsIsUndone(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	m, req, cluster, _ := kubeFixture(t)
	cluster.Fail = map[string]error{"Up": os.ErrPermission}
	if _, err := m.Up(t.Context(), req, &quietReporter{}); err == nil {
		t.Fatal("want an error")
	}
	if left, _ := filepath.Glob(filepath.Join(m.StateDir, "*", "pr-7.kube*")); len(left) != 0 {
		t.Errorf("left behind: %q", left)
	}
}

// Without kind or k3d, it says what to install before anything is
// built.
func TestKubernetesWithoutACluster(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	m, req, cluster, _ := kubeFixture(t)
	sandbox.SetKubeRunner(t.Cleanup, &nothingInstalled{})
	_, err := m.Up(t.Context(), req, &quietReporter{})
	if err == nil || !strings.Contains(err.Error(), "neither is installed") {
		t.Errorf("err = %v", err)
	}
	if slices.Contains(cluster.Methods(), "Build") {
		t.Errorf("built anyway: %q", cluster.Methods())
	}
}

type nothingInstalled struct{}

func (nothingInstalled) Output(context.Context, proc.Command) ([]byte, error) {
	return nil, os.ErrNotExist
}

func (nothingInstalled) Stream(context.Context, proc.Command, io.Writer, io.Writer) error {
	return os.ErrNotExist
}

// A review of some workloads, named in compose.services, is checked
// against the manifests' workloads.
func TestKubernetesWorkloadsAreServicesToChooseFrom(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	m, req, cluster, _ := kubeFixture(t)
	pushToPullRequest(t, req.Repo.Root, 7, map[string]string{"k8s/db.yaml": "apiVersion: apps/v1\nkind: StatefulSet\nmetadata: {name: db}\n"})
	chosen, err := config.Parse([]byte(kubeYAML + "compose:\n  services: [web]\n"))
	if err != nil {
		t.Fatal(err)
	}
	req.Config = chosen
	if _, err := m.Up(t.Context(), req, &quietReporter{}); err != nil {
		t.Fatalf("Up: %v", err)
	}
	for _, c := range cluster.Calls() {
		if c.Method == "Up" && !slices.Equal(c.Services, []string{"web"}) {
			t.Errorf("started %q", c.Services)
		}
	}

	m, req, _, _ = kubeFixture(t)
	cfg, err := config.Parse([]byte(kubeYAML + "compose:\n  services: [mailer]\n"))
	if err != nil {
		t.Fatal(err)
	}
	req.Config = cfg
	if _, err := m.Up(t.Context(), req, &quietReporter{}); err == nil || !strings.Contains(err.Error(), "mailer") {
		t.Errorf("err = %v", err)
	}
}

// An image is tagged by its last name alone, whatever registry the
// manifests pull it from, so the tag stays pit's own.
func TestImagesAreTaggedAsPits(t *testing.T) {
	sha := "0123456789abcdef0123"
	for name, want := range map[string]string{
		"shop-web":                         "pit.local/shop/shop-web:0123456789ab",
		"ghcr.io/acme/web":                 "pit.local/shop/web:0123456789ab",
		"ghcr.io/Acme/Web:1.2":             "pit.local/shop/web:0123456789ab",
		"registry:5000/acme/api@sha256:ff": "pit.local/shop/api:0123456789ab",
	} {
		if got := sandbox.ImageTag("shop", name, sha); got != want {
			t.Errorf("ImageTag(%q) = %q, want %q", name, got, want)
		}
	}
}
