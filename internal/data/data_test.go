package data_test

import (
	"context"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/thannoz/pit/internal/config"
	"github.com/thannoz/pit/internal/data"
	"github.com/thannoz/pit/internal/data/datatest"
	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/proc"
)

// The scenarios from docs/03-datenkonzept.md, minus the one that
// extends another -- inheritance is resolved in a later task.
const configured = `
web:
  service: web
  port: 3000
data:
  service: db
  scenarios:
    - name: leer
      description: "migrations only, no data"
    - name: standard
      description: "3 users, 20 products, 5 orders"
      apply: ["compose exec -T db psql -U app -d app -f /fixtures/standard.sql"]
  default: standard
`

func parse(t *testing.T, text string) *config.Config {
	t.Helper()
	c, err := config.Parse([]byte(text))
	if err != nil {
		t.Fatalf("config.Parse: %v", err)
	}
	return c
}

func sandbox() data.Sandbox {
	return data.Sandbox{
		Project: "pit-github.com-acme-shop-482",
		Files:   []string{"/w/docker-compose.yml", "/state/override.yml"},
		Dir:     "/w",
	}
}

// TestLoadsAScenarioAgainstTheFake is the acceptance criterion for
// T-402: the selected scenario reaches the store with its commands.
func TestLoadsAScenarioAgainstTheFake(t *testing.T) {
	c := parse(t, configured)
	store := datatest.New()

	scenario, err := data.Select(c, "standard")
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if err := store.Apply(t.Context(), sandbox(), scenario, io.Discard, io.Discard); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	calls := store.Calls()
	if len(calls) != 1 {
		t.Fatalf("applied %d scenarios, want 1", len(calls))
	}
	if calls[0].Scenario != "standard" {
		t.Errorf("applied %q, want standard", calls[0].Scenario)
	}
	if calls[0].Project != sandbox().Project {
		t.Errorf("applied to %q, want %q", calls[0].Project, sandbox().Project)
	}
	want := []string{"compose exec -T db psql -U app -d app -f /fixtures/standard.sql"}
	if !slices.Equal(calls[0].Commands, want) {
		t.Errorf("commands = %v, want %v", calls[0].Commands, want)
	}
}

func TestSelectFallsBackToTheDefault(t *testing.T) {
	c := parse(t, configured)

	got, err := data.Select(c, "")
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if got.Name != "standard" {
		t.Errorf("Select(\"\") = %q, want the configured default", got.Name)
	}
}

func TestSelectPrefersWhatWasAskedFor(t *testing.T) {
	c := parse(t, configured)

	got, err := data.Select(c, "leer")
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if got.Name != "leer" {
		t.Errorf("Select(\"leer\") = %q", got.Name)
	}
	if !got.Empty() {
		t.Errorf("a scenario with no apply commands is not empty: %v", got.Commands())
	}
}

func TestSelectWithoutAnyConfiguration(t *testing.T) {
	// Most projects have no data setup at all. Demanding one would
	// make pit useless for them.
	c := parse(t, "web:\n  service: web\n  port: 80\n")

	got, err := data.Select(c, "")
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if !got.Empty() || got.Name != "" {
		t.Errorf("Select = %+v, want nothing to do", got)
	}
}

func TestSelectRejectsAnUnknownName(t *testing.T) {
	c := parse(t, configured)

	_, err := data.Select(c, "standrad")
	if err == nil {
		t.Fatal("want an error for a scenario that is not configured")
	}
	if !strings.Contains(err.Error(), "standrad") {
		t.Errorf("error = %q, want it to quote the name", err)
	}
	if hint := errs.Hint(err); !strings.Contains(hint, "standard") {
		t.Errorf("hint = %q, want it to list the configured scenarios", hint)
	}
}

