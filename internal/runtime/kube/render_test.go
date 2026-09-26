package kube

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/proc"
)

const webYAML = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
spec:
  selector: {matchLabels: {app: web}}
  template:
    metadata: {labels: {app: web}}
    spec:
      initContainers:
        - name: migrate
          image: shop-web
      containers:
        - name: web
          image: shop-web
          imagePullPolicy: IfNotPresent
---
apiVersion: v1
kind: Service
metadata:
  name: web
spec:
  selector: {app: web}
  ports: [{port: 80}]
---
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: db
spec:
  template:
    spec:
      containers:
        - name: postgres
          image: postgres:17
`

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestWorkloads(t *testing.T) {
	ws, err := Workloads([]byte(webYAML + "---\napiVersion: apps/v1\nkind: DaemonSet\nmetadata: {name: agent}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(ws) != 3 || ws[0].Ref() != "deployment/web" || ws[1].Ref() != "statefulset/db" || ws[2].Ref() != "daemonset/agent" {
		t.Fatalf("%+v", ws)
	}
	// Init containers too: they run the image as well.
	if len(ws[0].Containers) != 2 || ws[0].Containers[0].Name != "migrate" || ws[0].Containers[1].PullPolicy != "IfNotPresent" {
		t.Errorf("%+v", ws[0].Containers)
	}
	if _, err := Workloads([]byte("kind: [")); err == nil {
		t.Error("read what is not YAML")
	}
}

func TestResources(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "one.yaml"), webYAML)
	write(t, filepath.Join(root, "plain", "b.yml"), webYAML)
	write(t, filepath.Join(root, "plain", "a.yaml"), webYAML)
	write(t, filepath.Join(root, "plain", "README.md"), "no")
	write(t, filepath.Join(root, "overlay", "kustomization.yaml"), "resources: []\n")
	write(t, filepath.Join(root, "empty", "README.md"), "no")

	got, err := Resources([]string{filepath.Join(root, "one.yaml"), filepath.Join(root, "plain"), filepath.Join(root, "overlay")})
	want := []string{filepath.Join(root, "one.yaml"), filepath.Join(root, "plain", "a.yaml"), filepath.Join(root, "plain", "b.yml"), filepath.Join(root, "overlay")}
	if err != nil || !slices.Equal(got, want) {
		t.Errorf("%q, %v", got, err)
	}
	if _, err := Resources([]string{filepath.Join(root, "empty")}); err == nil || !strings.Contains(err.Error(), "has no manifests") {
		t.Errorf("err = %v", err)
	}
	if _, err := Resources([]string{filepath.Join(root, "none")}); err == nil {
		t.Error("read manifests that are not there")
	}
	for _, name := range kustomizations {
		dir := filepath.Join(t.TempDir(), "k")
		write(t, filepath.Join(dir, name), "")
		if !IsKustomization(dir) {
			t.Errorf("%s is not taken for a kustomization", name)
		}
	}
}

func TestSplitTag(t *testing.T) {
	for image, want := range map[string][2]string{
		"pit.local/p/web:abc":   {"pit.local/p/web", "abc"},
		"localhost:5000/web":    {"localhost:5000/web", "latest"},
		"localhost:5000/web:v1": {"localhost:5000/web", "v1"},
		"web":                   {"web", "latest"},
	} {
		if n, tag := splitTag(image); n != want[0] || tag != want[1] {
			t.Errorf("%s: %s %s", image, n, tag)
		}
	}
}

func TestWriteKustomization(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "pr-7.kube")
	err := WriteKustomization(dir, "pit-shop-7", []string{"/wt/k8s/web.yaml", "/wt/k8s/overlay"}, []Image{{Name: "shop-web", Tag: "pit.local/pit-shop-7/shop-web:abc123"}})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "kustomization.yaml"))
	for _, want := range []string{"kind: Kustomization", "namespace: pit-shop-7", "- /wt/k8s/web.yaml", "- /wt/k8s/overlay",
		"name: shop-web", "newName: pit.local/pit-shop-7/shop-web", "newTag: abc123"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("kustomization lacks %q:\n%s", want, data)
		}
	}
}

// What kubectl makes of pit's kustomization, where kubectl is there:
// the namespace everywhere, pit's image, the manifests outside pit's
// directory read.
func TestRenderWithKubectl(t *testing.T) {
	if _, err := exec.LookPath("kubectl"); err != nil {
		t.Skip("skipping: no kubectl")
	}
	root := t.TempDir()
	write(t, filepath.Join(root, "k8s", "web.yaml"), webYAML)
	dir := filepath.Join(root, "state", "pr-7.kube")
	images := []Image{{Name: "shop-web", Tag: "pit.local/pit-shop-7/shop-web:abc123"}}
	if err := WriteKustomization(dir, "pit-shop-7", []string{filepath.Join(root, "k8s", "web.yaml")}, images); err != nil {
		t.Fatal(err)
	}
	out, err := Render(t.Context(), proc.Exec{}, dir)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(out), "namespace: pit-shop-7") != 3 || strings.Count(string(out), "image: pit.local/pit-shop-7/shop-web:abc123") != 2 {
		t.Errorf("rendered:\n%s", out)
	}
	ws, _ := Workloads(out)
	if err := CheckPulls(ws, images); err != nil {
		t.Error(err)
	}
}

func TestRenderSaysHowToReproduce(t *testing.T) {
	f := newFake(map[string]answer{"kubectl kustomize": {err: errs.New("exit status 1")}})
	if _, err := Render(t.Context(), f, "/state/pr-7.kube"); err == nil || !strings.Contains(errs.Hint(err), "LoadRestrictionsNone /state/pr-7.kube") {
		t.Errorf("err = %v", err)
	}
}

func TestCheckPulls(t *testing.T) {
	images := []Image{{Name: "shop-web", Tag: "pit.local/p/shop-web:abc"}}
	always := []Workload{{Kind: "deployment", Name: "web", Containers: []Container{{Name: "web", Image: "pit.local/p/shop-web:abc", PullPolicy: "Always"}}}}
	err := CheckPulls(always, images)
	if err == nil || !strings.Contains(err.Error(), "deployment/web pulls shop-web always") || !strings.Contains(errs.Hint(err), "IfNotPresent for its container web") {
		t.Errorf("err = %v", err)
	}
	// An image pit does not build may be pulled as often as it likes.
	always[0].Containers[0].Image = "postgres:17"
	if err := CheckPulls(always, images); err != nil {
		t.Error(err)
	}
}

func TestReadWorkloads(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "k8s", "web.yaml"), webYAML)
	write(t, filepath.Join(root, "overlay", "kustomization.yaml"), "resources: []\n")
	f := newFake(map[string]answer{"kubectl kustomize": {out: "apiVersion: apps/v1\nkind: Deployment\nmetadata: {name: worker}\n"}})
	ws, err := ReadWorkloads(t.Context(), f, []string{filepath.Join(root, "k8s"), filepath.Join(root, "overlay")})
	if err != nil || len(ws) != 3 || ws[2].Name != "worker" {
		t.Errorf("%+v, %v", ws, err)
	}
	// Plain files need no kubectl.
	if len(f.lines()) != 1 {
		t.Errorf("ran %q", f.lines())
	}
}
