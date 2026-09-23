package sandbox_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/thannoz/pit/internal/config"
	"github.com/thannoz/pit/internal/data/datatest"
	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/forge"
	"github.com/thannoz/pit/internal/proc"
	"github.com/thannoz/pit/internal/runtime/runtimetest"
	"github.com/thannoz/pit/internal/sandbox"
	"github.com/thannoz/pit/internal/state"
	"github.com/thannoz/pit/internal/workspace"
)

// quietReporter records the narration instead of printing it.
type quietReporter struct {
	begun []string
	steps []string
}

func (r *quietReporter) Begin(name string, _ bool) { r.begun = append(r.begun, name) }
func (r *quietReporter) Done(format string, args ...any) {
	r.steps = append(r.steps, fmt.Sprintf(format, args...))
}
func (r *quietReporter) Stdout() io.Writer { return io.Discard }
func (r *quietReporter) Stderr() io.Writer { return io.Discard }

// upFixture builds a real git repository with a pull request, plus the
// manager to review it with.
func upFixture(t *testing.T) (*sandbox.Manager, sandbox.UpRequest, *runtimetest.Fake) {
	t.Helper()

	repo := newUpstream(t)
	stateDir := t.TempDir()
	store, err := state.Open(stateDir)
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}

	fake := runtimetest.New("web")
	m := &sandbox.Manager{
		Store: store, Runtime: fake, Git: proc.Exec{}, Proc: proc.Exec{},
		Data: datatest.New(), StateDir: stateDir,
	}

	cfg, err := config.Parse([]byte("web:\n  service: web\n  port: 80\n"))
	if err != nil {
		t.Fatalf("config.Parse: %v", err)
	}

	req := sandbox.UpRequest{
		Repo:   repo,
		PR:     forge.PR{Number: 7, Title: "A change", Author: "lisa", Branch: "feature", State: forge.Open},
		Config: cfg,
	}
	return m, req, fake
}

// TestUpBuildsAndRecords is the acceptance criterion for T-309b.
func TestUpBuildsAndRecords(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	m, req, fake := upFixture(t)
	rep := &quietReporter{}

	record, err := m.Up(t.Context(), req, rep)
	if err != nil {
		t.Fatalf("Up: %v", err)
	}

	if record.Port < 40000 || record.Port > 49999 {
		t.Errorf("Port = %d, outside pit's range", record.Port)
	}
	if record.URL == "" || record.SHA == "" || record.Worktree == "" {
		t.Errorf("record = %+v, want it fully populated", record)
	}
	if !fake.IsUp(record.Project) {
		t.Error("the services were never started")
	}

	// The worktree really holds the pull request's change.
	if _, err := os.Stat(filepath.Join(record.Worktree, "pr.txt")); err != nil {
		t.Errorf("the worktree does not hold the change: %v", err)
	}

	f, err := m.Store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := f.Find(req.Repo.Identity.Ref(), 7); !ok {
		t.Error("the sandbox was not recorded")
	}

	// Every step announces itself before it runs and reports when it
	// is done, so the display always has something to show.
	if len(rep.begun) != len(rep.steps) {
		t.Errorf("%d steps begun but %d finished: %v / %v", len(rep.begun), len(rep.steps), rep.begun, rep.steps)
	}
	for _, want := range []string{"fetch", "worktree", "services", "healthy"} {
		if !slices.Contains(rep.begun, want) {
			t.Errorf("the step %q was never announced: %v", want, rep.begun)
		}
	}
}

// TestUpUndoesEverythingWhenAStepFails is the property T-310 depends
// on: a half-built sandbox holds a port, a worktree and containers that
// nothing knows about.
func TestUpUndoesEverythingWhenAStepFails(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}

	failures := map[string]string{
		"the services never start":     "Up",
		"the services never get ready": "WaitReady",
	}

	for name, method := range failures {
		t.Run(name, func(t *testing.T) {
			m, req, fake := upFixture(t)
			fake.Fail[method] = errors.New("something went wrong")

			if _, err := m.Up(t.Context(), req, &quietReporter{}); err == nil {
				t.Fatal("want an error")
			}

			assertNothingLeftBehind(t, m, req)
		})
	}
}

