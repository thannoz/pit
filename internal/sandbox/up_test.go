package sandbox_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
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

	record, err := m.Up(t.Context(), req, rep)
	if err != nil {
		t.Fatalf("Up: %v", err)
	}

	if applied := store.Applied(); !slices.Equal(applied, []string{"standard"}) {
		t.Fatalf("applied %v, want the configured default", applied)
	}
	// Recorded, so that `pit ls` can say what a sandbox was started
	// with without reading a configuration that may have changed.
	if record.Scenario != "standard" {
		t.Errorf("the record says %q was loaded", record.Scenario)
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

func TestUpAppliesTheScenarioAfterTheMigrations(t *testing.T) {
	// A fixture that loads before the migration that creates its table
	// fails in a way that is tedious to diagnose; and once pit says
	// the sandbox answers, it has to answer with the data.
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	m, req, _ := upFixture(t)
	scenario(t, m, req)
	req.Config.Hooks.AfterUp = []string{"true"}
	req.Config.Data.Migrate = []string{"true"}
	rep := &quietReporter{}

	if _, err := m.Up(t.Context(), req, rep); err != nil {
		t.Fatalf("Up: %v", err)
	}

	want := []string{"fetch", "worktree", "services", "hooks", "migrate", "data", "healthy"}
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

// TestUpNamesAFailedMigrationAsOne is the acceptance criterion for
// T-407.
func TestUpNamesAFailedMigrationAsOne(t *testing.T) {
	// A migration is frequently the change being reviewed. Reporting
	// it as "a hook failed" hides the one thing the reviewer most
	// wants to know.
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	m, req, _ := upFixture(t)
	req.Config.Hooks.AfterUp = []string{"true"}
	req.Config.Data.Migrate = []string{"false"}

	_, err := m.Up(t.Context(), req, &quietReporter{})
	if err == nil {
		t.Fatal("want an error from the failing migration")
	}

	msg := err.Error()
	if !strings.Contains(msg, "data.migrate entry 1") {
		t.Errorf("error = %q, want it to name the migration", err)
	}
	if strings.Contains(msg, "hook") {
		t.Errorf("error = %q, want it not to blame a hook", err)
	}
	assertNothingLeftBehind(t, m, req)
}

func TestUpMigratesBeforeTheData(t *testing.T) {
	// A fixture that loads before the table it fills exists fails in a
	// way that is tedious to diagnose.
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	m, req, _ := upFixture(t)
	store := scenario(t, m, req)
	req.Config.Data.Migrate = []string{"false"}

	if _, err := m.Up(t.Context(), req, &quietReporter{}); err == nil {
		t.Fatal("want an error from the failing migration")
	}
	if applied := store.Applied(); len(applied) != 0 {
		t.Errorf("the scenario was applied although the migration failed: %v", applied)
	}
}

func TestUpRecordsHowLongEachStepTook(t *testing.T) {
	// Optimising P5 without this would be guessing. The record is
	// what a measurement is: taken while it happened, not afterwards.
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	m, req, _ := upFixture(t)
	scenario(t, m, req)
	req.Config.Hooks.AfterUp = []string{"true"}
	req.Config.Data.Migrate = []string{"true"}

	record, err := m.Up(t.Context(), req, &quietReporter{})
	if err != nil {
		t.Fatalf("Up: %v", err)
	}

	var names []string
	var counted int64
	for _, s := range record.Steps {
		names = append(names, s.Name)
		counted += s.Millis
	}

	want := []string{"fetch", "worktree", "services", "hooks", "migrate", "data", "healthy"}
	if !slices.Equal(names, want) {
		t.Errorf("recorded %v, want every step in the order it ran", names)
	}
	// The whole setup has to be at least the sum of its parts; the
	// difference is the work between the steps.
	if record.SetupMillis < counted {
		t.Errorf("the setup took %dms but its steps add up to %dms", record.SetupMillis, counted)
	}
	if record.SetupMillis <= 0 {
		t.Error("the setup is recorded as having taken no time at all")
	}
}

func TestUpRecordsNoStepsItDidNotRun(t *testing.T) {
	// The control: a project without hooks, migrations or scenarios
	// must not be given rows of zeros for them.
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	m, req, _ := upFixture(t)

	record, err := m.Up(t.Context(), req, &quietReporter{})
	if err != nil {
		t.Fatalf("Up: %v", err)
	}

	for _, s := range record.Steps {
		if s.Name == "hooks" || s.Name == "migrate" || s.Name == "data" {
			t.Errorf("recorded a %q step although none was configured", s.Name)
		}
	}
}

// TestUpReusesARunningSandbox is the acceptance criterion for T-503.
func TestUpReusesARunningSandbox(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	m, req, fake := upFixture(t)

	first, err := m.Up(t.Context(), req, &quietReporter{})
	if err != nil {
		t.Fatalf("first Up: %v", err)
	}

	rep := &quietReporter{}
	second, err := m.Up(t.Context(), req, rep)
	if err != nil {
		t.Fatalf("second Up: %v", err)
	}

	if second.URL != first.URL {
		t.Errorf("the second setup answers at %s, want the same URL as the first (%s)", second.URL, first.URL)
	}
	if !slices.Contains(rep.begun, "reuse") {
		t.Errorf("the second setup did not say it was reusing anything:\n%v", rep.begun)
	}
	// Nothing was built: the runtime was asked whether the sandbox
	// answers, and nothing else.
	if count := countMethod(fake.Methods(), "Up"); count != 1 {
		t.Errorf("the services were started %d times, want only the first", count)
	}
	if slices.Contains(rep.begun, "services") {
		t.Errorf("a services step ran on the second setup:\n%v", rep.begun)
	}
}

func TestUpDoesNotHandBackASandboxOfTheOldCommit(t *testing.T) {
	// The control for reuse: a sandbox of yesterday's code answers
	// just as readily as one of today's, and handing it over would be
	// a review of something that is not under review.
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	m, req, _ := upFixture(t)

	first, err := m.Up(t.Context(), req, &quietReporter{})
	if err != nil {
		t.Fatalf("first Up: %v", err)
	}
	advancePullRequest(t, req.Repo.Root, req.PR.Number)

	rep := &quietReporter{}
	second, err := m.Up(t.Context(), req, rep)
	if err != nil {
		t.Fatalf("second Up: %v", err)
	}

	if slices.Contains(rep.begun, "reuse") {
		t.Errorf("a sandbox of the previous commit was handed over:\n%v", rep.begun)
	}
	if second.SHA == first.SHA {
		t.Errorf("the record still points at %s", first.SHA)
	}
}

func TestUpKeepsThePortWhenBuildingAgain(t *testing.T) {
	// A pull request that moves to a new port every time it is set up
	// again defeats the reason the ports are deterministic: a browser
	// tab that stays valid.
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	m, req, fake := upFixture(t)

	first, err := m.Up(t.Context(), req, &quietReporter{})
	if err != nil {
		t.Fatalf("first Up: %v", err)
	}

	// Not reusable any more: the services are gone, so the second
	// call goes the long way round.
	if err := fake.Down(t.Context(), sandbox.RuntimeSandbox(first), io.Discard, io.Discard); err != nil {
		t.Fatalf("Down: %v", err)
	}

	second, err := m.Up(t.Context(), req, &quietReporter{})
	if err != nil {
		t.Fatalf("second Up: %v", err)
	}
	if second.Port != first.Port {
		t.Errorf("port moved from %d to %d", first.Port, second.Port)
	}
}

func countMethod(methods []string, want string) int {
	n := 0
	for _, m := range methods {
		if m == want {
			n++
		}
	}
	return n
}

// twoBuiltServices is a project where both services are built from
// source, each from its own directory -- the shape the incremental
// question is about.
const twoBuiltServices = `services:
  web:
    build: ./site
  api:
    build:
      context: ./api
      dockerfile: Dockerfile
`

// TestUpRebuildsOnlyWhatChanged is the acceptance criterion for T-504.
func TestUpRebuildsOnlyWhatChanged(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	m, req, fake := upFixture(t)
	fake.Declared = []string{"web", "api"}

	pushToPullRequest(t, req.Repo.Root, req.PR.Number, map[string]string{
		"docker-compose.yml": twoBuiltServices,
		"site/Dockerfile":    "FROM nginx\n",
		"site/index.html":    "<h1>shop</h1>\n",
		"api/Dockerfile":     "FROM golang\n",
		"api/main.go":        "package main\n",
	})
	if _, err := m.Up(t.Context(), req, &quietReporter{}); err != nil {
		t.Fatalf("first Up: %v", err)
	}

	// The author pushes a change to the frontend only.
	pushToPullRequest(t, req.Repo.Root, req.PR.Number, map[string]string{
		"site/index.html": "<h1>shop, now with refunds</h1>\n",
	})

	rep := &quietReporter{}
	if _, err := m.Up(t.Context(), req, rep); err != nil {
		t.Fatalf("second Up: %v", err)
	}

	ups := upCalls(fake)
	if len(ups) != 2 {
		t.Fatalf("the runtime was asked to bring services up %d times, want 2", len(ups))
	}
	if len(ups[0].Services) != 0 {
		t.Errorf("the first setup built only %v, want the whole project", ups[0].Services)
	}
	if !slices.Equal(ups[1].Services, []string{"web"}) {
		t.Errorf("the second setup built %v, want only web", ups[1].Services)
	}
	if !slices.Contains(rep.steps, "web rebuilt") {
		t.Errorf("the narration does not say what was rebuilt:\n%v", rep.steps)
	}
}

func TestUpRebuildsNothingForAChangeNoServiceContains(t *testing.T) {
	// A commit that only touches documentation leaves the sandbox
	// exactly as it is.
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	m, req, fake := upFixture(t)
	fake.Declared = []string{"web", "api"}

	pushToPullRequest(t, req.Repo.Root, req.PR.Number, map[string]string{
		"docker-compose.yml": twoBuiltServices,
		"site/Dockerfile":    "FROM nginx\n",
		"api/Dockerfile":     "FROM golang\n",
	})
	if _, err := m.Up(t.Context(), req, &quietReporter{}); err != nil {
		t.Fatalf("first Up: %v", err)
	}

	pushToPullRequest(t, req.Repo.Root, req.PR.Number, map[string]string{
		"README.md": "a paragraph about refunds\n",
	})

	rep := &quietReporter{}
	if _, err := m.Up(t.Context(), req, rep); err != nil {
		t.Fatalf("second Up: %v", err)
	}

	if len(upCalls(fake)) != 1 {
		t.Errorf("the services were brought up again for a change no service contains")
	}
	if !slices.Contains(rep.steps, "nothing to rebuild") {
		t.Errorf("the narration does not say that nothing was needed:\n%v", rep.steps)
	}
}

func TestUpRebuildsEverythingWhenTheComposeFileChanges(t *testing.T) {
	// What a diff means is worked out from the compose file. Once
	// that has moved, the diff cannot be read against it.
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	m, req, fake := upFixture(t)
	fake.Declared = []string{"web", "api"}

	pushToPullRequest(t, req.Repo.Root, req.PR.Number, map[string]string{
		"docker-compose.yml": twoBuiltServices,
		"site/Dockerfile":    "FROM nginx\n",
		"api/Dockerfile":     "FROM golang\n",
	})
	if _, err := m.Up(t.Context(), req, &quietReporter{}); err != nil {
		t.Fatalf("first Up: %v", err)
	}

	pushToPullRequest(t, req.Repo.Root, req.PR.Number, map[string]string{
		"docker-compose.yml": twoBuiltServices + "  cache:\n    image: redis\n",
	})

	if _, err := m.Up(t.Context(), req, &quietReporter{}); err != nil {
		t.Fatalf("second Up: %v", err)
	}

	ups := upCalls(fake)
	if len(ups) != 2 || len(ups[1].Services) != 0 {
		t.Errorf("the second setup built %v, want the whole project", ups)
	}
}

func upCalls(f *runtimetest.Fake) []runtimetest.Call {
	var out []runtimetest.Call
	for _, c := range f.Calls() {
		if c.Method == "Up" {
			out = append(out, c)
		}
	}
	return out
}

// updated brings a sandbox up, moves the pull request on by a commit,
// and returns the manager ready for the second setup.
func updated(t *testing.T) (*sandbox.Manager, sandbox.UpRequest, *datatest.Fake) {
	t.Helper()

	m, req, _ := upFixture(t)
	store := scenario(t, m, req)

	if _, err := m.Up(t.Context(), req, &quietReporter{}); err != nil {
		t.Fatalf("first Up: %v", err)
	}
	advancePullRequest(t, req.Repo.Root, req.PR.Number)
	return m, req, store
}

func TestUpKeepsTheDataOfASandboxItUpdates(t *testing.T) {
	// The reviewer has been working in this sandbox. A new commit is
	// no reason to throw away what they put in it.
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	m, req, store := updated(t)

	rep := &quietReporter{}
	record, err := m.Up(t.Context(), req, rep)
	if err != nil {
		t.Fatalf("second Up: %v", err)
	}

	if applied := store.Applied(); len(applied) != 1 {
		t.Errorf("the data was loaded %d times, want only the first setup", len(applied))
	}
	if !slices.Contains(rep.steps, "kept as it was") {
		t.Errorf("the narration does not say the data was kept:\n%v", rep.steps)
	}
	if record.Scenario != "standard" {
		t.Errorf("Scenario = %q, want what is actually in the sandbox", record.Scenario)
	}
}

func TestUpReloadsTheDataWhenTheReviewerSaysSo(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	m, req, store := updated(t)

	var asked string
	req.Confirm = func(question string) bool {
		asked = question
		return true
	}

	if _, err := m.Up(t.Context(), req, &quietReporter{}); err != nil {
		t.Fatalf("second Up: %v", err)
	}

	if applied := store.Applied(); len(applied) != 2 {
		t.Errorf("the data was loaded %d times, want it loaded again", len(applied))
	}
	if !strings.Contains(asked, "standard") {
		t.Errorf("the question does not say what would be loaded: %q", asked)
	}
}

func TestUpDoesNotAskWhenAScenarioWasNamed(t *testing.T) {
	// Typing --scenario is an answer in itself.
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	m, req, store := updated(t)
	req.Scenario = "leer"
	req.Config.Data.Scenarios = append(req.Config.Data.Scenarios, config.Scenario{
		Name:  "leer",
		Apply: []string{"compose exec -T db psql -f /fixtures/empty.sql"},
	})
	req.Confirm = func(string) bool {
		t.Error("the reviewer was asked although they had already said what they wanted")
		return false
	}

	record, err := m.Up(t.Context(), req, &quietReporter{})
	if err != nil {
		t.Fatalf("second Up: %v", err)
	}

	if applied := store.Applied(); len(applied) != 2 || applied[1] != "leer" {
		t.Errorf("applied %v, want the scenario that was asked for", applied)
	}
	if record.Scenario != "leer" {
		t.Errorf("Scenario = %q, want what was loaded", record.Scenario)
	}
}

func TestUpKeepsThePortOfASandboxItUpdates(t *testing.T) {
	// The port is bound -- by this sandbox's own container -- so it
	// looks taken to anyone who asks, and the allocator would move the
	// pull request to the next one. A fake runtime binds nothing, so
	// this only shows up when something really holds the port.
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	m, req, _ := upFixture(t)

	first, err := m.Up(t.Context(), req, &quietReporter{})
	if err != nil {
		t.Fatalf("first Up: %v", err)
	}

	// On every interface, the way Docker publishes a port -- a
	// loopback-only listener does not collide with it everywhere.
	var lc net.ListenConfig
	listener, err := lc.Listen(t.Context(), "tcp", ":"+strconv.Itoa(first.Port))
	if err != nil {
		t.Fatalf("cannot hold the sandbox's port: %v", err)
	}
	defer func() { _ = listener.Close() }()

	advancePullRequest(t, req.Repo.Root, req.PR.Number)

	second, err := m.Up(t.Context(), req, &quietReporter{})
	if err != nil {
		t.Fatalf("second Up: %v", err)
	}
	if second.Port != first.Port {
		t.Errorf("port moved from %d to %d while the sandbox was holding it", first.Port, second.Port)
	}
	if second.URL != first.URL {
		t.Errorf("URL moved from %s to %s", first.URL, second.URL)
	}
}