// inherited is the three-level chain from docs/03-datenkonzept.md,
// written in the order an author would: the base last, to make sure
// nothing relies on the file's order.
const inherited = `
web:
  service: web
  port: 3000
data:
  scenarios:
    - name: teilerstattung
      description: "an order with a partial refund from two warehouses"
      extends: standard
      apply: ["psql -f /fixtures/refund.sql"]
    - name: standard
      extends: leer
      apply: ["psql -f /fixtures/standard.sql"]
    - name: leer
      apply: ["psql -f /fixtures/schema.sql"]
`

// TestSelectResolvesExtendsInOrder is the acceptance criterion for
// T-404: a three-level chain loads base first.
func TestSelectResolvesExtendsInOrder(t *testing.T) {
	c := parse(t, inherited)

	got, err := data.Select(c, "teilerstattung")
	if err != nil {
		t.Fatalf("Select: %v", err)
	}

	want := []string{
		"psql -f /fixtures/schema.sql",
		"psql -f /fixtures/standard.sql",
		"psql -f /fixtures/refund.sql",
	}
	if !slices.Equal(got.Commands(), want) {
		t.Errorf("commands = %v, want %v", got.Commands(), want)
	}
	if got.Name != "teilerstattung" {
		t.Errorf("Name = %q, want the scenario that was asked for", got.Name)
	}
	if got.Description != "an order with a partial refund from two warehouses" {
		t.Errorf("Description = %q, want the one of the scenario asked for", got.Description)
	}
}

func TestSelectKeepsEachCommandWithItsScenario(t *testing.T) {
	// A base fixture that fails has to send the author to the base.
	c := parse(t, inherited)

	got, err := data.Select(c, "teilerstattung")
	if err != nil {
		t.Fatalf("Select: %v", err)
	}

	want := []string{"leer", "standard", "teilerstattung"}
	names := make([]string, 0, len(got.Steps))
	for _, s := range got.Steps {
		names = append(names, s.Scenario)
	}
	if !slices.Equal(names, want) {
		t.Errorf("steps = %v, want %v", names, want)
	}
	if describe := got.Describe(); !strings.Contains(describe, "leer → standard → teilerstattung") {
		t.Errorf("Describe = %q, want it to show the chain", describe)
	}
}

func TestSelectReportsACycle(t *testing.T) {
	// Parse rather than Load: the validation rejects this file, and
	// the resolver still must not walk in circles when it is handed
	// one anyway.
	c := parse(t, `
web:
  service: web
  port: 3000
data:
  scenarios:
    - name: a
      extends: b
    - name: b
      extends: a
`)

	_, err := data.Select(c, "a")
	if err == nil {
		t.Fatal("want an error rather than a walk that never ends")
	}
	if !strings.Contains(err.Error(), "a → b → a") {
		t.Errorf("error = %q, want it to show the cycle", err)
	}
}

// recordingRunner remembers what it was asked to run.
type recordingRunner struct {
	mu     sync.Mutex
	calls  []proc.Command
	err    error
	failAt int // 1-based; 0 means never
}

func (r *recordingRunner) Stream(_ context.Context, c proc.Command, _, _ io.Writer) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.calls = append(r.calls, c)
	if r.failAt == len(r.calls) {
		return errs.New("exit status 1")
	}
	return r.err
}

func TestCommandsRunInsideTheSandbox(t *testing.T) {
	// The author writes "compose exec"; pit fills in the isolation
	// flags it chose, because the author cannot know them.
	r := &recordingRunner{}
	c := parse(t, configured)
	scenario, err := data.Select(c, "standard")
	if err != nil {
		t.Fatalf("Select: %v", err)
	}

	if err := (data.Commands{Runner: r}).Apply(t.Context(), sandbox(), scenario, io.Discard, io.Discard); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if len(r.calls) != 1 {
		t.Fatalf("ran %d commands, want 1", len(r.calls))
	}
	got := r.calls[0]
	if got.Name != "docker" {
		t.Errorf("Name = %q, want docker", got.Name)
	}
	if !slices.Contains(got.Args, sandbox().Project) {
		t.Errorf("args %v do not carry the project name", got.Args)
	}
}

