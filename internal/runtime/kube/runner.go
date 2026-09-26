package kube

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/proc"
	"github.com/thannoz/pit/internal/runtime"
	"github.com/thannoz/pit/internal/runtime/local"
)

// Runtime runs a sandbox in pit's cluster. A sandbox's Files are its
// plan.
//
// The cluster runs the workloads; what it does not do is let this
// machine reach them. That is a kubectl port-forward, which has to run
// on after pit is gone: Forward runs it, as a sandbox of one process.
type Runtime struct {
	Runner Runner
	// Forward runs the port-forward of each sandbox.
	Forward runtime.Runtime
}

var _ runtime.Runtime = Runtime{}

func (r Runtime) plan(s runtime.Sandbox) (Plan, error) {
	if len(s.Files) < 1 {
		return Plan{}, errs.New("the sandbox %s has no plan", s.Project)
	}
	return ReadPlan(s.Files[0])
}

func (r Runtime) cluster(p Plan) Cluster {
	return Cluster{Tool: p.Tool, Dir: filepath.Dir(p.Kubeconfig), Runner: r.Runner}
}

// kubectl is a kubectl command for the sandbox.
func kubectl(p Plan, args ...string) proc.Command {
	return proc.Command{Name: "kubectl", Args: append(p.Flags(), args...)}
}

// Build builds the plan's images, or the named ones, and loads them
// into the cluster, which it makes first if it has to.
func (r Runtime) Build(ctx context.Context, s runtime.Sandbox, images []string, stdout, stderr io.Writer) error {
	p, err := r.plan(s)
	if err != nil {
		return err
	}
	c := r.cluster(p)
	if _, err := c.Ensure(ctx, stdout, stderr); err != nil {
		return err
	}
	for _, im := range p.Images {
		if len(images) > 0 && !slices.Contains(images, im.Name) {
			continue
		}
		args := []string{"build", "--tag", im.Tag}
		if im.Dockerfile != "" {
			args = append(args, "--file", filepath.Join(im.Context, im.Dockerfile))
		}
		args = append(args, im.Context)
		if err := r.Runner.Stream(ctx, proc.Command{Name: "docker", Args: args, Dir: s.Dir}, stdout, stderr); err != nil {
			return errs.Wrap(err, "cannot build the image %s", im.Name).
				WithHint("the build output above says what went wrong")
		}
		if err := c.Load(ctx, im.Tag, stdout, stderr); err != nil {
			return err
		}
	}
	return nil
}

// Pull fetches an image and loads it into the cluster.
func (r Runtime) Pull(ctx context.Context, s runtime.Sandbox, image string, stdout, stderr io.Writer) error {
	p, err := r.plan(s)
	if err != nil {
		return err
	}
	if err := r.Runner.Stream(ctx, proc.Command{Name: "docker", Args: []string{"pull", image}}, stdout, stderr); err != nil {
		return errs.Wrap(err, "cannot pull %s", image)
	}
	return r.cluster(p).Load(ctx, image, stdout, stderr)
}

// Up applies the manifests into the sandbox's namespace, the named
// workloads of them or all. The web workload's port is forwarded once
// it has rolled out, by WaitReady: forwarded now, it would reach the
// pod of the commit before.
func (r Runtime) Up(ctx context.Context, s runtime.Sandbox, services []string, stdout, stderr io.Writer) error {
	p, err := r.plan(s)
	if err != nil {
		return err
	}
	if _, err := r.cluster(p).Ensure(ctx, stdout, stderr); err != nil {
		return err
	}
	rendered, err := Render(ctx, r.Runner, p.Kustomization)
	if err != nil {
		return err
	}
	workloads, err := Workloads(rendered)
	if err != nil {
		return err
	}
	if err := CheckPulls(workloads, p.Images); err != nil {
		return err
	}
	if len(services) > 0 {
		if rendered, err = only(rendered, services); err != nil {
			return err
		}
	}

	// Labelled, so that what is left of a sandbox can be found by it.
	ns := fmt.Sprintf("apiVersion: v1\nkind: Namespace\nmetadata:\n  name: %s\n  labels:\n    pit.project: %s\n", p.Namespace, p.Project)
	apply := kubectl(p, "apply", "--filename", "-")
	apply.Stdin = strings.NewReader(ns)
	if err := r.Runner.Stream(ctx, apply, stdout, stderr); err != nil {
		return errs.Wrap(err, "cannot make the namespace %s", p.Namespace)
	}
	apply = kubectl(p, "apply", "--filename", "-")
	apply.Stdin = bytes.NewReader(rendered)
	if err := r.Runner.Stream(ctx, apply, stdout, stderr); err != nil {
		return errs.Wrap(err, "cannot apply the manifests").
			WithHint("kubectl's output above says which one it refused")
	}
	if _, ok := find(workloads, p.Web); p.Web != "" && !ok {
		return errs.New("the manifests have no workload %s to open", p.Web).
			WithHint("web.service names a Deployment, StatefulSet or DaemonSet: %s", names(workloads))
	}
	return nil
}

