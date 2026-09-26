//go:build unix

package local

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/runtime"
)

// Runner runs a sandbox's processes. A sandbox's Files are its Procfile
// and its plan, in that order.
type Runner struct {
	// Root is where each sandbox's processes keep their logs and state,
	// in a directory named after its project.
	Root string
	// Supervisor is the command that runs Supervise for a directory,
	// which is appended to it: pit itself, with a hidden command.
	Supervisor []string
}

var _ runtime.Runtime = Runner{}

// startWait is how long the processes have to be started.
const startWait = 10 * time.Second

func (r Runner) dir(s runtime.Sandbox) string { return filepath.Join(r.Root, s.Project) }

// read is a sandbox's Procfile and plan.
func (r Runner) read(s runtime.Sandbox) ([]Process, Plan, error) {
	if len(s.Files) < 2 {
		return nil, Plan{}, errs.New("the sandbox %s has no Procfile and plan", s.Project)
	}
	procs, err := ReadProcfile(s.Files[0])
	if err != nil {
		return nil, Plan{}, err
	}
	plan, err := ReadPlan(s.Files[1])
	return procs, plan, err
}

// Up starts the named processes, or all of them, anew: whatever of the
// sandbox was running stops first, because what it runs is the code
// the worktree held when it started.
func (r Runner) Up(ctx context.Context, s runtime.Sandbox, services []string, stdout, _ io.Writer) error {
	procs, plan, err := r.read(s)
	if err != nil {
		return err
	}
	chosen := procs
	if len(services) > 0 {
		chosen = nil
		for _, name := range services {
			i := slices.IndexFunc(procs, func(p Process) bool { return p.Name == name })
			if i < 0 {
				return errs.New("the Procfile has no process %q", name)
			}
			chosen = append(chosen, procs[i])
		}
	}

	dir := r.dir(s)
	if err := r.stop(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return errs.Wrap(err, "cannot create %s", dir)
	}
	for _, f := range []string{statusFile, supervisorFile} {
		_ = os.Remove(filepath.Join(dir, f))
	}
	paused, _ := filepath.Glob(filepath.Join(dir, "*.paused"))
	for _, f := range paused {
		_ = os.Remove(f)
	}

	job := run{Dir: s.Dir, Grace: Grace}
	for _, p := range chosen {
		job.Processes = append(job.Processes, runProcess{
			Name: p.Name, Command: p.Command,
			Port: plan.PortOf(procs, p.Name),
			Env:  plan.Environment(procs, p.Name),
		})
	}
	data, err := json.MarshalIndent(job, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, runFile), data, 0o600); err != nil {
		return errs.Wrap(err, "cannot write %s", dir)
	}

	if len(r.Supervisor) == 0 {
		return errs.New("pit does not know how to start its supervisor")
	}
	out, err := os.OpenFile(filepath.Join(dir, "supervisor.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer out.Close()                                                                    //nolint:errcheck // the supervisor has its own copy
	cmd := exec.Command(r.Supervisor[0], append(slices.Clone(r.Supervisor[1:]), dir)...) //nolint:gosec,noctx // it has to outlive pit
	cmd.Stdout, cmd.Stderr = out, out
	cmd.SysProcAttr = ownSession()
	if err := cmd.Start(); err != nil {
		return errs.Wrap(err, "cannot start the processes")
	}
	// Reaped when it ends, if pit is still there to see it.
	go func() { _ = cmd.Wait() }()
	pid := cmd.Process.Pid
	if err := os.WriteFile(filepath.Join(dir, supervisorFile), []byte(strconv.Itoa(pid)+"\n"), 0o600); err != nil {
		return err
	}

	deadline := time.Now().Add(startWait)
	for {
		st, err := readState(dir)
		if err == nil && len(st.Processes) == len(chosen) {
			for _, p := range job.Processes {
				ps := st.Processes[p.Name]
				if ps.Error != "" {
					return errs.New("%s did not start: %s", p.Name, ps.Error)
				}
				_, _ = fmt.Fprintf(stdout, " %s started on port %d (pid %d)\n", p.Name, p.Port, ps.PID)
			}
			return nil
		}
		if !alive(pid) || time.Now().After(deadline) {
			tail, _ := os.ReadFile(filepath.Join(dir, "supervisor.log"))
			return errs.New("the processes did not start: %s", strings.TrimSpace(lastLines(string(tail), 5)))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// stop ends a sandbox's supervisor, and with it its processes, when
// there is one.
func (r Runner) stop(dir string) error {
	pid, ok := supervisorOf(dir)
	if !ok {
		return nil
	}
	_ = signalProcess(pid, syscall.SIGTERM)
	deadline := time.Now().Add(Grace + 5*time.Second)
	for alive(pid) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if !alive(pid) {
		return nil
	}
	// It did not stop them in time: everything it started goes.
	_ = signalProcess(pid, syscall.SIGKILL)
	if st, err := readState(dir); err == nil {
		for _, ps := range st.Processes {
			if ps.Running {
				_ = signalGroup(ps.PID, syscall.SIGKILL)
			}
		}
	}
	return nil
}

// supervisorOf is the supervisor running for dir: the recorded one, if
// it is still running and still is one. After a reboot the number can
// belong to anything.
func supervisorOf(dir string) (int, bool) {
	data, err := os.ReadFile(filepath.Join(dir, supervisorFile))
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || !alive(pid) {
		return 0, false
	}
	out, err := exec.Command("ps", "-o", "command=", "-p", strconv.Itoa(pid)).Output() //nolint:noctx // a quick question
	if err != nil || !strings.Contains(string(out), dir) {
		return 0, false
	}
	return pid, true
}

func readState(dir string) (state, error) {
	var st state
	data, err := os.ReadFile(filepath.Join(dir, statusFile))
	if err != nil {
		return st, err
	}
	err = json.Unmarshal(data, &st)
	return st, err
}

// running is the state, with a process counted as running only while
// its supervisor is: without it, nothing the state says is running is.
func (r Runner) running(s runtime.Sandbox) (state, bool, error) {
	dir := r.dir(s)
	st, err := readState(dir)
	if err != nil {
		return st, false, err
	}
	_, up := supervisorOf(dir)
	if !up {
		for name, ps := range st.Processes {
			if ps.Running {
				ps.Running, ps.Exit = false, -1
				st.Processes[name] = ps
			}
		}
	}
	return st, up, nil
}

// Build has nothing to build: processes run from the worktree.
func (Runner) Build(context.Context, runtime.Sandbox, []string, io.Writer, io.Writer) error {
	return nil
}

// Pull has no images to pull.
func (Runner) Pull(_ context.Context, _ runtime.Sandbox, image string, _, _ io.Writer) error {
	return errs.New("processes run no images, and %s is one", image)
}

// Down stops the processes and takes away their logs.
func (r Runner) Down(_ context.Context, s runtime.Sandbox, _, _ io.Writer) error {
	dir := r.dir(s)
	if err := r.stop(dir); err != nil {
		return err
	}
	if err := os.RemoveAll(dir); err != nil {
		return errs.Wrap(err, "cannot remove %s", dir)
	}
	return nil
}

// Services are the Procfile's processes.
func (r Runner) Services(_ context.Context, s runtime.Sandbox) ([]string, error) {
	if len(s.Files) == 0 {
		return nil, errs.New("the sandbox %s has no Procfile", s.Project)
	}
	procs, err := ReadProcfile(s.Files[0])
	if err != nil {
		return nil, err
	}
	names := make([]string, len(procs))
	for i, p := range procs {
		names[i] = p.Name
	}
	return names, nil
}

// Port is the port a process was given.
func (r Runner) Port(_ context.Context, s runtime.Sandbox, service string, _ int) (string, error) {
	procs, plan, err := r.read(s)
	if err != nil {
		return "", err
	}
	if port := plan.PortOf(procs, service); port > 0 {
		return strconv.Itoa(port), nil
	}
	return "", errs.New("the Procfile has no process %q", service)
}

// Logs is the end of what a process printed.
func (r Runner) Logs(_ context.Context, s runtime.Sandbox, service string, tail int) ([]byte, error) {
	lines, err := r.lines(s, service)
	if err != nil {
		return nil, err
	}
	if tail > 0 && len(lines) > tail {
		lines = lines[len(lines)-tail:]
	}
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l.Text + "\n")
	}
	return []byte(b.String()), nil
}

// LogsSince is what a process printed since a moment.
func (r Runner) LogsSince(_ context.Context, s runtime.Sandbox, service string, since time.Time) ([]runtime.LogLine, error) {
	lines, err := r.lines(s, service)
	if err != nil {
		return nil, err
	}
	var out []runtime.LogLine
	for _, l := range lines {
		if !l.At.Before(since) {
			out = append(out, l)
		}
	}
	return out, nil
}

func (r Runner) lines(s runtime.Sandbox, service string) ([]runtime.LogLine, error) {
	f, err := os.Open(filepath.Join(r.dir(s), service+logSuffix))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close() //nolint:errcheck // read only
	return parseLog(f), nil
}

// parseLog reads a log the supervisor wrote.
func parseLog(r io.Reader) []runtime.LogLine {
	var out []runtime.LogLine
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		stamp, text, _ := strings.Cut(sc.Text(), " ")
		at, err := time.Parse(time.RFC3339Nano, stamp)
		if err != nil {
			continue
		}
		out = append(out, runtime.LogLine{At: at, Text: text})
	}
	return out
}

// Status says what each process is doing, in Docker's words.
func (r Runner) Status(_ context.Context, s runtime.Sandbox) ([]runtime.Status, error) {
	st, _, err := r.running(s)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(st.Processes))
	for n := range st.Processes {
		names = append(names, n)
	}
	slices.Sort(names)
	out := make([]runtime.Status, 0, len(names))
	for _, n := range names {
		ps := st.Processes[n]
		status := runtime.Status{Service: n, Container: n, State: "exited", ExitCode: ps.Exit}
		if ps.Running {
			status.State, status.ExitCode = "running", 0
			if _, err := os.Stat(filepath.Join(r.dir(s), n+".paused")); err == nil {
				status.State = "paused"
			}
		}
		out = append(out, status)
	}
	return out, nil
}

