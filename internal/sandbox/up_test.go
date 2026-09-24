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
	"sync"
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

	want := []string{"fetch", "worktree", "build", "services", "hooks", "migrate", "data", "healthy"}
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

	want := []string{"fetch", "worktree", "build", "services", "hooks", "migrate", "data", "healthy"}
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
	if slices.Contains(rep.begun, "build") || slices.Contains(rep.begun, "services") {
		t.Errorf("the second setup built or started something:\n%v", rep.begun)
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

	builds := callsTo(fake, "Build")
	if len(builds) != 2 {
		t.Fatalf("the runtime was asked to build %d times, want 2", len(builds))
	}
	if !slices.Equal(builds[0].Services, []string{"web", "api"}) {
		t.Errorf("the first setup built %v, want both services", builds[0].Services)
	}
	if !slices.Equal(builds[1].Services, []string{"web"}) {
		t.Errorf("the second setup built %v, want only web", builds[1].Services)
	}
	if !slices.Contains(rep.steps, "web built") {
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

	if builds := callsTo(fake, "Build"); len(builds) != 1 {
		t.Errorf("something was built for a change no service contains: %+v", builds)
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

	builds := callsTo(fake, "Build")
	if len(builds) != 2 || !slices.Equal(builds[1].Services, []string{"web", "api"}) {
		t.Errorf("the second setup built %v, want the whole project", builds)
	}
}

func callsTo(f *runtimetest.Fake, method string) []runtimetest.Call {
	var out []runtimetest.Call
	for _, c := range f.Calls() {
		if c.Method == method {
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

// TestUpPullsInsteadOfBuilding is the acceptance criterion for T-506.
func TestUpPullsInsteadOfBuilding(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	m, req, fake := upFixture(t)
	fake.Declared = []string{"web", "api"}
	req.Config.Build.Prebuilt = "ghcr.io/acme/shop-{service}:{sha}"

	pushToPullRequest(t, req.Repo.Root, req.PR.Number, map[string]string{
		"docker-compose.yml": twoBuiltServices,
		"site/Dockerfile":    "FROM nginx\n",
		"api/Dockerfile":     "FROM golang\n",
	})
	sha := pullRequestHead(t, req.Repo.Root, req.PR.Number)
	fake.Prebuilt = map[string]bool{
		"ghcr.io/acme/shop-web:" + sha: true,
		"ghcr.io/acme/shop-api:" + sha: true,
	}

	rep := &quietReporter{}
	if _, err := m.Up(t.Context(), req, rep); err != nil {
		t.Fatalf("Up: %v", err)
	}

	if builds := callsTo(fake, "Build"); len(builds) != 0 {
		t.Errorf("something was built although every image had been published: %+v", builds)
	}
	if pulls := callsTo(fake, "Pull"); len(pulls) != 2 {
		t.Errorf("pulled %d images, want one per service", len(pulls))
	}
	if !slices.Contains(rep.steps, "web, api pulled") {
		t.Errorf("the narration does not say the images were pulled:\n%v", rep.steps)
	}
}

func TestUpBuildsWhatWasNotPublished(t *testing.T) {
	// A pull that finds nothing is an ordinary answer, not a failure:
	// nobody published that image, so pit builds it, which is what it
	// would have done anyway.
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	m, req, fake := upFixture(t)
	fake.Declared = []string{"web", "api"}
	req.Config.Build.Prebuilt = "ghcr.io/acme/shop-{service}:{sha}"

	pushToPullRequest(t, req.Repo.Root, req.PR.Number, map[string]string{
		"docker-compose.yml": twoBuiltServices,
		"site/Dockerfile":    "FROM nginx\n",
		"api/Dockerfile":     "FROM golang\n",
	})
	sha := pullRequestHead(t, req.Repo.Root, req.PR.Number)
	fake.Prebuilt = map[string]bool{"ghcr.io/acme/shop-web:" + sha: true}

	rep := &quietReporter{}
	if _, err := m.Up(t.Context(), req, rep); err != nil {
		t.Fatalf("Up: %v", err)
	}

	builds := callsTo(fake, "Build")
	if len(builds) != 1 || !slices.Equal(builds[0].Services, []string{"api"}) {
		t.Fatalf("built %+v, want only the service nobody published", builds)
	}
	if !slices.Contains(rep.steps, "web pulled, api built") {
		t.Errorf("the narration does not say which was which:\n%v", rep.steps)
	}
}

func TestUpNamesThePulledImageInTheOverride(t *testing.T) {
	// Compose has to be told to use the image instead of the build,
	// or the next command would build it after all.
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	m, req, fake := upFixture(t)
	fake.Declared = []string{"web", "api"}
	req.Config.Build.Prebuilt = "ghcr.io/acme/shop-{service}:{sha}"

	pushToPullRequest(t, req.Repo.Root, req.PR.Number, map[string]string{
		"docker-compose.yml": twoBuiltServices,
		"site/Dockerfile":    "FROM nginx\n",
		"api/Dockerfile":     "FROM golang\n",
	})
	sha := pullRequestHead(t, req.Repo.Root, req.PR.Number)
	fake.Prebuilt = map[string]bool{"ghcr.io/acme/shop-web:" + sha: true}

	record, err := m.Up(t.Context(), req, &quietReporter{})
	if err != nil {
		t.Fatalf("Up: %v", err)
	}

	override := record.ComposeFiles[len(record.ComposeFiles)-1]
	written, err := os.ReadFile(override)
	if err != nil {
		t.Fatalf("cannot read the generated override: %v", err)
	}
	if !strings.Contains(string(written), "image: ghcr.io/acme/shop-web:"+sha) {
		t.Errorf("the override does not name the pulled image:\n%s", written)
	}
	if strings.Contains(string(written), "shop-api") {
		t.Errorf("the override names an image nobody published:\n%s", written)
	}
}

// eightServices is the shape the question is about: four services
// decide what a screen looks like, and four more cost build time and
// answer nothing a reviewer asked.
const eightServices = `services:
  web:
    build: ./site
    depends_on: [api]
  api:
    build: ./api
    depends_on:
      db:
        condition: service_started
      cache:
        condition: service_started
  db:
    image: postgres:16
  cache:
    image: redis:7
  worker:
    build: ./api
  mailer:
    image: mailhog/mailhog
  analytics:
    build: ./analytics
  docs:
    build: ./docs
`

func atEightServices(t *testing.T, req sandbox.UpRequest) {
	t.Helper()

	pushToPullRequest(t, req.Repo.Root, req.PR.Number, map[string]string{
		"docker-compose.yml":   eightServices,
		"site/Dockerfile":      "FROM nginx\n",
		"api/Dockerfile":       "FROM golang\n",
		"analytics/Dockerfile": "FROM python\n",
		"docs/Dockerfile":      "FROM nginx\n",
	})
}

// TestUpStartsOnlyTheServicesAReviewNeeds is the acceptance criterion
// for T-508.
func TestUpStartsOnlyTheServicesAReviewNeeds(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	m, req, fake := upFixture(t)
	fake.Declared = []string{"web", "api", "db", "cache"}
	atEightServices(t, req)
	// One entry point: what it needs comes with it.
	req.Config.Compose.Services = []string{"web"}

	rep := &quietReporter{}
	if _, err := m.Up(t.Context(), req, rep); err != nil {
		t.Fatalf("Up: %v", err)
	}

	ups := callsTo(fake, "Up")
	if len(ups) != 1 {
		t.Fatalf("started the services %d times, want once", len(ups))
	}
	want := []string{"web", "api", "db", "cache"}
	if !slices.Equal(ups[0].Services, want) {
		t.Errorf("started %v, want %v", ups[0].Services, want)
	}
	if !slices.Contains(rep.steps, "pit-"+req.Repo.Identity.Ref()+"-7, 4 of 8 services") {
		t.Errorf("the narration does not say how much of the project is running:\n%v", rep.steps)
	}
}

func TestUpBuildsNothingForAServiceItDoesNotStart(t *testing.T) {
	// Building an image for a service nobody starts costs exactly as
	// much as building one that somebody does.
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	m, req, fake := upFixture(t)
	fake.Declared = []string{"web", "api", "db", "cache"}
	atEightServices(t, req)
	req.Config.Compose.Services = []string{"web"}

	if _, err := m.Up(t.Context(), req, &quietReporter{}); err != nil {
		t.Fatalf("Up: %v", err)
	}

	builds := callsTo(fake, "Build")
	if len(builds) != 1 {
		t.Fatalf("built %d times, want once", len(builds))
	}
	// worker, analytics and docs are built from source and are not
	// part of this review.
	if !slices.Equal(builds[0].Services, []string{"web", "api"}) {
		t.Errorf("built %v, want only the services being started", builds[0].Services)
	}
}

func TestUpStartsEverythingWhenNothingIsChosen(t *testing.T) {
	// The control: without the setting the whole project comes up, and
	// the narration says nothing about counts.
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	m, req, fake := upFixture(t)
	fake.Declared = []string{"web", "api", "db", "cache"}
	atEightServices(t, req)

	rep := &quietReporter{}
	if _, err := m.Up(t.Context(), req, rep); err != nil {
		t.Fatalf("Up: %v", err)
	}

	ups := callsTo(fake, "Up")
	if len(ups) != 1 || len(ups[0].Services) != 0 {
		t.Errorf("started %v, want the whole project", ups)
	}
	for _, step := range rep.steps {
		if strings.Contains(step, "of 8 services") {
			t.Errorf("the narration counts services although all of them are running: %q", step)
		}
	}
}

func TestUpRejectsAServiceThatIsNotDeclared(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	m, req, fake := upFixture(t)
	atEightServices(t, req)
	req.Config.Compose.Services = []string{"wbe"}

	_, err := m.Up(t.Context(), req, &quietReporter{})
	if err == nil {
		t.Fatal("want an error")
	}
	if hint := errs.Hint(err); !strings.Contains(hint, `did you mean "web"?`) {
		t.Errorf("hint = %q, want the suggestion", hint)
	}
	if slices.Contains(fake.Methods(), "Up") {
		t.Error("the services were started for a selection that cannot work")
	}
}

func TestUpRejectsASelectionWithoutTheServiceUnderReview(t *testing.T) {
	// The reviewer opens web.service. A selection that leaves it out
	// would come up and then fail its healthcheck, which says nothing
	// about what is wrong.
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	m, req, _ := upFixture(t)
	atEightServices(t, req)
	req.Config.Compose.Services = []string{"worker"}

	_, err := m.Up(t.Context(), req, &quietReporter{})
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "which is the service a reviewer opens") {
		t.Errorf("error = %q", err)
	}
}

func TestUpUndoesEverythingWhenAPullIsInterrupted(t *testing.T) {
	// Ctrl+C lands in the pull more often than anywhere else, because
	// that is where the waiting is. The other pulls have to stop with
	// it, and what has been created has to go.
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	m, req, fake := upFixture(t)
	fake.Declared = []string{"web", "api", "db", "cache"}
	atEightServices(t, req)
	req.Config.Compose.Services = []string{"web"}
	req.Config.Build.Prebuilt = "ghcr.io/acme/shop-{service}:{sha}"

	ctx, cancel := context.WithCancel(t.Context())
	var once sync.Once
	fake.OnPull = func(string) { once.Do(cancel) }

	_, err := m.Up(ctx, req, &quietReporter{})
	if err == nil {
		t.Fatal("want an error after the interruption")
	}

	// Not built after all: carrying on would turn a Ctrl+C during a
	// pull into the longest wait of the day.
	if builds := callsTo(fake, "Build"); len(builds) != 0 {
		t.Errorf("built %+v after the pull was interrupted", builds)
	}
	if slices.Contains(fake.Methods(), "Up") {
		t.Error("the services were started although the setup was interrupted")
	}
	assertNothingLeftBehind(t, m, req)
}
