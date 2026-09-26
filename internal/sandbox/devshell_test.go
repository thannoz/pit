package sandbox_test

import (
	"os"
	"path/filepath"
	goruntime "runtime"
	"slices"
	"strings"
	"testing"

	"github.com/thannoz/pit/internal/data"
	"github.com/thannoz/pit/internal/data/datatest"
	"github.com/thannoz/pit/internal/runtime"
	"github.com/thannoz/pit/internal/runtime/local"
	"github.com/thannoz/pit/internal/runtime/runtimetest"
	"github.com/thannoz/pit/internal/sandbox"
)

// fakeShell stands in for nix and devenv: it notes how it was called,
// and runs the command it was given with IN_DEV_SHELL set, as a dev
// shell gives its tools. It returns the file of notes.
func fakeShell(t *testing.T) string {
	t.Helper()
	if goruntime.GOOS == "windows" {
		t.Skip("skipping: the stand-in for nix is a shell script")
	}
	bin, log := t.TempDir(), filepath.Join(t.TempDir(), "calls")
	script := `#!/bin/sh
echo "$(basename "$0") $*" >> "` + log + `"
if [ -n "$PIT_TEST_SHELL_FAIL" ]; then echo "error: $PIT_TEST_SHELL_FAIL" >&2; exit 3; fi
while [ $# -gt 0 ]; do case "$1" in --command|--) shift; break;; esac; shift; done
IN_DEV_SHELL=yes exec "$@"
`
	for _, name := range []string{"nix", "devenv"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil { //nolint:gosec // it is run
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

func calls(t *testing.T, log string) []string {
	t.Helper()
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

const devShellYAML = `processes:
  file: Procfile
  environment: devenv
  setup:
    - "sh -c 'echo $IN_DEV_SHELL $PORT > setup.txt'"
web:
  service: web
data:
  migrate:
    - "sh -c 'echo $IN_DEV_SHELL > migrate.txt'"
  scenarios:
    - name: standard
      apply: ["psql -f standard.sql"]
  default: standard
`

func TestProcessesRunInTheDevShell(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	log := fakeShell(t)
	m, req := processFixture(t, "web: node index.js\n", devShellYAML)
	pushToPullRequest(t, req.Repo.Root, 7, map[string]string{"devenv.nix": "{ pkgs, ... }: { packages = [ pkgs.nodejs ]; }\n"})
	rep := &quietReporter{}
	asked := ""
	req.Confirm = func(q string) bool { asked = q; return true }
	m.Runtime = runtime.Either{Compose: runtimetest.New("web"), Processes: runtimetest.New("web")}
	box, err := m.Up(t.Context(), req, rep)
	if err != nil {
		t.Fatalf("Up: %v", err)
	}

	// The devenv.nix the reviewer does not have was asked about.
	if asked == "" || !slices.Contains(rep.notes, "    devenv.nix") {
		t.Errorf("asked %q, notes %q", asked, rep.notes)
	}
	// Entered once before anything, then each command in it.
	wrap := []string{"devenv", "shell", "--"}
	got := calls(t, log)
	if len(got) != 3 || got[0] != "devenv shell -- true" || !strings.HasPrefix(got[1], "devenv shell -- sh -c") || !strings.HasPrefix(got[2], "devenv shell -- sh -c") {
		t.Errorf("calls = %q", got)
	}
	for _, file := range []string{"setup.txt", "migrate.txt"} {
		if text, err := os.ReadFile(filepath.Join(box.Worktree, file)); err != nil || !strings.HasPrefix(string(text), "yes") {
			t.Errorf("%s = %q, %v", file, text, err)
		}
	}
	if i := slices.Index(rep.begun, "setup"); i < 0 || rep.steps[i] != "the devenv environment and 1 command" {
		t.Errorf("steps = %q / %q", rep.begun, rep.steps)
	}
	// The processes, the scenario and any later command get it too.
	plan, err := local.ReadPlan(box.ComposeFiles[1])
	if err != nil || !slices.Equal(plan.Wrap, wrap) {
		t.Errorf("plan = %+v, %v", plan, err)
	}
	if c := m.Data.(*datatest.Fake).Calls(); len(c) != 1 || !slices.Equal(c[0].Wrap, wrap) {
		t.Errorf("data calls = %+v", c)
	}
	if w := sandbox.CommandWrap(box); !slices.Equal(w, wrap) {
		t.Errorf("command wrap = %q", w)
	}
	// So does a scenario loaded again later.
	if err := m.ResetData(t.Context(), box, data.Scenario{Name: "standard"}, rep); err != nil {
		t.Fatal(err)
	}
	if c := m.Data.(*datatest.Fake).Calls(); len(c) != 2 || !slices.Equal(c[1].Wrap, wrap) {
		t.Errorf("data calls = %+v", c)
	}
}

// Without a dev shell, a flake the pull request changes is not what
// runs the processes, and not asked about.
func TestAFlakeWithoutADevShellIsNotAskedAbout(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	m, req := processFixture(t, "web: node index.js\n", "processes:\n  file: Procfile\nweb:\n  service: web\n")
	pushToPullRequest(t, req.Repo.Root, 7, map[string]string{"flake.nix": "{ outputs = _: { }; }\n"})
	writeIn(t, req.Repo.Root, "Procfile", "web: node index.js\n")
	req.Confirm = func(q string) bool { t.Errorf("asked %q", q); return false }
	m.Runtime = runtime.Either{Compose: runtimetest.New("web"), Processes: runtimetest.New("web")}
	box, err := m.Up(t.Context(), req, &quietReporter{})
	if err != nil {
		t.Fatalf("Up: %v", err)
	}
	if w := sandbox.CommandWrap(box); len(w) != 0 {
		t.Errorf("wrap = %q", w)
	}
}

// A flake's dev shell is entered from the worktree, the one named if
// one is, without writing a lock file into the checkout.
func TestProcessesRunInTheFlakesDevShell(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	log := fakeShell(t)
	yaml := strings.Replace(devShellYAML, "environment: devenv", "environment: \"nix#api\"", 1)
	m, req := processFixture(t, "web: node index.js\n", yaml)
	pushToPullRequest(t, req.Repo.Root, 7, map[string]string{"flake.nix": "{ outputs = _: { }; }\n"})
	req.Confirm = func(string) bool { return true }
	m.Runtime = runtime.Either{Compose: runtimetest.New("web"), Processes: runtimetest.New("web")}
	box, err := m.Up(t.Context(), req, &quietReporter{})
	if err != nil {
		t.Fatalf("Up: %v", err)
	}
	want := []string{"nix", "--extra-experimental-features", "nix-command flakes", "develop", box.Worktree + "#api", "--no-write-lock-file", "--command"}
	if w := sandbox.CommandWrap(box); !slices.Equal(w, want) {
		t.Errorf("wrap = %q", w)
	}
	if got := calls(t, log); len(got) != 3 || got[0] != "nix --extra-experimental-features nix-command flakes develop "+box.Worktree+"#api --no-write-lock-file --command true" {
		t.Errorf("calls = %q", got)
	}
	if _, err := os.Stat(filepath.Join(box.Worktree, "setup.txt")); err != nil {
		t.Error(err)
	}
}

// A dev shell that cannot be entered stops the sandbox before its
// setup, with what the tool said.
func TestADevShellThatCannotBeEntered(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	log := fakeShell(t)
	t.Setenv("PIT_TEST_SHELL_FAIL", "attribute 'nodejs_99' missing")
	m, req := processFixture(t, "web: node index.js\n", devShellYAML)
	pushToPullRequest(t, req.Repo.Root, 7, map[string]string{"devenv.nix": "{ }\n"})
	req.Confirm = func(string) bool { return true }
	processes := runtimetest.New("web")
	m.Runtime = runtime.Either{Compose: runtimetest.New("web"), Processes: processes}
	var stderr strings.Builder
	rep := &quietReporter{stderr: &stderr}
	_, err := m.Up(t.Context(), req, rep)
	if err == nil || !strings.Contains(err.Error(), "cannot enter the pull request's devenv environment") {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(stderr.String(), "attribute 'nodejs_99' missing") {
		t.Errorf("stderr = %q", stderr.String())
	}
	if got := calls(t, log); len(got) != 1 || slices.Contains(processes.Methods(), "Up") {
		t.Errorf("calls = %q, processes = %q", got, processes.Methods())
	}
	if files, _ := filepath.Glob(filepath.Join(m.StateDir, "*", "*.processes.json")); len(files) != 0 {
		t.Errorf("left behind: %q", files)
	}
}

// A dev shell the reviewer's checkout has too is not asked about; one
// whose lock differs is, by the file that does.
func TestADevShellIsAskedAboutWhereItDiffers(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	fakeShell(t)
	flake := map[string]string{"flake.nix": "{ outputs = _: { }; }\n", "nix/shell.nix": "{ }\n", "flake.lock": "{\"version\": 7}\n"}
	yaml := strings.Replace(devShellYAML, "environment: devenv", "environment: nix", 1)
	m, req := processFixture(t, "web: node index.js\n", yaml)
	pushToPullRequest(t, req.Repo.Root, 7, flake)
	for name, content := range flake {
		if name == "flake.lock" {
			content = "{\"version\": 6}\n"
		}
		writeIn(t, req.Repo.Root, name, content)
	}
	writeIn(t, req.Repo.Root, "Procfile", "web: node index.js\n")
	rep := &quietReporter{}
	asked := ""
	req.Confirm = func(q string) bool { asked = q; return false }
	m.Runtime = runtime.Either{Compose: runtimetest.New("web"), Processes: runtimetest.New("web")}
	_, err := m.Up(t.Context(), req, rep)
	if err == nil || asked != "Run them?" || !slices.Contains(rep.notes, "    flake.lock") || slices.Contains(rep.notes, "    flake.nix") || slices.Contains(rep.notes, "    nix/shell.nix") {
		t.Errorf("err = %v, asked %q, notes %q", err, asked, rep.notes)
	}
	// Without anyone to ask, it says what it wanted.
	m, req = processFixture(t, "web: node index.js\n", yaml)
	pushToPullRequest(t, req.Repo.Root, 7, flake)
	writeIn(t, req.Repo.Root, "Procfile", "web: node index.js\n")
	req.Confirm = nil
	if _, err := m.Up(t.Context(), req, &quietReporter{}); err == nil || !strings.Contains(err.Error(), "wants to run its processes in a nix environment of its own") {
		t.Errorf("err = %v", err)
	}

	// The same everywhere: nothing to ask.
	m, req = processFixture(t, "web: node index.js\n", yaml)
	pushToPullRequest(t, req.Repo.Root, 7, flake)
	for name, content := range flake {
		writeIn(t, req.Repo.Root, name, content)
	}
	writeIn(t, req.Repo.Root, "Procfile", "web: node index.js\n")
	req.Confirm = func(q string) bool { t.Errorf("asked %q", q); return false }
	m.Runtime = runtime.Either{Compose: runtimetest.New("web"), Processes: runtimetest.New("web")}
	if _, err := m.Up(t.Context(), req, &quietReporter{}); err != nil {
		t.Errorf("Up: %v", err)
	}
}

func writeIn(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