// signal sends a signal to the named running processes, and reports
// which it reached.
func (r Runner) signal(s runtime.Sandbox, services []string, sig syscall.Signal) ([]string, error) {
	st, up, err := r.running(s)
	if err != nil {
		return nil, err
	}
	if !up {
		return nil, errs.New("the processes of %s are not running", s.Project)
	}
	var reached []string
	for _, name := range services {
		ps, ok := st.Processes[name]
		if !ok || !ps.Running {
			continue
		}
		if err := signalGroup(ps.PID, sig); err != nil {
			return reached, errs.Wrap(err, "cannot signal %s", name)
		}
		reached = append(reached, name)
	}
	return reached, nil
}

// Pause freezes the named processes, and whatever they started.
func (r Runner) Pause(_ context.Context, s runtime.Sandbox, services []string) error {
	reached, err := r.signal(s, services, syscall.SIGSTOP)
	for _, n := range reached {
		_ = os.WriteFile(filepath.Join(r.dir(s), n+".paused"), nil, 0o600)
	}
	return err
}

// Unpause lets them carry on.
func (r Runner) Unpause(_ context.Context, s runtime.Sandbox, services []string) error {
	reached, err := r.signal(s, services, syscall.SIGCONT)
	for _, n := range reached {
		_ = os.Remove(filepath.Join(r.dir(s), n+".paused"))
	}
	return err
}

