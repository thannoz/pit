package kube

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/runtime"
	"github.com/thannoz/pit/internal/runtime/local"
	"github.com/thannoz/pit/internal/runtime/runtimetest"
)

// sandboxIn writes a plan into dir and is the sandbox of it.
func sandboxIn(t *testing.T, dir string, p Plan) runtime.Sandbox {
	t.Helper()
	if p.Project == "" {
		p = Plan{Project: "pit-shop-7", Namespace: "pit-shop-7", Tool: Kind, Kubeconfig: filepath.Join(dir, "kube", "config-kind"),
			Context: "kind-pit", Kustomization: filepath.Join(dir, "pr-7.kube"), Web: "web", WebPort: 80, Port: 40007,
			Images: []Image{{Name: "shop-web", Tag: "pit.local/pit-shop-7/shop-web:abc", Context: filepath.Join(dir, "wt")},
				{Name: "shop-api", Tag: "pit.local/pit-shop-7/shop-api:abc", Context: filepath.Join(dir, "wt", "api"), Dockerfile: "Dockerfile.dev"}}}
	}
	path := filepath.Join(dir, "pr-7.kube.json")
	if err := WritePlan(path, p); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(p.Kubeconfig), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.Kubeconfig, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	return runtime.Sandbox{Project: p.Project, Dir: filepath.Join(dir, "wt"), Files: []string{path}, Kubernetes: true}
}

// clusterUp are the answers of a cluster that is there.
func clusterUp(extra map[string]answer) map[string]answer {
	a := map[string]answer{"kind get clusters": {out: "pit\n"}}
	for k, v := range extra {
		a[k] = v
	}
	return a
}

func TestBuildBuildsAndLoadsEachImage(t *testing.T) {
	dir := t.TempDir()
	s := sandboxIn(t, dir, Plan{})
	f := newFake(clusterUp(map[string]answer{"docker build": {}, "kind load": {}}))
	r := Runtime{Runner: f}
	if err := r.Build(t.Context(), s, nil, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"kind get clusters",
		"docker build --tag pit.local/pit-shop-7/shop-api:abc --file " + filepath.Join(dir, "wt", "api", "Dockerfile.dev") + " " + filepath.Join(dir, "wt", "api"),
		"kind load docker-image pit.local/pit-shop-7/shop-api:abc --name pit",
		"docker build --tag pit.local/pit-shop-7/shop-web:abc " + filepath.Join(dir, "wt"),
		"kind load docker-image pit.local/pit-shop-7/shop-web:abc --name pit",
	}
	if got := f.lines(); !slices.Equal(got, want) {
		t.Errorf("ran\n%q\nwant\n%q", got, want)
	}

	// Named, only those.
	f = newFake(clusterUp(map[string]answer{"docker build": {}, "kind load": {}}))
	r.Runner = f
	if err := r.Build(t.Context(), s, []string{"shop-web"}, io.Discard, io.Discard); err != nil || len(f.lines()) != 3 {
		t.Errorf("ran %q, %v", f.lines(), err)
	}

	f = newFake(clusterUp(map[string]answer{"docker build": {err: errors.New("exit status 1")}}))
	r.Runner = f
	if err := r.Build(t.Context(), s, nil, io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "cannot build the image shop-api") {
		t.Errorf("err = %v", err)
	}
}

const rendered = `apiVersion: v1
kind: Service
metadata: {name: web, namespace: pit-shop-7}
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: web, namespace: pit-shop-7}
spec:
  template:
    spec:
      containers: [{name: web, image: "pit.local/pit-shop-7/shop-web:abc"}]
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: worker, namespace: pit-shop-7}
---
apiVersion: apps/v1
kind: StatefulSet
metadata: {name: db, namespace: pit-shop-7}
---
apiVersion: v1
kind: ConfigMap
metadata: {name: settings, namespace: pit-shop-7}
`

