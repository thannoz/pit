package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/thannoz/pit/internal/runtime"
	"github.com/thannoz/pit/internal/runtime/local"
	"github.com/thannoz/pit/internal/runtime/runtimetest"
	"github.com/thannoz/pit/internal/state"
)

// processBox is a recorded sandbox of processes, with its Procfile and
// plan where the record says.
func processBox(t *testing.T) state.Sandbox {
	t.Helper()
	id := atRepo(t, "github.com", "acme", "shop")
	box := recorded(7, id.String(), id.Ref(), "feature", time.Minute)
	box.Processes, box.WebService = true, "web"
	box.Worktree = t.TempDir()
	procfile := filepath.Join(box.Worktree, "Procfile")
	if err := os.WriteFile(procfile, []byte("web: node index.js\nworker: node jobs.js\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	plan := filepath.Join(t.TempDir(), "pr-7.processes.json")
	if err := local.WritePlan(plan, local.Plan{Project: box.Project, Web: "web", Port: 40007, Env: map[string]string{"GREETING": "hi"}}); err != nil {
		t.Fatal(err)
	}
	box.ComposeFiles = []string{procfile, plan}
	return box
}

func TestLogsOfProcesses(t *testing.T) {
	box := processBox(t)
	_, fake := withManager(t, box)
	now := time.Now()
	fake.ServiceLines = map[string][]runtime.LogLine{
		"web":    {{At: now.Add(-3 * time.Second), Text: "one"}, {At: now.Add(-2 * time.Second), Text: "two"}, {At: now.Add(-time.Second), Text: "three"}},
		"worker": {{At: now, Text: "busy"}},
	}
	out, err := runCLI(t, "logs", "7", "--tail", "2")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "web | two\nweb | three\n") || strings.Contains(out, "one") {
		t.Errorf("out = %q", out)
	}
	if out, _ := runCLI(t, "logs", "7", "worker"); !strings.Contains(out, "worker | busy") || strings.Contains(out, "three") {
		t.Errorf("out = %q", out)
	}
	for _, c := range fake.Calls() {
		if c.Method == "Logs" {
			t.Errorf("went through compose: %+v", c)
		}
	}
}

// growing is a runtime whose process goes on printing.
type growing struct {
	*runtimetest.Fake
	mu    sync.Mutex
	lines []runtime.LogLine
}

func (g *growing) LogsSince(context.Context, runtime.Sandbox, string, time.Time) ([]runtime.LogLine, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]runtime.LogLine{}, g.lines...), nil
}

func (g *growing) print(text string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.lines = append(g.lines, runtime.LogLine{At: time.Now(), Text: text})
}

func TestLogsOfProcessesFollow(t *testing.T) {
	box := processBox(t)
	m, _ := withManager(t, box)
	g := &growing{Fake: runtimetest.New("web")}
	g.print("first")
	m.Runtime = g
	previous := followEvery
	followEvery = 5 * time.Millisecond
	t.Cleanup(func() { followEvery = previous })

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	c := &cobra.Command{}
	c.SetContext(ctx)
	var out syncBuffer
	c.SetOut(&out)
	done := make(chan error, 1)
	go func() { done <- processLogs(c, box, "web", &logsOptions{follow: true, tail: 10}) }()
	g.print("second")
	g.print("third")
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(out.String(), "web | third") && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	if err := <-done; err == nil || ctx.Err() == nil {
		t.Errorf("err = %v", err)
	}
	if got := out.String(); got != "web | first\nweb | second\nweb | third\n" {
		t.Errorf("out = %q", got)
	}
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestShellInProcesses(t *testing.T) {
	box := processBox(t)
	withManager(t, box)
	if _, err := runCLI(t, "shell", "7", "--", "sh", "-c", "echo $PORT $PIT_PROJECT $GREETING $PWD > shell.txt"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(box.Worktree, "shell.txt"))
	dir, _ := filepath.EvalSymlinks(box.Worktree)
	if err != nil || !strings.HasPrefix(string(got), "40007 "+box.Project+" hi ") || !strings.Contains(string(got), filepath.Base(dir)) {
		t.Errorf("shell.txt = %q, %v", got, err)
	}
	if _, err := runCLI(t, "shell", "7", "--", "sh", "-c", "exit 3"); err == nil || !strings.Contains(err.Error()+" ", "3") {
		t.Errorf("err = %v", err)
	}
}

// pit runs itself, with a hidden command, to keep a sandbox's processes
// running once it is gone.
func TestSuperviseRunsWhatItIsGiven(t *testing.T) {
	dir, work := t.TempDir(), t.TempDir()
	job := `{"dir": ` + strconvQuote(work) + `, "processes": [{"name": "web", "command": "echo $GREETING > out.txt", "env": ["GREETING=hi"]}]}`
	if err := os.WriteFile(filepath.Join(dir, "run.json"), []byte(job), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, superviseCommand, dir); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(work, "out.txt")); err != nil || string(got) != "hi\n" {
		t.Errorf("out.txt = %q, %v", got, err)
	}
	if status, err := os.ReadFile(filepath.Join(dir, "status.json")); err != nil || !strings.Contains(string(status), `"running": false`) {
		t.Errorf("status = %s, %v", status, err)
	}
	// Nobody is meant to type it.
	if out, _ := runCLI(t, "--help"); strings.Contains(out, superviseCommand) {
		t.Errorf("the help shows it:\n%s", out)
	}
}

func strconvQuote(s string) string { return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"` }
