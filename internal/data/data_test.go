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
		t.Errorf("a scenario with no apply commands is not empty: %v", got.Apply)
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

func TestSelectSaysExtendsIsNotResolved(t *testing.T) {
	// Applying only the child's commands would produce data quietly
	// missing its base, which is the failure the data concept exists
	// to prevent.
	c := parse(t, `
web:
  service: web
  port: 3000
data:
  scenarios:
    - name: standard
      apply: ["compose exec -T db psql -f /fixtures/standard.sql"]
    - name: teilerstattung
      extends: standard
      apply: ["compose exec -T db psql -f /fixtures/refund.sql"]
`)

	_, err := data.Select(c, "teilerstattung")
	if err == nil {
		t.Fatal("want an error while extends is unresolved")
	}
	if !strings.Contains(err.Error(), "extends") {
		t.Errorf("error = %q, want it to name the reason", err)
	}
}

// recordingRunner remembers what it was asked to run.
type recordingRunner struct {
	mu    sync.Mutex
	calls []proc.Command
	err   error
}

func (r *recordingRunner) Stream(_ context.Context, c proc.Command, _, _ io.Writer) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, c)
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
	scenario := data.Scenario{Name: "standard", Apply: []string{"compose exec -T db psql -f /fixtures/standard.sql"}}

	err := (data.Commands{Runner: r}).Apply(t.Context(), sandbox(), scenario, io.Discard, io.Discard)
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), `data.scenarios["standard"].apply`) {
		t.Errorf("error = %q, want it to name the scenario's setting", err)
	}
}
