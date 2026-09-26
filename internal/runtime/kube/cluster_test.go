package kube

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/thannoz/pit/internal/errs"
)

var errNotInstalled = errors.New(`exec: "x": executable file not found in $PATH`)

// onSystem makes Choose believe it runs on goos, until the test ends.
func onSystem(t *testing.T, name string) {
	previous := goos
	goos = name
	t.Cleanup(func() { goos = previous })
}

func TestChooseTheToolThatMadeTheCluster(t *testing.T) {
	onSystem(t, "linux")
	dir := t.TempDir()
	for _, tc := range []struct {
		name    string
		answers map[string]answer
		want    string
	}{
		{"kind made it", map[string]answer{"kind get clusters": {out: "other\npit\n"}, "k3d cluster list": {out: `[]`}}, Kind},
		{"k3d made it", map[string]answer{"kind get clusters": {out: "other\n"}, "k3d cluster list": {out: `[{"name":"pit"}]`}}, K3d},
		{"neither: kind first", map[string]answer{"kind get clusters": {out: "No kind clusters found.\n"}, "k3d cluster list": {out: `[]`}}, Kind},
		{"only k3d", map[string]answer{"kind get clusters": {err: errNotInstalled}, "k3d cluster list": {out: `[]`}}, K3d},
	} {
		c, err := Choose(t.Context(), newFake(tc.answers), dir)
		if err != nil || c.Tool != tc.want || c.Dir != dir {
			t.Errorf("%s: %+v, %v", tc.name, c, err)
		}
	}
	// A cluster whose name only begins with pit's is another.
	if exists, err := (Cluster{Tool: Kind, Runner: newFake(map[string]answer{"kind get clusters": {out: "pit-probe\npits\n"}})}).Exists(t.Context()); err != nil || exists {
		t.Errorf("pit-probe taken for pit: %v, %v", exists, err)
	}
	_, err := Choose(t.Context(), newFake(map[string]answer{"kind": {err: errNotInstalled}, "k3d": {err: errNotInstalled}}), dir)
	if err == nil || !strings.Contains(err.Error(), "neither is installed") || !strings.Contains(errs.Hint(err), "kind.sigs.k8s.io") {
		t.Errorf("err = %v", err)
	}
}

// On Windows, before anything is asked or built: the forward needs
// pit's process supervisor, which Windows lacks.
func TestChooseRefusesWindows(t *testing.T) {
	onSystem(t, "windows")
	f := newFake(map[string]answer{"kind get clusters": {out: "pit\n"}})
	_, err := Choose(t.Context(), f, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "macOS and Linux only") {
		t.Errorf("err = %v", err)
	}
	if len(f.lines()) != 0 {
		t.Errorf("asked anyway: %q", f.lines())
	}
}

// pit's kubeconfig is its own: the reviewer's current context may be
// the cluster a team deploys to.
func TestEnsureMakesTheClusterWithAKubeconfigOfPits(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "kube")
	f := newFake(map[string]answer{"kind get clusters": {out: "No kind clusters found.\n"}, "kind create cluster": {}, "kind export kubeconfig": {}})
	c := Cluster{Tool: Kind, Dir: dir, Runner: f}
	made, err := c.Ensure(t.Context(), io.Discard, io.Discard)
	if err != nil || !made {
		t.Fatalf("%v, %v", made, err)
	}
	want := "kind create cluster --name pit --kubeconfig " + filepath.Join(dir, "config-kind") + " --wait 120s"
	if !slices.Contains(f.lines(), want) {
		t.Errorf("ran %q", f.lines())
	}
	if c.Context() != "kind-pit" {
		t.Errorf("context %s", c.Context())
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Errorf("dir: %v", err)
	}

	// There, with its kubeconfig: nothing to do.
	if err := os.WriteFile(c.Kubeconfig(), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	f = newFake(map[string]answer{"kind get clusters": {out: "pit\n"}})
	c.Runner = f
	if made, err := c.Ensure(t.Context(), io.Discard, io.Discard); err != nil || made || len(f.lines()) != 1 {
		t.Errorf("%v, %v, ran %q", made, err, f.lines())
	}

	// There, without it: it is written again.
	_ = os.Remove(c.Kubeconfig())
	f = newFake(map[string]answer{"kind get clusters": {out: "pit\n"}, "kind export kubeconfig": {}})
	c.Runner = f
	if made, err := c.Ensure(t.Context(), io.Discard, io.Discard); err != nil || made {
		t.Errorf("%v, %v", made, err)
	}
	if _, ok := f.find("kind export kubeconfig --name pit --kubeconfig " + c.Kubeconfig()); !ok {
		t.Errorf("ran %q", f.lines())
	}
}

