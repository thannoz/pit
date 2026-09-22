package doctor_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thannoz/pit/internal/doctor"
	"github.com/thannoz/pit/internal/proc"
	"github.com/thannoz/pit/internal/state"
)

// stubRunner answers a command from a table, and fails for anything
// whose name is in missing.
type stubRunner struct {
	replies map[string]string
	missing map[string]bool
	fails   map[string]bool
}

func (s *stubRunner) Output(_ context.Context, c proc.Command) ([]byte, error) {
	if s.missing[c.Name] {
		return nil, errors.New(c.Name + " is not installed or not on PATH")
	}
	key := c.Name + " " + strings.Join(c.Args, " ")
	if s.fails[key] {
		return nil, errors.New("refused")
	}
	if out, ok := s.replies[key]; ok {
		return []byte(out), nil
	}
	return []byte("ok"), nil
}

func healthyRunner() *stubRunner {
	return &stubRunner{
		replies: map[string]string{
			"git --version":                           "git version 2.50.1",
			"docker --version":                        "Docker version 29.3.1",
			"gh --version":                            "gh version 2.98.0\nhttps://github.com/cli/cli",
			"docker compose version --short":          "2.31.0",
			"docker info --format {{.ServerVersion}}": "29.3.1",
		},
		missing: map[string]bool{},
		fails:   map[string]bool{},
	}
}

func env(t *testing.T, r doctor.Runner) doctor.Environment {
	t.Helper()

	dir := t.TempDir()
	store, err := state.Open(dir)
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	return doctor.Environment{Runner: r, Store: store, StateDir: dir, WorkDir: dir}
}

func findingFor(t *testing.T, report doctor.Report, name string) doctor.Finding {
	t.Helper()

	for _, f := range report.Findings {
		if f.Name == name {
			return f
		}
	}
	t.Fatalf("no finding called %q in %+v", name, report.Findings)
	return doctor.Finding{}
}

// TestEveryPreconditionIsDetected is the acceptance criterion for
// T-313: each of the things P3 relies on has to be recognised when it
// is missing, and each has to say what to do.
func TestEveryPreconditionIsDetected(t *testing.T) {
	tests := []struct {
		name    string
		break_  func(*stubRunner)
		finding string
		wantFix string
	}{
		{"git is missing", func(r *stubRunner) { r.missing["git"] = true }, "git", "install git"},
		{"docker is missing", func(r *stubRunner) { r.missing["docker"] = true }, "docker", "docs.docker.com"},
		{
			name:    "the daemon is not running",
			break_:  func(r *stubRunner) { r.fails["docker info --format {{.ServerVersion}}"] = true },
			finding: "docker daemon",
			wantFix: "start Docker",
		},
		{
			name:    "compose is v1",
			break_:  func(r *stubRunner) { r.replies["docker compose version --short"] = "1.29.2" },
			finding: "docker compose",
			wantFix: "Compose v2",
		},
		{"gh is missing", func(r *stubRunner) { r.missing["gh"] = true }, "gh", "cli.github.com"},
		{
			name:    "gh is not logged in",
			break_:  func(r *stubRunner) { r.fails["gh auth status"] = true },
			finding: "gh account",
			wantFix: "gh auth login",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := healthyRunner()
			tt.break_(r)

			report := doctor.Run(t.Context(), doctor.Default(env(t, r)))
			f := findingFor(t, report, tt.finding)

			if f.Result != doctor.Fail {
				t.Errorf("%s = %q, want a failure", tt.finding, f.Result)
			}
			if !strings.Contains(f.Fix, tt.wantFix) {
				t.Errorf("fix = %q, want it to mention %q", f.Fix, tt.wantFix)
			}
			if !report.Failed() {
				t.Error("the report does not report a failure")
			}
		})
	}
}

func TestAHealthyMachinePasses(t *testing.T) {
	report := doctor.Run(t.Context(), doctor.Default(env(t, healthyRunner())))

	if report.Failed() {
		for _, f := range report.Findings {
			if f.Result == doctor.Fail {
				t.Errorf("unexpected failure: %+v", f)
			}
		}
	}
	for _, name := range []string{"git", "docker", "docker compose", "gh", "state directory", "port range"} {
		if f := findingFor(t, report, name); f.Result == doctor.Fail {
			t.Errorf("%s failed on a healthy machine: %+v", name, f)
		}
	}
}

func TestAMissingConfigurationIsOnlyWorthKnowing(t *testing.T) {
	// doctor is also run to find out why something else is wrong, so
	// standing outside a configured repository is not a failure.
	report := doctor.Run(t.Context(), doctor.Default(env(t, healthyRunner())))

	f := findingFor(t, report, ".pit.yaml")
	if f.Result != doctor.Warn {
		t.Errorf("result = %q, want a warning", f.Result)
	}
	if !strings.Contains(f.Fix, "pit init") {
		t.Errorf("fix = %q, want it to point at pit init", f.Fix)
	}
}