func TestCommandsRunNothingForAnEmptyScenario(t *testing.T) {
	r := &recordingRunner{}
	scenario := data.Scenario{Name: "leer"}

	if err := (data.Commands{Runner: r}).Apply(t.Context(), sandbox(), scenario, io.Discard, io.Discard); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(r.calls) != 0 {
		t.Errorf("ran %d commands for a scenario that asks for none", len(r.calls))
	}
}

func TestCommandsNameTheScenarioThatFailed(t *testing.T) {
	// "entry 1 failed" would send the author to hooks.after_up, which
	// is the wrong half of the file.
	r := &recordingRunner{err: errs.New("exit status 1")}
	scenario := data.Scenario{
		Name:  "standard",
		Steps: []data.Step{{Scenario: "standard", Apply: []string{"compose exec -T db psql -f /fixtures/standard.sql"}}},
	}

	err := (data.Commands{Runner: r}).Apply(t.Context(), sandbox(), scenario, io.Discard, io.Discard)
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), `data.scenarios["standard"].apply`) {
		t.Errorf("error = %q, want it to name the scenario's setting", err)
	}
}

func TestCommandsNameTheScenarioTheFailingCommandBelongsTo(t *testing.T) {
	// The reviewer asked for "teilerstattung", but the fixture that
	// failed is written under "standard". Naming the wrong one sends
	// the author to a file that is not at fault.
	c := parse(t, inherited)
	scenario, err := data.Select(c, "teilerstattung")
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	r := &recordingRunner{failAt: 2}

	err = (data.Commands{Runner: r}).Apply(t.Context(), sandbox(), scenario, io.Discard, io.Discard)
	if err == nil {
		t.Fatal("want an error")
	}

	if !strings.Contains(err.Error(), `data.scenarios["standard"].apply`) {
		t.Errorf("error = %q, want it to name the scenario the command is written under", err)
	}
	if strings.Contains(err.Error(), "teilerstattung") {
		t.Errorf("error = %q, want it not to blame the scenario that was asked for", err)
	}
	if len(r.calls) != 2 {
		t.Errorf("ran %d commands, want it to stop at the one that failed", len(r.calls))
	}
}

func TestCommandsRunTheBaseFirst(t *testing.T) {
	c := parse(t, inherited)
	scenario, err := data.Select(c, "teilerstattung")
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	r := &recordingRunner{}

	if err := (data.Commands{Runner: r}).Apply(t.Context(), sandbox(), scenario, io.Discard, io.Discard); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	want := []string{"/fixtures/schema.sql", "/fixtures/standard.sql", "/fixtures/refund.sql"}
	for i, fixture := range want {
		if i >= len(r.calls) {
			t.Fatalf("ran %d commands, want %d", len(r.calls), len(want))
		}
		if !slices.Contains(r.calls[i].Args, fixture) {
			t.Errorf("command %d is %v, want it to load %s", i+1, r.calls[i].Args, fixture)
		}
	}
}

func TestSelectSuggestsTheNameThatWasMeant(t *testing.T) {
	c := parse(t, inherited)

	_, err := data.Select(c, "tielerstattung")
	if err == nil {
		t.Fatal("want an error")
	}

	hint := errs.Hint(err)
	if !strings.Contains(hint, `did you mean "teilerstattung"?`) {
		t.Errorf("hint = %q, want the suggestion", hint)
	}
	// The guess is what the reader usually needs; the list is what
	// they need when the guess is wrong.
	if !strings.Contains(hint, "teilerstattung, standard, leer") {
		t.Errorf("hint = %q, want it to list what is configured", hint)
	}
}

func TestSelectDoesNotGuessWildly(t *testing.T) {
	// A wrong suggestion is worse than none: it sends the reader
	// looking for something that has nothing to do with what they
	// meant.
	c := parse(t, inherited)

	_, err := data.Select(c, "postgres-dump")
	if err == nil {
		t.Fatal("want an error")
	}
	if strings.Contains(errs.Hint(err), "did you mean") {
		t.Errorf("hint = %q, want no guess", errs.Hint(err))
	}
}