func TestUpAppliesIntoTheNamespace(t *testing.T) {
	dir := t.TempDir()
	s := sandboxIn(t, dir, Plan{})
	f := newFake(clusterUp(map[string]answer{"kubectl kustomize": {out: rendered}, "kubectl --kubeconfig": {}}))
	r := Runtime{Runner: f}
	if err := r.Up(t.Context(), s, nil, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	flags := "kubectl --kubeconfig " + filepath.Join(dir, "kube", "config-kind") + " --context kind-pit --cache-dir " + filepath.Join(dir, "kube", "cache") + " --namespace pit-shop-7"
	var applied []call
	for _, c := range f.calls {
		if c.line == flags+" apply --filename -" {
			applied = append(applied, c)
		}
	}
	if len(applied) != 2 {
		t.Fatalf("ran %q", f.lines())
	}
	// The namespace first, labelled; then everything.
	if !strings.Contains(applied[0].stdin, "kind: Namespace") || !strings.Contains(applied[0].stdin, "name: pit-shop-7") || !strings.Contains(applied[0].stdin, "pit.project: pit-shop-7") {
		t.Errorf("namespace %q", applied[0].stdin)
	}
	if applied[1].stdin != rendered {
		t.Errorf("applied %q", applied[1].stdin)
	}
	if k, _ := f.find("kubectl kustomize --load-restrictor LoadRestrictionsNone " + filepath.Join(dir, "pr-7.kube")); k.line == "" {
		t.Errorf("ran %q", f.lines())
	}

	// A review of some workloads applies those, and all that is not a
	// workload.
	f = newFake(clusterUp(map[string]answer{"kubectl kustomize": {out: rendered}, "kubectl --kubeconfig": {}}))
	r.Runner = f
	if err := r.Up(t.Context(), s, []string{"web", "db"}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	last := f.calls[len(f.calls)-1].stdin
	if strings.Contains(last, "worker") || !strings.Contains(last, "name: db") || !strings.Contains(last, "kind: Service") ||
		!strings.Contains(last, "kind: ConfigMap") || strings.Count(last, "kind: Deployment") != 1 {
		t.Errorf("applied %q", last)
	}
}

func TestUpRefusesWhatCannotWork(t *testing.T) {
	dir := t.TempDir()
	s := sandboxIn(t, dir, Plan{})
	// No workload to open.
	f := newFake(clusterUp(map[string]answer{"kubectl kustomize": {out: strings.ReplaceAll(rendered, "name: web,", "name: shop,")}, "kubectl --kubeconfig": {}}))
	err := Runtime{Runner: f}.Up(t.Context(), s, nil, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "no workload web to open") || !strings.Contains(errs.Hint(err), "shop, worker, db") {
		t.Errorf("err = %v, hint %q", err, errs.Hint(err))
	}
	// pit's image, pulled from a registry.
	always := strings.Replace(rendered, `image: "pit.local/pit-shop-7/shop-web:abc"`, `image: "pit.local/pit-shop-7/shop-web:abc", imagePullPolicy: Always`, 1)
	f = newFake(clusterUp(map[string]answer{"kubectl kustomize": {out: always}, "kubectl --kubeconfig": {}}))
	if err := (Runtime{Runner: f}).Up(t.Context(), s, nil, io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "pulls shop-web always") {
		t.Errorf("err = %v", err)
	}
	if _, applied := f.find("kubectl --kubeconfig"); applied {
		t.Errorf("applied anyway: %q", f.lines())
	}
	// kubectl refusing a manifest.
	f = newFake(clusterUp(map[string]answer{"kubectl kustomize": {out: rendered}, "kubectl --kubeconfig": {err: errors.New("exit status 1")}}))
	if err := (Runtime{Runner: f}).Up(t.Context(), s, nil, io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "cannot make the namespace") {
		t.Errorf("err = %v", err)
	}
}

func TestDownTakesTheForwardAndTheNamespace(t *testing.T) {
	dir := t.TempDir()
	s := sandboxIn(t, dir, Plan{})
	forward := runtimetest.New("forward")
	fs, _ := forwardSandbox(s, Plan{Project: "pit-shop-7"})
	for _, f := range fs.Files {
		if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	f := newFake(clusterUp(map[string]answer{"kubectl --kubeconfig": {}}))
	if err := (Runtime{Runner: f, Forward: forward}).Down(t.Context(), s, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	if m := forward.Methods(); !slices.Contains(m, "Down") {
		t.Errorf("forward: %q", m)
	}
	del, ok := f.find("kubectl --kubeconfig")
	if !ok || !strings.Contains(del.line, "delete namespace pit-shop-7 --ignore-not-found --wait") || strings.Contains(del.line, "--namespace") {
		t.Errorf("ran %q", f.lines())
	}
	for _, file := range fs.Files {
		if _, err := os.Stat(file); !os.IsNotExist(err) {
			t.Errorf("%s is left: %v", file, err)
		}
	}
	// Without the cluster, there is nothing in it to delete.
	f = newFake(map[string]answer{"kind get clusters": {out: "No kind clusters found.\n"}})
	if err := (Runtime{Runner: f, Forward: runtimetest.New("forward")}).Down(t.Context(), s, io.Discard, io.Discard); err != nil || len(f.lines()) != 1 {
		t.Errorf("ran %q, %v", f.lines(), err)
	}
}

func TestStatusOfTheWorkloads(t *testing.T) {
	st, err := statuses([]byte(`{"items": [
		{"kind": "Deployment", "metadata": {"name": "web"}, "spec": {"replicas": 2}, "status": {"readyReplicas": 2}},
		{"kind": "Deployment", "metadata": {"name": "worker"}, "spec": {"replicas": 1}, "status": {}},
		{"kind": "Deployment", "metadata": {"name": "mailer"}, "spec": {}, "status": {"readyReplicas": 1}},
		{"kind": "StatefulSet", "metadata": {"name": "db"}, "spec": {"replicas": 0}, "status": {}},
		{"kind": "DaemonSet", "metadata": {"name": "agent"}, "spec": {}, "status": {"desiredNumberScheduled": 1, "numberReady": 1}}
	]}`))
	if err != nil {
		t.Fatal(err)
	}
	want := []runtime.Status{
		{Service: "web", Container: "deployment/web", State: "running"},
		{Service: "worker", Container: "deployment/worker", State: "starting"},
		{Service: "mailer", Container: "deployment/mailer", State: "running"},
		{Service: "db", Container: "statefulset/db", State: "stopped"},
		{Service: "agent", Container: "daemonset/agent", State: "running"},
	}
	if !slices.Equal(st, want) {
		t.Errorf("%+v", st)
	}
	// No cluster: nothing is running.
	dir := t.TempDir()
	s := sandboxIn(t, dir, Plan{})
	got, err := Runtime{Runner: newFake(map[string]answer{"kind get clusters": {out: ""}})}.Status(t.Context(), s)
	if err != nil || len(got) != 0 {
		t.Errorf("%+v, %v", got, err)
	}
}

func TestLogsSince(t *testing.T) {
	since := time.Date(2026, 9, 26, 13, 27, 52, 500_000_000, time.UTC)
	lines := timestamped([]byte("2026-09-26T13:27:52.100000000Z too early\n2026-09-26T13:27:52.834342637Z GET / 200\nnot a line\n2026-09-26T13:28:01Z GET /tea 200\n"), since)
	if len(lines) != 2 || lines[0].Text != "GET / 200" || lines[1].Text != "GET /tea 200" || !lines[1].At.Equal(time.Date(2026, 9, 26, 13, 28, 1, 0, time.UTC)) {
		t.Errorf("%+v", lines)
	}

	dir := t.TempDir()
	s := sandboxIn(t, dir, Plan{})
	f := newFake(map[string]answer{"kubectl kustomize": {out: rendered}, "kubectl --kubeconfig": {out: "2026-09-26T13:28:01Z hi\n"}})
	got, err := Runtime{Runner: f}.LogsSince(t.Context(), s, "db", since)
	if err != nil || len(got) != 1 {
		t.Fatalf("%+v, %v", got, err)
	}
	if c := f.calls[len(f.calls)-1].line; !strings.HasSuffix(c, "logs statefulset/db --timestamps --since-time 2026-09-26T13:27:52Z") {
		t.Errorf("ran %q", c)
	}
	// No name is the web workload's.
	if _, err := (Runtime{Runner: f}).Logs(t.Context(), s, "", 5); err != nil || !strings.HasSuffix(f.calls[len(f.calls)-1].line, "logs deployment/web --tail 5") {
		t.Errorf("ran %q, %v", f.lines(), err)
	}
	if _, err := (Runtime{Runner: f}).Logs(t.Context(), s, "mailer", 5); err == nil || !strings.Contains(errs.Hint(err), "web, worker, db") {
		t.Errorf("err = %v", err)
	}
}

func TestPortStopAndPause(t *testing.T) {
	dir := t.TempDir()
	s := sandboxIn(t, dir, Plan{})
	r := Runtime{Runner: newFake(map[string]answer{"kubectl kustomize": {out: rendered}, "kubectl --kubeconfig": {}})}
	if p, err := r.Port(t.Context(), s, "web", 80); err != nil || p != "40007" {
		t.Errorf("%q, %v", p, err)
	}
	if _, err := r.Port(t.Context(), s, "db", 5432); err == nil {
		t.Error("a port for a workload that is not forwarded")
	}
	if err := r.Stop(t.Context(), s, []string{"worker"}); err != nil {
		t.Fatal(err)
	}
	f := r.Runner.(*fake)
	if c := f.calls[len(f.calls)-1].line; !strings.HasSuffix(c, "scale deployment/worker --replicas 0") {
		t.Errorf("ran %q", c)
	}
	if err := r.Pause(t.Context(), s, nil); err == nil || !strings.Contains(err.Error(), "cannot pause") {
		t.Errorf("err = %v", err)
	}
	if err := r.Unpause(t.Context(), s, nil); err == nil {
		t.Error("unpaused")
	}
	if names, err := r.Services(t.Context(), s); err != nil || !slices.Equal(names, []string{"web", "worker", "db"}) {
		t.Errorf("%q, %v", names, err)
	}
}

// forwardRecorder is a Forward that notes the Procfile it is given.
type forwardRecorder struct {
	*runtimetest.Fake
	procfile string
	plan     local.Plan
}

func (f *forwardRecorder) Up(ctx context.Context, s runtime.Sandbox, services []string, stdout, stderr io.Writer) error {
	data, _ := os.ReadFile(s.Files[0])
	f.procfile = string(data)
	f.plan, _ = local.ReadPlan(s.Files[1])
	return f.Fake.Up(ctx, s, services, stdout, stderr)
}

func TestWaitReadyWaitsForTheRolloutThenForwards(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") }))
	defer srv.Close()
	dir := t.TempDir()
	s := sandboxIn(t, dir, Plan{})
	f := newFake(map[string]answer{
		"kubectl kustomize":    {out: rendered},
		"kubectl --kubeconfig": {out: `deployment "web" successfully rolled out`},
	})
	fw := &forwardRecorder{Fake: runtimetest.New("forward")}
	probe := runtime.Probe{URL: srv.URL, ExpectStatus: 200, Timeout: 5 * time.Second, Interval: 10 * time.Millisecond}
	if err := (Runtime{Runner: f, Forward: fw}).WaitReady(t.Context(), s, "web", probe); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.find("kubectl --kubeconfig"); !ok || !strings.Contains(f.lines()[1], "rollout status deployment/web --watch=false") {
		t.Errorf("ran %q", f.lines())
	}
	// A loop, since kubectl's forward ends with the pod it reaches; on
	// this machine's loopback only; on the sandbox's port.
	for _, want := range []string{"forward: while :; do kubectl", "port-forward --address 127.0.0.1 deployment/web \"$PORT\":80; sleep 1; done", "--namespace pit-shop-7"} {
		if !strings.Contains(fw.procfile, want) {
			t.Errorf("Procfile %q lacks %q", fw.procfile, want)
		}
	}
	if fw.plan.Web != "forward" || fw.plan.Port != 40007 {
		t.Errorf("plan %+v", fw.plan)
	}
}

func TestWaitReadyStopsForAPodThatCannotStart(t *testing.T) {
	dir := t.TempDir()
	s := sandboxIn(t, dir, Plan{})
	pods := `{"items": [{"metadata": {"name": "web-7d9f"}, "status": {"containerStatuses": [{"state": {"waiting": {"reason": "CrashLoopBackOff", "message": "back-off 10s restarting failed container"}}}]}}]}`
	f := newFake(map[string]answer{
		"kubectl kustomize": {out: rendered},
		"kubectl --kubeconfig " + filepath.Join(dir, "kube", "config-kind") + " --context kind-pit --cache-dir " + filepath.Join(dir, "kube", "cache") + " --namespace pit-shop-7 rollout":            {out: "Waiting for deployment \"web\" rollout to finish"},
		"kubectl --kubeconfig " + filepath.Join(dir, "kube", "config-kind") + " --context kind-pit --cache-dir " + filepath.Join(dir, "kube", "cache") + " --namespace pit-shop-7 get deployment/web": {out: `{"app":"web","tier":"front"}`},
		"kubectl --kubeconfig " + filepath.Join(dir, "kube", "config-kind") + " --context kind-pit --cache-dir " + filepath.Join(dir, "kube", "cache") + " --namespace pit-shop-7 get pods":           {out: pods},
		"kubectl --kubeconfig " + filepath.Join(dir, "kube", "config-kind") + " --context kind-pit --cache-dir " + filepath.Join(dir, "kube", "cache") + " --namespace pit-shop-7 logs pod/web-7d9f":  {out: "panic: no database\n"},
	})
	probe := runtime.Probe{URL: "http://127.0.0.1:1", ExpectStatus: 200, Timeout: time.Minute, Interval: 10 * time.Millisecond}
	err := Runtime{Runner: f, Forward: runtimetest.New("forward")}.WaitReady(t.Context(), s, "web", probe)
	if err == nil || !strings.Contains(err.Error(), "web cannot start: CrashLoopBackOff (back-off 10s restarting failed container)") ||
		!strings.Contains(err.Error(), "panic: no database") {
		t.Errorf("err = %v", err)
	}
	if c, _ := f.find("kubectl --kubeconfig " + filepath.Join(dir, "kube", "config-kind") + " --context kind-pit --cache-dir " + filepath.Join(dir, "kube", "cache") + " --namespace pit-shop-7 get pods"); !strings.Contains(c.line, "--selector app=web,tier=front") {
		t.Errorf("asked %q", c.line)
	}
	if c, _ := f.find("kubectl --kubeconfig " + filepath.Join(dir, "kube", "config-kind") + " --context kind-pit --cache-dir " + filepath.Join(dir, "kube", "cache") + " --namespace pit-shop-7 logs"); !strings.Contains(c.line, "--previous") {
		t.Errorf("asked %q", c.line)
	}
}

// A pod can roll out and crash a moment later; the probe is stopped
// then too.
func TestWaitReadyStopsForAPodThatCrashesAfterItsRollout(t *testing.T) {
	dir := t.TempDir()
	s := sandboxIn(t, dir, Plan{})
	flags := "kubectl --kubeconfig " + filepath.Join(dir, "kube", "config-kind") + " --context kind-pit --cache-dir " + filepath.Join(dir, "kube", "cache") + " --namespace pit-shop-7 "
	f := newFake(map[string]answer{
		"kubectl kustomize":          {out: rendered},
		flags + "rollout":            {out: `deployment "web" successfully rolled out`},
		flags + "get deployment/web": {out: `{"app":"web"}`},
		flags + "get pods":           {out: `{"items": [{"metadata": {"name": "web-1"}, "status": {"containerStatuses": [{"state": {"waiting": {"reason": "CrashLoopBackOff"}}}]}}]}`},
		flags + "logs":               {out: "panic\n"},
	})
	probe := runtime.Probe{URL: "http://127.0.0.1:1", ExpectStatus: 200, Timeout: 3 * time.Second, Interval: 10 * time.Millisecond}
	started := time.Now()
	err := Runtime{Runner: f, Forward: runtimetest.New("forward")}.WaitReady(t.Context(), s, "web", probe)
	if err == nil || !strings.Contains(err.Error(), "web cannot start: CrashLoopBackOff") || time.Since(started) > 10*time.Second {
		t.Errorf("err = %v after %v", err, time.Since(started))
	}
}

func TestWaitReadyGivesUp(t *testing.T) {
	dir := t.TempDir()
	s := sandboxIn(t, dir, Plan{})
	f := newFake(map[string]answer{"kubectl kustomize": {out: rendered}, "kubectl --kubeconfig": {out: "Waiting for rollout"}})
	probe := runtime.Probe{URL: "http://127.0.0.1:1", ExpectStatus: 200, Timeout: 30 * time.Millisecond, Interval: 10 * time.Millisecond}
	err := Runtime{Runner: f, Forward: runtimetest.New("forward")}.WaitReady(t.Context(), s, "web", probe)
	if err == nil || !strings.Contains(err.Error(), "deployment/web did not roll out") || !strings.Contains(errs.Hint(err), "get pods") {
		t.Errorf("err = %v", err)
	}

	// Rolled out, but nothing answers.
	l, _ := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	addr := l.Addr().String()
	_ = l.Close()
	f = newFake(map[string]answer{"kubectl kustomize": {out: rendered}, "kubectl --kubeconfig": {out: "successfully rolled out"}})
	probe.URL = "http://" + addr
	err = Runtime{Runner: f, Forward: runtimetest.New("forward")}.WaitReady(t.Context(), s, "web", probe)
	if err == nil || !strings.Contains(err.Error(), "did not become ready") {
		t.Errorf("err = %v", err)
	}
}

func TestShellQuote(t *testing.T) {
	for in, want := range map[string]string{
		"kubectl":                     "kubectl",
		"/state/kube/config-kind":     "/state/kube/config-kind",
		"/Users/me/Library/App Stuff": "'/Users/me/Library/App Stuff'",
		"it's":                        `'it'\''s'`,
		"":                            "''",
		"deployment/web":              "deployment/web",
		strconv.Itoa(8080):            "8080",
	} {
		if got := shellQuote(in); got != want {
			t.Errorf("%q: %s, want %s", in, got, want)
		}
	}
}