func TestEnsureWithK3d(t *testing.T) {
	dir := t.TempDir()
	f := newFake(map[string]answer{"k3d cluster list": {out: `[]`}, "k3d cluster create": {}, "k3d kubeconfig write": {}})
	c := Cluster{Tool: K3d, Dir: dir, Runner: f}
	if _, err := c.Ensure(t.Context(), io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	// k3d is told to leave the reviewer's kubeconfig alone, and writes
	// pit's where pit keeps it.
	create, _ := f.find("k3d cluster create pit")
	if !strings.Contains(create.line, "--kubeconfig-update-default=false") || !strings.Contains(create.line, "--kubeconfig-switch-context=false") {
		t.Errorf("created with %q", create.line)
	}
	if _, ok := f.find("k3d kubeconfig write pit --output " + filepath.Join(dir, "config-k3d")); !ok {
		t.Errorf("ran %q", f.lines())
	}
	if c.Context() != "k3d-pit" {
		t.Errorf("context %s", c.Context())
	}
}

func TestEnsureSaysWhatWentWrong(t *testing.T) {
	c := Cluster{Tool: Kind, Dir: t.TempDir(), Runner: newFake(map[string]answer{"kind get clusters": {err: errNotInstalled}})}
	if _, err := c.Ensure(t.Context(), io.Discard, io.Discard); err == nil || !strings.Contains(errs.Hint(err), "Docker is running") {
		t.Errorf("err = %v", err)
	}
	c.Runner = newFake(map[string]answer{"kind get clusters": {}, "kind create cluster": {err: errors.New("exit status 1")}})
	if _, err := c.Ensure(t.Context(), io.Discard, io.Discard); err == nil || !strings.Contains(errs.Hint(err), "kind create cluster") {
		t.Errorf("err = %v, hint %q", err, errs.Hint(err))
	}
}

func TestLoad(t *testing.T) {
	for tool, want := range map[string]string{
		Kind: "kind load docker-image pit.local/p/web:abc --name pit",
		K3d:  "k3d image import pit.local/p/web:abc --cluster pit",
	} {
		f := newFake(map[string]answer{tool: {}})
		if err := (Cluster{Tool: tool, Runner: f}).Load(t.Context(), "pit.local/p/web:abc", io.Discard, io.Discard); err != nil || f.lines()[0] != want {
			t.Errorf("%s: %q, %v", tool, f.lines(), err)
		}
	}
	f := newFake(map[string]answer{"kind": {err: errors.New("exit status 1")}})
	if err := (Cluster{Tool: Kind, Runner: f}).Load(t.Context(), "x", io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "cannot load x") {
		t.Errorf("err = %v", err)
	}
	if err := (Cluster{Tool: "minikube", Runner: f}).Load(t.Context(), "x", io.Discard, io.Discard); err == nil {
		t.Error("loaded with a tool pit does not know")
	}
}

func TestPlan(t *testing.T) {
	p := Plan{Project: "pit-shop-7", Namespace: "pit-shop-7", Kubeconfig: filepath.Join("/state", "kube", "config-kind"), Context: "kind-pit",
		Images: []Image{{Name: "web", Tag: "t2"}, {Name: "api", Tag: "t1"}}}
	cache := filepath.Join("/state", "kube", "cache")
	if got := p.Flags(); !slices.Equal(got, []string{"--kubeconfig", p.Kubeconfig, "--context", "kind-pit", "--cache-dir", cache, "--namespace", "pit-shop-7"}) {
		t.Errorf("flags %q", got)
	}
	env := p.Environment()
	for _, want := range []string{"KUBECONFIG=" + p.Kubeconfig, "KUBECACHEDIR=" + cache, "PIT_KUBE_CONTEXT=kind-pit", "PIT_NAMESPACE=pit-shop-7", "PIT_PROJECT=pit-shop-7"} {
		if !slices.Contains(env, want) {
			t.Errorf("env %q lacks %s", env, want)
		}
	}
	path := filepath.Join(t.TempDir(), "sub", "pr-7.kube.json")
	if err := WritePlan(path, p); err != nil {
		t.Fatal(err)
	}
	got, err := ReadPlan(path)
	if err != nil || got.Project != p.Project || len(got.Images) != 2 || got.Images[0].Name != "api" {
		t.Errorf("%+v, %v", got, err)
	}
	if _, err := ReadPlan(filepath.Join(t.TempDir(), "none")); err == nil {
		t.Error("read a plan that is not there")
	}
}