// Stop ends the named processes and waits until they have ended.
func (r Runner) Stop(ctx context.Context, s runtime.Sandbox, services []string) error {
	reached, err := r.signal(s, services, syscall.SIGTERM)
	if err != nil {
		return err
	}
	// A paused process takes no TERM until it runs again.
	_, _ = r.signal(s, reached, syscall.SIGCONT)
	deadline := time.Now().Add(Grace)
	for {
		st, _, err := r.running(s)
		if err != nil {
			return err
		}
		still := slices.ContainsFunc(reached, func(n string) bool { return st.Processes[n].Running })
		if !still {
			return nil
		}
		if time.Now().After(deadline) {
			_, _ = r.signal(s, reached, syscall.SIGKILL)
			deadline = time.Now().Add(Grace)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// WaitReady asks the web process's address until it answers, and stops
// asking as soon as the process has ended: it will not answer then.
func (r Runner) WaitReady(ctx context.Context, s runtime.Sandbox, service string, p runtime.Probe) error {
	gone := func() error {
		st, _, err := r.running(s)
		if err != nil {
			return nil
		}
		if ps, ok := st.Processes[service]; ok && !ps.Running {
			return errs.New("%s ended with code %d before it answered%s", service, ps.Exit, r.quote(ctx, s, service))
		}
		return nil
	}
	attempts, err := runtime.Poll(ctx, p, gone)
	var late runtime.TimedOut
	if errors.As(err, &late) {
		return errs.New("%s%s", runtime.NotReady(service, p, attempts, late.Last), r.quote(ctx, s, service)).
			WithHint("look at %s, or run `pit logs`", p.URL)
	}
	if err != nil && ctx.Err() == nil {
		return errs.Hinted(err, "`pit logs` shows all it printed")
	}
	return err
}

// quote is the end of what a process printed, for a message.
func (r Runner) quote(ctx context.Context, s runtime.Sandbox, service string) string {
	logs, err := r.Logs(ctx, s, service, 30)
	if err != nil {
		return ""
	}
	tail := strings.TrimSpace(string(logs))
	if tail == "" {
		return ""
	}
	return "\n\nthe last output from " + service + ":\n" + runtime.IndentLines(tail)
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
