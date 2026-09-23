package cli

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thannoz/pit/internal/data/datatest"
	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/sandbox"
	"github.com/thannoz/pit/internal/state"
)

// recordedWithConfig is a sandbox whose repository is a real directory
// holding the given .pit.yaml, which is what every reset has to read.
func recordedWithConfig(t *testing.T, pr int, scenario, pitYAML string) state.Sandbox {
	t.Helper()

	root := t.TempDir()
	for name, content := range map[string]string{
		"docker-compose.yml": "services:\n  web:\n    image: nginx\n",
		".pit.yaml":          pitYAML,
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	box := recorded(pr, "github.com/acme/shop", "acme-shop-c56680", "refunds", time.Minute)
	box.RepoRoot = root
	box.Scenario = scenario
	return box
}

func dataFake(t *testing.T, boxes ...state.Sandbox) *datatest.Fake {
	t.Helper()

	m, fake := withManager(t, boxes...)
	fake.Declared = []string{"web"}
	// The runtime has to believe the sandbox is up, or the reset is
	// refused before it reaches the data.
	if err := fake.Up(t.Context(), sandbox.RuntimeSandbox(boxes[0]), nil, io.Discard, io.Discard); err != nil {
		t.Fatalf("Up: %v", err)
	}

	store, ok := m.Data.(*datatest.Fake)
	if !ok {
		t.Fatalf("the manager's data store is %T", m.Data)
	}
	return store
}

// TestDataResetLoadsTheScenarioAgain is the acceptance criterion for
// T-410.
func TestDataResetLoadsTheScenarioAgain(t *testing.T) {
	box := recordedWithConfig(t, 482, "standard", withScenarios)
	store := dataFake(t, box)

	out, _, err := run(t, "data", "reset", "482", "--yes")
	if err != nil {
		t.Fatalf("pit data reset: %v\n%s", err, out)
	}

	calls := store.Calls()
	if len(calls) != 1 {
		t.Fatalf("applied %d scenarios, want 1", len(calls))
	}
	if calls[0].Scenario != "standard" {
		t.Errorf("loaded %q, want the one the sandbox was started with", calls[0].Scenario)
	}
	if calls[0].Project != box.Project {
		t.Errorf("loaded into %q, want %q", calls[0].Project, box.Project)
	}
}

func TestDataResetCanSwitchScenario(t *testing.T) {
	box := recordedWithConfig(t, 482, "standard", withScenarios)
	store := dataFake(t, box)

	if _, _, err := run(t, "data", "reset", "482", "--scenario=teilerstattung", "--yes"); err != nil {
		t.Fatalf("pit data reset: %v", err)
	}

	calls := store.Calls()
	if len(calls) != 1 || calls[0].Scenario != "teilerstattung" {
		t.Fatalf("applied %+v, want teilerstattung", calls)
	}
	// Base first, because the scenario extends another: a reset that
	// loaded only the refinement would leave a different state than a
	// fresh review.
	if len(calls[0].Steps) != 2 {
		t.Errorf("steps = %+v, want the chain", calls[0].Steps)
	}

	// And the record has to follow, or `pit ls` would name the state
	// the sandbox was in yesterday.
	out, _, err := run(t, "ls")
	if err != nil {
		t.Fatalf("pit ls: %v", err)
	}
	if !lineWith(out, "#482", "teilerstattung") {
		t.Errorf("the listing still shows the old scenario:\n%s", out)
	}
}

func TestDataResetWithoutAScenarioSaysSo(t *testing.T) {
	// A sandbox started without data was started that way on purpose;
	// filling it now would be a surprise, not a reset.
	box := recordedWithConfig(t, 482, "", withScenarios)
	dataFake(t, box)

	_, _, err := run(t, "data", "reset", "482", "--yes")
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "not started with a scenario") {
		t.Errorf("error = %q", err)
	}
	if hint := errs.Hint(err); !strings.Contains(hint, "--scenario") {
		t.Errorf("hint = %q, want it to point at the way out", hint)
	}
}

func TestDataResetAsksFirst(t *testing.T) {
	// It throws away whatever the reviewer entered by hand, which is
	// the same class of action as pit down.
	box := recordedWithConfig(t, 482, "standard", withScenarios)
	store := dataFake(t, box)

	out, _, err := run(t, "data", "reset", "482")
	if err != nil {
		t.Fatalf("pit data reset: %v", err)
	}
	if len(store.Calls()) != 0 {
		t.Errorf("the data was replaced without an answer: %+v", store.Calls())
	}
	if !strings.Contains(out, "left alone") {
		t.Errorf("output does not say that nothing happened:\n%s", out)
	}
}

func TestDataResetOfAScenarioWithNoCommands(t *testing.T) {
	box := recordedWithConfig(t, 482, "leer", withScenarios)
	store := dataFake(t, box)

	out, _, err := run(t, "data", "reset", "482", "--yes")
	if err != nil {
		t.Fatalf("pit data reset: %v", err)
	}
	if len(store.Calls()) != 0 {
		t.Errorf("something was loaded for a scenario that runs nothing: %+v", store.Calls())
	}
	if !strings.Contains(out, "nothing to load") {
		t.Errorf("output does not say why nothing happened:\n%s", out)
	}
}

func TestDataResetOfASandboxThatIsNotRunning(t *testing.T) {
	box := recordedWithConfig(t, 482, "standard", withScenarios)
	m, fake := withManager(t, box)
	fake.Declared = []string{"web"} // never brought up

	_, _, err := run(t, "data", "reset", "482", "--yes")
	if err == nil {
		t.Fatal("want an error for a sandbox that is not there")
	}
	if !strings.Contains(err.Error(), "nothing is running") {
		t.Errorf("error = %q, want it to say what is missing", err)
	}

	store, _ := m.Data.(*datatest.Fake)
	if len(store.Calls()) != 0 {
		t.Errorf("data was loaded into nothing: %+v", store.Calls())
	}
}