// TestUpUndoesEverythingWhenCancelled is the acceptance criterion for
// T-310, at the level where it can be tested deterministically.
func TestUpUndoesEverythingWhenCancelled(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	m, req, fake := upFixture(t)

	ctx, cancel := context.WithCancel(t.Context())
	// Cancel while the services are being started, which is where a
	// reviewer's Ctrl+C usually lands.
	fake.Fail["WaitReady"] = context.Canceled
	cancel()

	if _, err := m.Up(ctx, req, &quietReporter{}); err == nil {
		t.Fatal("want an error")
	}

	// The cleanup must not use the cancelled context: every command
	// would refuse to start, and nothing would be undone.
	assertNothingLeftBehind(t, m, req)
}

func assertNothingLeftBehind(t *testing.T, m *sandbox.Manager, req sandbox.UpRequest) {
	t.Helper()

	worktree := req.Repo.Identity.WorktreeDir(m.StateDir, req.PR.Number)
	if _, err := os.Stat(worktree); !os.IsNotExist(err) {
		t.Errorf("%s survived the failure", worktree)
	}

	if _, err := workspace.ResolveRef(t.Context(), proc.Exec{}, req.Repo.Root, workspace.LocalRef(req.PR.Number)); err == nil {
		t.Error("the fetched ref survived the failure")
	}

	out, err := proc.Exec{}.Output(t.Context(), proc.Command{
		Name: "git", Args: []string{"worktree", "list"}, Dir: req.Repo.Root,
	})
	if err != nil {
		t.Fatalf("git worktree list: %v", err)
	}
	if bytes.Count(out, []byte("\n")) != 1 {
		t.Errorf("git still knows about a worktree:\n%s", out)
	}

	f, err := m.Store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(f.Sandboxes) != 0 {
		t.Errorf("the failed setup was recorded anyway: %+v", f.Sandboxes)
	}
}

func TestUpRunsTheConfiguredHooks(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	m, req, _ := upFixture(t)

	marker := filepath.Join(t.TempDir(), "hook-ran")
	req.Config.Hooks.AfterUp = []string{"touch " + marker}

	if _, err := m.Up(t.Context(), req, &quietReporter{}); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("the hook did not run: %v", err)
	}
}

func TestUpUndoesEverythingWhenAHookFails(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	m, req, _ := upFixture(t)
	req.Config.Hooks.AfterUp = []string{"false"}

	if _, err := m.Up(t.Context(), req, &quietReporter{}); err == nil {
		t.Fatal("want an error from the failing hook")
	}
	assertNothingLeftBehind(t, m, req)
}

func TestUpDoesNotReuseAPortItAlreadyHandedOut(t *testing.T) {
	// Two sandboxes started in quick succession can otherwise pick the
	// same number: the first has not bound it when the second checks.
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	m, req, _ := upFixture(t)

	first, err := m.Up(t.Context(), req, &quietReporter{})
	if err != nil {
		t.Fatalf("Up: %v", err)
	}

	req.PR.Number = 8
	addPullRequest(t, req.Repo.Root, 8)
	second, err := m.Up(t.Context(), req, &quietReporter{})
	if err != nil {
		t.Fatalf("second Up: %v", err)
	}

	if first.Port == second.Port {
		t.Errorf("both sandboxes got port %d", first.Port)
	}
}

func TestSandboxAge(t *testing.T) {
	box := state.Sandbox{CreatedAt: time.Now().Add(-90 * time.Minute)}
	if box.Age() < time.Hour {
		t.Errorf("Age() = %v, want about 90 minutes", box.Age())
	}
}