// forward starts the port-forward, which runs until the sandbox goes:
// kubectl's ends whenever the pod it reaches does, as on every update,
// so it is started again each time.
func (r Runtime) forward(ctx context.Context, s runtime.Sandbox, p Plan, ref string) error {
	fs, err := forwardSandbox(s, p)
	if err != nil {
		return err
	}
	cmd := kubectl(p, "port-forward", "--address", "127.0.0.1", ref, `"$PORT":`+strconv.Itoa(p.WebPort))
	quoted := make([]string, 0, len(cmd.Args)+1)
	for _, a := range append([]string{cmd.Name}, cmd.Args...) {
		if strings.HasPrefix(a, `"$PORT"`) {
			quoted = append(quoted, a)
			continue
		}
		quoted = append(quoted, shellQuote(a))
	}
	line := "forward: while :; do " + strings.Join(quoted, " ") + "; sleep 1; done\n"
	if err := os.WriteFile(fs.Files[0], []byte(line), 0o600); err != nil {
		return errs.Wrap(err, "cannot write %s", fs.Files[0])
	}
	if err := local.WritePlan(fs.Files[1], local.Plan{Project: p.Project, Web: "forward", Port: p.Port}); err != nil {
		return err
	}
	// What the supervisor says of it -- a pid, a port -- is pit's
	// business, not the reviewer's.
	return r.Forward.Up(ctx, fs, nil, io.Discard, io.Discard)
}

// forwardSandbox is the sandbox of one process that forwards a
// sandbox's port: its Procfile and plan, beside the sandbox's own.
func forwardSandbox(s runtime.Sandbox, p Plan) (runtime.Sandbox, error) {
	if len(s.Files) < 1 {
		return runtime.Sandbox{}, errs.New("the sandbox %s has no plan", s.Project)
	}
	base := strings.TrimSuffix(s.Files[0], ".json")
	return runtime.Sandbox{
		Project:   p.Project,
		Dir:       s.Dir,
		Files:     []string{base + ".forward", base + ".forward.json"},
		Processes: true,
	}, nil
}

