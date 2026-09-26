//go:build unix

package local

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"sync"
	"syscall"
	"time"

	"github.com/thannoz/pit/internal/errs"
)

// The files of a sandbox's directory.
const (
	runFile        = "run.json"
	statusFile     = "status.json"
	supervisorFile = "supervisor.pid"
	logSuffix      = ".log"
)

// run is what the runner hands the supervisor: where, and what.
type run struct {
	Dir       string       `json:"dir"`
	Processes []runProcess `json:"processes"`
	// Grace is the runner's, which the supervisor, a process of its
	// own, does not otherwise know.
	Grace time.Duration `json:"grace,omitempty"`
}

type runProcess struct {
	Name    string   `json:"name"`
	Command string   `json:"command"`
	Port    int      `json:"port"`
	Env     []string `json:"env"`
	Wrap    []string `json:"wrap,omitempty"`
}

// state is what the supervisor says about each process.
type state struct {
	Processes map[string]procState `json:"processes"`
}

type procState struct {
	PID     int       `json:"pid"`
	Running bool      `json:"running"`
	Exit    int       `json:"exit"`
	Started time.Time `json:"started"`
	Ended   time.Time `json:"ended,omitzero"`
	// Error says why it did not start.
	Error string `json:"error,omitempty"`
}

// Grace is how long a process has to end after it is asked to.
var Grace = 10 * time.Second

// Supervise runs the processes the runner put in dir, each in a process
// group of its own, and writes down what they print and when they end.
// It returns once all of them have ended, or once it is asked to stop
// and has stopped them. It is what `pit` runs, detached, for a sandbox
// of processes: pit itself does not stay.
func Supervise(ctx context.Context, dir string) error {
	var r run
	data, err := os.ReadFile(filepath.Join(dir, runFile))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return err
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	defer signal.Stop(stop)

	s := &supervisor{dir: dir, state: state{Processes: map[string]procState{}}}
	var wg sync.WaitGroup
	var started []*exec.Cmd
	for _, p := range r.Processes {
		cmd, err := s.start(r.Dir, p)
		if err != nil {
			s.set(p.Name, procState{Exit: -1, Error: err.Error(), Started: time.Now(), Ended: time.Now()})
			continue
		}
		started = append(started, cmd)
		wg.Add(1)
		go func(name string, cmd *exec.Cmd) {
			defer wg.Done()
			_ = cmd.Wait()
			s.ended(name, cmd.ProcessState)
		}(p.Name, cmd)
	}
	if err := s.save(); err != nil {
		return err
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
	case <-stop:
	}
	for _, cmd := range started {
		_ = signalGroup(cmd.Process.Pid, syscall.SIGTERM)
	}
	grace := Grace
	if r.Grace > 0 {
		grace = r.Grace
	}
	select {
	case <-done:
	case <-time.After(grace):
		for _, cmd := range started {
			_ = signalGroup(cmd.Process.Pid, syscall.SIGKILL)
		}
		<-done
	}
	return nil
}

type supervisor struct {
	dir   string
	mu    sync.Mutex
	state state
	// saving lets one save at a time write the file: a process that
	// ends while the state is saved for another reason would otherwise
	// rename the other's half-written file away, or its own older state
	// over the newer.
	saving sync.Mutex
}

func (s *supervisor) start(dir string, p runProcess) (*exec.Cmd, error) {
	// Go would say /bin/sh was not there.
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return nil, errs.New("there is no directory %s to run it in", dir)
	}
	log, err := os.OpenFile(filepath.Join(s.dir, p.Name+logSuffix), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	w := &lineWriter{out: log}
	argv := append(slices.Clone(p.Wrap), "/bin/sh", "-c", p.Command)
	cmd := exec.Command(argv[0], argv[1:]...) //nolint:gosec,noctx // the Procfile's command, which is the point; it outlives any context
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), p.Env...)
	cmd.Stdout, cmd.Stderr = w, w
	cmd.SysProcAttr = ownGroup()
	// A process that leaves children holding its output behind would
	// otherwise keep Wait from returning.
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Start(); err != nil {
		_ = log.Close()
		return nil, err
	}
	s.set(p.Name, procState{PID: cmd.Process.Pid, Running: true, Started: time.Now()})
	return cmd, nil
}

func (s *supervisor) ended(name string, ps *os.ProcessState) {
	s.mu.Lock()
	st := s.state.Processes[name]
	st.Running, st.Exit, st.Ended = false, exitCode(ps), time.Now()
	s.state.Processes[name] = st
	s.mu.Unlock()
	_ = s.save()
}

func (s *supervisor) set(name string, st procState) {
	s.mu.Lock()
	s.state.Processes[name] = st
	s.mu.Unlock()
}

// save writes the state where the runner reads it, whole or not at
// all.
func (s *supervisor) save() error {
	s.saving.Lock()
	defer s.saving.Unlock()
	s.mu.Lock()
	data, err := json.MarshalIndent(s.state, "", "  ")
	s.mu.Unlock()
	if err != nil {
		return err
	}
	tmp := filepath.Join(s.dir, statusFile+".tmp")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(s.dir, statusFile))
}

// exitCode is a process's exit status, or 128 and the signal that ended
// it, as a shell says it.
func exitCode(ps *os.ProcessState) int {
	if ps == nil {
		return -1
	}
	if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return ps.ExitCode()
}

// lineWriter writes what a process prints, a line at a time, each with
// when it was printed.
type lineWriter struct {
	mu      sync.Mutex
	out     *os.File
	partial []byte
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.partial = append(w.partial, p...)
	for {
		i := bytes.IndexByte(w.partial, '\n')
		if i < 0 {
			break
		}
		line := bytes.TrimRight(w.partial[:i], "\r")
		stamp := time.Now().UTC().Format(time.RFC3339Nano)
		if _, err := w.out.Write(append(append([]byte(stamp+" "), line...), '\n')); err != nil {
			return 0, errs.Wrap(err, "cannot write the log")
		}
		w.partial = w.partial[i+1:]
	}
	return len(p), nil
}