// scenario configures one named data state and returns the fake store
// it will be applied against.
func scenario(t *testing.T, m *sandbox.Manager, req sandbox.UpRequest) *datatest.Fake {
	t.Helper()

	req.Config.Data.Scenarios = []config.Scenario{{
		Name:        "standard",
		Description: "3 users, 20 products, 5 orders",
		Apply:       []string{"compose exec -T db psql -f /fixtures/standard.sql"},
	}}
	req.Config.Data.Default = "standard"

	store, ok := m.Data.(*datatest.Fake)
	if !ok {
		t.Fatalf("the fixture's data store is %T", m.Data)
	}
	return store
}

// TestUpAppliesTheDefaultScenario is the acceptance criterion for
// T-403: a sandbox comes up in the state the repository declared,
// without the reviewer asking for anything.
func TestUpAppliesTheDefaultScenario(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	m, req, _ := upFixture(t)
	store := scenario(t, m, req)
	rep := &quietReporter{}

	if _, err := m.Up(t.Context(), req, rep); err != nil {
		t.Fatalf("Up: %v", err)
	}

	if applied := store.Applied(); !slices.Equal(applied, []string{"standard"}) {
		t.Fatalf("applied %v, want the configured default", applied)
	}
	if !slices.Contains(rep.steps, "scenario standard") {
		t.Errorf("the narration does not say which data was loaded:\n%v", rep.steps)
	}
}

// TestUpLeavesTheDataAloneWithoutAScenario is the control: the step
// has to be absent when nothing is configured, or the assertion above
// proves nothing.
func TestUpLeavesTheDataAloneWithoutAScenario(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	m, req, _ := upFixture(t)
	rep := &quietReporter{}

	if _, err := m.Up(t.Context(), req, rep); err != nil {
		t.Fatalf("Up: %v", err)
	}

	store := m.Data.(*datatest.Fake)
	if applied := store.Applied(); len(applied) != 0 {
		t.Errorf("applied %v although no scenario is configured", applied)
	}
	if slices.Contains(rep.begun, "data") {
		t.Errorf("a data step was announced anyway:\n%v", rep.begun)
	}
}

func TestUpAppliesTheScenarioAfterTheHooks(t *testing.T) {
	// A fixture that loads before the migration that creates its table
	// fails in a way that is tedious to diagnose; and once pit says
	// the sandbox answers, it has to answer with the data.
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	m, req, _ := upFixture(t)
	scenario(t, m, req)
	req.Config.Hooks.AfterUp = []string{"true"}
	rep := &quietReporter{}

	if _, err := m.Up(t.Context(), req, rep); err != nil {
		t.Fatalf("Up: %v", err)
	}

	want := []string{"fetch", "worktree", "services", "hooks", "data", "healthy"}
	if !slices.Equal(rep.begun, want) {
		t.Errorf("steps ran as %v, want %v", rep.begun, want)
	}
}

func TestUpUndoesEverythingWhenTheScenarioFails(t *testing.T) {
	// A sandbox whose data never loaded looks ready and shows an empty
	// screen, which is the failure this whole stage exists to prevent.
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	m, req, _ := upFixture(t)
	store := scenario(t, m, req)
	store.Err = errors.New("relation \"orders\" does not exist")

	if _, err := m.Up(t.Context(), req, &quietReporter{}); err == nil {
		t.Fatal("want an error from the failing scenario")
	}
	assertNothingLeftBehind(t, m, req)
}

func TestUpRejectsAScenarioThatIsNotConfigured(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	m, req, fake := upFixture(t)
	scenario(t, m, req)
	req.Scenario = "standrad"

	_, err := m.Up(t.Context(), req, &quietReporter{})
	if err == nil {
		t.Fatal("want an error for a scenario that is not configured")
	}
	if !strings.Contains(err.Error(), "standrad") {
		t.Errorf("error = %q, want it to quote the name", err)
	}
	if hint := errs.Hint(err); !strings.Contains(hint, `did you mean "standard"?`) {
		t.Errorf("hint = %q, want the suggestion", hint)
	}
	// And it has to be caught before anything was built, not after.
	if slices.Contains(fake.Methods(), "Up") {
		t.Error("the services were started for a scenario that does not exist")
	}
	assertNothingLeftBehind(t, m, req)
}