// Down stops the port-forward and deletes the namespace, with
// everything in it. The cluster stays, for the next sandbox.
func (r Runtime) Down(ctx context.Context, s runtime.Sandbox, stdout, stderr io.Writer) error {
	p, err := r.plan(s)
	if err != nil {
		return err
	}
	if fs, err := forwardSandbox(s, p); err == nil {
		if err := r.Forward.Down(ctx, fs, stdout, stderr); err != nil {
			return err
		}
		for _, f := range fs.Files {
			if err := os.Remove(f); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	// Without the cluster there is nothing in it to delete.
	if exists, err := r.cluster(p).Exists(ctx); err != nil || !exists {
		return nil
	}
	del := proc.Command{Name: "kubectl", Args: append(p.ClusterFlags(),
		"delete", "namespace", p.Namespace, "--ignore-not-found", "--wait", "--timeout", "180s")}
	if err := r.Runner.Stream(ctx, del, stdout, stderr); err != nil {
		return errs.Wrap(err, "cannot delete the namespace %s", p.Namespace)
	}
	return nil
}

// Services are the manifests' workloads.
func (r Runtime) Services(ctx context.Context, s runtime.Sandbox) ([]string, error) {
	workloads, err := r.workloads(ctx, s)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(workloads))
	for _, w := range workloads {
		out = append(out, w.Name)
	}
	return out, nil
}

func (r Runtime) workloads(ctx context.Context, s runtime.Sandbox) ([]Workload, error) {
	p, err := r.plan(s)
	if err != nil {
		return nil, err
	}
	rendered, err := Render(ctx, r.Runner, p.Kustomization)
	if err != nil {
		return nil, err
	}
	return Workloads(rendered)
}

// Target is a workload of the sandbox as kubectl names it, the web
// workload's for no name, with the plan that points kubectl at it.
func (r Runtime) Target(ctx context.Context, s runtime.Sandbox, service string) (Plan, string, error) {
	return r.ref(ctx, s, service)
}

// ref is how kubectl names a workload of the sandbox.
func (r Runtime) ref(ctx context.Context, s runtime.Sandbox, service string) (Plan, string, error) {
	p, err := r.plan(s)
	if err != nil {
		return Plan{}, "", err
	}
	if service == "" {
		service = p.Web
	}
	workloads, err := r.workloads(ctx, s)
	if err != nil {
		return Plan{}, "", err
	}
	w, ok := find(workloads, service)
	if !ok {
		return Plan{}, "", errs.New("the sandbox has no workload %s", service).
			WithHint("it has %s", names(workloads))
	}
	return p, w.Ref(), nil
}

// Port is the port the web workload is forwarded to.
func (r Runtime) Port(_ context.Context, s runtime.Sandbox, service string, _ int) (string, error) {
	p, err := r.plan(s)
	if err != nil {
		return "", err
	}
	if service != p.Web {
		return "", errs.New("only %s is forwarded to this machine, not %s", p.Web, service)
	}
	return strconv.Itoa(p.Port), nil
}

// Logs is the tail of a workload's output.
func (r Runtime) Logs(ctx context.Context, s runtime.Sandbox, service string, tail int) ([]byte, error) {
	p, ref, err := r.ref(ctx, s, service)
	if err != nil {
		return nil, err
	}
	out, err := r.Runner.Output(ctx, kubectl(p, "logs", ref, "--tail", strconv.Itoa(tail)))
	if err != nil {
		return nil, errs.Wrap(err, "cannot read the logs of %s", service)
	}
	return out, nil
}

// LogsSince is what a workload wrote since a moment, with when.
func (r Runtime) LogsSince(ctx context.Context, s runtime.Sandbox, service string, since time.Time) ([]runtime.LogLine, error) {
	p, ref, err := r.ref(ctx, s, service)
	if err != nil {
		return nil, err
	}
	args := []string{"logs", ref, "--timestamps"}
	if !since.IsZero() {
		args = append(args, "--since-time", since.UTC().Format(time.RFC3339))
	}
	out, err := r.Runner.Output(ctx, kubectl(p, args...))
	if err != nil {
		return nil, errs.Wrap(err, "cannot read the logs of %s", service)
	}
	return timestamped(out, since), nil
}

// timestamped reads kubectl's --timestamps lines. --since-time is to
// the second, so what came earlier in that second is dropped here.
func timestamped(out []byte, since time.Time) []runtime.LogLine {
	var lines []runtime.LogLine
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		stamp, text, ok := strings.Cut(sc.Text(), " ")
		if !ok {
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, stamp)
		if err != nil || at.Before(since) {
			continue
		}
		lines = append(lines, runtime.LogLine{At: at, Text: text})
	}
	return lines
}

// Status is what each workload is doing: running when every replica it
// wants is ready, starting while some are not, stopped with none.
func (r Runtime) Status(ctx context.Context, s runtime.Sandbox) ([]runtime.Status, error) {
	p, err := r.plan(s)
	if err != nil {
		return nil, err
	}
	if exists, err := r.cluster(p).Exists(ctx); err != nil || !exists {
		// No cluster, nothing running: the sandbox is gone.
		return nil, nil
	}
	out, err := r.Runner.Output(ctx, kubectl(p, "get", "deployments,statefulsets,daemonsets", "--output", "json"))
	if err != nil {
		return nil, errs.Wrap(err, "cannot ask the cluster about %s", p.Namespace)
	}
	return statuses(out)
}