func TestABrokenConfigurationFails(t *testing.T) {
	e := env(t, healthyRunner())
	writeFile(t, filepath.Join(e.WorkDir, ".git"), "")
	writeFile(t, filepath.Join(e.WorkDir, ".pit.yaml"), "web:\n  prot: 3000\n")

	f := findingFor(t, doctor.Run(t.Context(), doctor.Default(e)), ".pit.yaml")
	if f.Result != doctor.Fail {
		t.Errorf("result = %q, want a failure for a broken file", f.Result)
	}
}

// TestStrayContainersAreNoticed covers the gap a second Ctrl+C leaves:
// containers pit created that no record mentions. Nothing else would
// ever point at them again.
func TestStrayContainersAreNoticed(t *testing.T) {
	r := healthyRunner()
	r.replies[`docker ps --all --filter name=pit- --format {{.Label "com.docker.compose.project"}}`] =
		"pit-acme-shop-c56680-482\npit-acme-shop-c56680-482\nsomeone-elses-project\n"

	f := findingFor(t, doctor.Run(t.Context(), doctor.Default(env(t, r))), "stray containers")

	if f.Result != doctor.Warn {
		t.Errorf("result = %q, want a warning", f.Result)
	}
	if !strings.Contains(f.Detail, "pit-acme-shop-c56680-482") {
		t.Errorf("detail = %q, want it to name the project", f.Detail)
	}
	// Listed once, although Docker reported it per container.
	if strings.Count(f.Detail, "pit-acme-shop-c56680-482") != 1 {
		t.Errorf("detail = %q, want the project named once", f.Detail)
	}
	// Someone else's project is none of pit's business.
	if strings.Contains(f.Detail, "someone-elses-project") {
		t.Errorf("detail = %q, want it to leave other projects alone", f.Detail)
	}
}

func TestRecordedProjectsAreNotStray(t *testing.T) {
	e := env(t, healthyRunner())
	err := e.Store.Update(func(f *state.File) error {
		f.Put(state.Sandbox{PR: 482, RepoRef: "acme-shop-c56680", Project: "pit-acme-shop-c56680-482"})
		return nil
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	e.Runner.(*stubRunner).replies[`docker ps --all --filter name=pit- --format {{.Label "com.docker.compose.project"}}`] =
		"pit-acme-shop-c56680-482\n"

	f := findingFor(t, doctor.Run(t.Context(), doctor.Default(e)), "stray containers")
	if f.Result != doctor.OK {
		t.Errorf("a recorded project was reported as stray: %+v", f)
	}
}

func TestAnUnusableStateDirectoryFails(t *testing.T) {
	e := doctor.Environment{Runner: healthyRunner(), Store: nil, StateDir: "/nope", WorkDir: t.TempDir()}

	f := findingFor(t, doctor.Run(t.Context(), doctor.Default(e)), "state directory")
	if f.Result != doctor.Fail {
		t.Errorf("result = %q, want a failure", f.Result)
	}
}

func TestAnotherPitHoldingTheLockIsNoticed(t *testing.T) {
	e := env(t, healthyRunner())
	release, ok := e.Store.TryLock()
	if !ok {
		t.Fatal("could not take the lock")
	}
	defer release()

	// A second store on the same directory stands in for a second pit.
	other, err := state.Open(e.StateDir)
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	e.Store = other

	f := findingFor(t, doctor.Run(t.Context(), doctor.Default(e)), "no other pit running")
	if f.Result != doctor.Warn {
		t.Errorf("result = %q, want a warning", f.Result)
	}
}

func TestCounts(t *testing.T) {
	report := doctor.Report{Findings: []doctor.Finding{
		{Result: doctor.OK}, {Result: doctor.OK},
		{Result: doctor.Warn},
		{Result: doctor.Fail},
	}}

	ok, warn, fail := report.Counts()
	if ok != 2 || warn != 1 || fail != 1 {
		t.Errorf("Counts() = %d, %d, %d; want 2, 1, 1", ok, warn, fail)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile(%q): %v", path, err)
	}
}

// TestDoctorNeverBlocks is the property the hang above exposed: doctor
// is run precisely when something is stuck, so no check may wait for a
// lock another pit is holding.
func TestDoctorNeverBlocks(t *testing.T) {
	e := env(t, healthyRunner())

	// Hold the lock for the whole run, the way a second pit would.
	release, ok := e.Store.TryLock()
	if !ok {
		t.Fatal("could not take the lock")
	}
	defer release()

	other, err := state.Open(e.StateDir)
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	e.Store = other

	done := make(chan doctor.Report, 1)
	go func() { done <- doctor.Run(t.Context(), doctor.Default(e)) }()

	select {
	case report := <-done:
		if f := findingFor(t, report, "stray containers"); f.Result != doctor.Warn {
			t.Errorf("stray containers = %q, want a warning while the record is locked", f.Result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("doctor is waiting for a lock another pit holds")
	}
}