func statuses(out []byte) ([]runtime.Status, error) {
	var list struct {
		Items []struct {
			Kind     string `json:"kind"`
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Spec struct {
				Replicas *int `json:"replicas"`
			} `json:"spec"`
			Status struct {
				ReadyReplicas int `json:"readyReplicas"`
				// A DaemonSet's.
				Desired     int `json:"desiredNumberScheduled"`
				NumberReady int `json:"numberReady"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(out, &list); err != nil {
		return nil, errs.Wrap(err, "cannot read what the cluster said")
	}
	var st []runtime.Status
	for _, it := range list.Items {
		want, ready := 1, it.Status.ReadyReplicas
		if it.Spec.Replicas != nil {
			want = *it.Spec.Replicas
		}
		if it.Kind == "DaemonSet" {
			want, ready = it.Status.Desired, it.Status.NumberReady
		}
		state := "running"
		switch {
		case want == 0:
			state = "stopped"
		case ready < want:
			state = "starting"
		}
		kind := workloadKinds[it.Kind]
		st = append(st, runtime.Status{Service: it.Metadata.Name, Container: kind + "/" + it.Metadata.Name, State: state})
	}
	return st, nil
}

// errNoPause is why a Kubernetes sandbox cannot be paused.
var errNoPause = errs.New("Kubernetes cannot pause a workload").
	WithHint("pit saves and restores the data of compose projects only")

// Pause is not something Kubernetes does.
func (Runtime) Pause(context.Context, runtime.Sandbox, []string) error { return errNoPause }

// Unpause is not either.
func (Runtime) Unpause(context.Context, runtime.Sandbox, []string) error { return errNoPause }

// Stop scales the named workloads to nothing.
func (r Runtime) Stop(ctx context.Context, s runtime.Sandbox, services []string) error {
	for _, svc := range services {
		p, ref, err := r.ref(ctx, s, svc)
		if err != nil {
			return err
		}
		if _, err := r.Runner.Output(ctx, kubectl(p, "scale", ref, "--replicas", "0")); err != nil {
			return errs.Wrap(err, "cannot stop %s", svc)
		}
	}
	return nil
}

// WaitReady waits for the web workload to roll out, forwards its port,
// and asks its URL until it answers. It stops early when its pods
// cannot start: an image that cannot be pulled, a container that keeps
// crashing.
func (r Runtime) WaitReady(ctx context.Context, s runtime.Sandbox, service string, pr runtime.Probe) error {
	p, ref, err := r.ref(ctx, s, service)
	if err != nil {
		return err
	}
	gone := func() error { return r.stuck(ctx, p, ref, service) }
	started := time.Now()
	if err := r.rolledOut(ctx, p, ref, pr, gone); err != nil {
		return err
	}
	if err := r.forward(ctx, s, p, ref); err != nil {
		return err
	}
	pr.Timeout -= time.Since(started)
	attempts, err := runtime.Poll(ctx, pr, gone)
	var late runtime.TimedOut
	if errors.As(err, &late) {
		msg := runtime.NotReady(service, pr, attempts, late.Last)
		if logs, lerr := r.Runner.Output(ctx, kubectl(p, "logs", ref, "--tail", "30")); lerr == nil {
			if tail := strings.TrimSpace(string(logs)); tail != "" {
				msg += "\n\nthe last output from " + service + ":\n" + runtime.IndentLines(tail)
			}
		}
		return errs.New("%s", msg).
			WithHint("`pit logs` shows all of it; `kubectl --kubeconfig %s --context %s -n %s get pods` what the pods are doing", p.Kubeconfig, p.Context, p.Namespace)
	}
	return err
}

// rolledOut waits until every pod of the workload runs the manifests as
// they were applied last, within the probe's time.
func (r Runtime) rolledOut(ctx context.Context, p Plan, ref string, pr runtime.Probe, gone func() error) error {
	deadline := time.Now().Add(pr.Timeout)
	for {
		out, err := r.Runner.Output(ctx, kubectl(p, "rollout", "status", ref, "--watch=false"))
		if err == nil && strings.Contains(string(out), "successfully rolled out") {
			return nil
		}
		if err := gone(); err != nil {
			return err
		}
		if time.Now().After(deadline) {
			last := strings.TrimSpace(string(out))
			if err != nil {
				last = err.Error()
			}
			return errs.New("%s did not roll out within %v: %s", ref, pr.Timeout, last).
				WithHint("`kubectl --kubeconfig %s --context %s -n %s get pods` shows what its pods are doing", p.Kubeconfig, p.Context, p.Namespace)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pr.Interval):
		}
	}
}

// stuck are the reasons a pod gives for not starting that waiting does
// not cure.
var stuck = []string{"CrashLoopBackOff", "ImagePullBackOff", "ErrImagePull", "CreateContainerConfigError", "InvalidImageName", "CreateContainerError"}

// stuck says why the workload's pods cannot start, if they cannot.
func (r Runtime) stuck(ctx context.Context, p Plan, ref, service string) error {
	sel, err := r.Runner.Output(ctx, kubectl(p, "get", ref, "--output", "jsonpath={.spec.selector.matchLabels}"))
	if err != nil {
		return nil // asked again on the next attempt
	}
	var labels map[string]string
	if json.Unmarshal(sel, &labels) != nil || len(labels) == 0 {
		return nil
	}
	pairs := make([]string, 0, len(labels))
	for k, v := range labels {
		pairs = append(pairs, k+"="+v)
	}
	slices.Sort(pairs)
	out, err := r.Runner.Output(ctx, kubectl(p, "get", "pods", "--selector", strings.Join(pairs, ","), "--output", "json"))
	if err != nil {
		return nil
	}
	reason, message, crashed := waiting(out)
	if reason == "" {
		return nil
	}
	msg := fmt.Sprintf("%s cannot start: %s", service, reason)
	if message != "" {
		msg += " (" + message + ")"
	}
	if crashed != "" {
		if logs, err := r.Runner.Output(ctx, kubectl(p, "logs", "pod/"+crashed, "--previous", "--tail", "30")); err == nil {
			if tail := strings.TrimSpace(string(logs)); tail != "" {
				msg += "\n\nthe last output from " + service + ":\n" + runtime.IndentLines(tail)
			}
		}
	}
	return errs.New("%s", msg).WithHint("`pit logs` shows what it wrote; the manifests say what it runs")
}

// waiting finds a container waiting for a reason in stuck, and the pod
// of one that crashed.
func waiting(out []byte) (reason, message, crashedPod string) {
	var pods struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Status struct {
				InitContainerStatuses []containerStatus `json:"initContainerStatuses"`
				ContainerStatuses     []containerStatus `json:"containerStatuses"`
			} `json:"status"`
		} `json:"items"`
	}
	if json.Unmarshal(out, &pods) != nil {
		return "", "", ""
	}
	for _, pod := range pods.Items {
		for _, c := range slices.Concat(pod.Status.InitContainerStatuses, pod.Status.ContainerStatuses) {
			w := c.State.Waiting
			if w == nil || !slices.Contains(stuck, w.Reason) {
				continue
			}
			if w.Reason == "CrashLoopBackOff" {
				crashedPod = pod.Metadata.Name
			}
			return w.Reason, w.Message, crashedPod
		}
	}
	return "", "", ""
}

type containerStatus struct {
	State struct {
		Waiting *struct {
			Reason  string `json:"reason"`
			Message string `json:"message"`
		} `json:"waiting"`
	} `json:"state"`
}

// only keeps the named workloads of rendered manifests, and everything
// that is not a workload: a review of four services of eight runs four.
func only(rendered []byte, services []string) ([]byte, error) {
	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	dec := yaml.NewDecoder(bytes.NewReader(rendered))
	for {
		var doc yaml.Node
		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, errs.Wrap(err, "cannot read the rendered manifests")
		}
		var head struct {
			Kind     string `yaml:"kind"`
			Metadata struct {
				Name string `yaml:"name"`
			} `yaml:"metadata"`
		}
		if err := doc.Decode(&head); err != nil {
			return nil, errs.Wrap(err, "cannot read the rendered manifests")
		}
		if _, workload := workloadKinds[head.Kind]; workload && !slices.Contains(services, head.Metadata.Name) {
			continue
		}
		if err := enc.Encode(&doc); err != nil {
			return nil, err
		}
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func find(workloads []Workload, name string) (Workload, bool) {
	for _, w := range workloads {
		if w.Name == name {
			return w, true
		}
	}
	return Workload{}, false
}

func names(workloads []Workload) string {
	if len(workloads) == 0 {
		return "none"
	}
	out := make([]string, 0, len(workloads))
	for _, w := range workloads {
		out = append(out, w.Name)
	}
	return strings.Join(out, ", ")
}

// shellQuote quotes a word for sh.
func shellQuote(s string) string {
	plain := func(r rune) bool {
		return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_./=:,@", r)
	}
	if s != "" && strings.IndexFunc(s, func(r rune) bool { return !plain(r) }) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
