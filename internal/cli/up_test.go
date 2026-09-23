package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/workspace"
)

// atConfiguredRepo makes the commands believe they were run in a
// repository that has the given .pit.yaml.
//
// Nothing here may reach the forge: every test using it has to fail
// before the pull request is looked up, or it would talk to GitHub.
func atConfiguredRepo(t *testing.T, pitYAML string) {
	t.Helper()

	root := t.TempDir()
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	write("docker-compose.yml", "services:\n  web:\n    image: nginx\n")
	write(".pit.yaml", pitYAML)

	// Commands that read the configuration find it from the working
	// directory; those that build a sandbox go through currentRepo.
	// A fixture has to serve both.
	t.Chdir(root)

	previous := currentRepo
	currentRepo = func(context.Context) (workspace.Repo, error) {
		return workspace.Repo{
			Root:     root,
			Identity: workspace.Identity{Host: "github.com", Owner: "acme", Name: "shop"},
		}, nil
	}
	t.Cleanup(func() { currentRepo = previous })
}

const withScenarios = `version: 1

web:
  service: web
  port: 3000

data:
  scenarios:
    - name: leer
      description: "nothing but the empty schema"
    - name: standard
      description: "3 users, 20 products, 5 orders"
      apply: ["compose exec -T db psql -f /fixtures/standard.sql"]
    - name: teilerstattung
      description: "an order with a partial refund from two warehouses"
      extends: standard
      apply: ["compose exec -T db psql -f /fixtures/refund.sql"]
  default: standard
`

// TestScenarioTypoIsAnswered is the acceptance criterion for T-405.
func TestScenarioTypoIsAnswered(t *testing.T) {
	atConfiguredRepo(t, withScenarios)

	_, _, err := run(t, "482", "--scenario=tielerstattung")
	if err == nil {
		t.Fatal("want an error for a scenario that is not configured")
	}

	if !strings.Contains(err.Error(), `"tielerstattung"`) {
		t.Errorf("error = %q, want it to quote what was asked for", err)
	}
	hint := errs.Hint(err)
	if !strings.Contains(hint, `did you mean "teilerstattung"?`) {
		t.Errorf("hint = %q, want the suggestion", hint)
	}
	if !strings.Contains(hint, "leer, standard, teilerstattung") {
		t.Errorf("hint = %q, want it to list what is configured", hint)
	}
}

func TestScenarioTypoIsAnsweredBeforeAnythingIsAsked(t *testing.T) {
	// The check has to come before the forge is asked for the pull
	// request: a typo answered after a round trip to GitHub reads as a
	// slow tool. Nothing here can reach the network, so a test that
	// gets an answer at all proves the order.
	atConfiguredRepo(t, withScenarios)

	_, _, err := run(t, "482", "--scenario=nonsense-that-is-nothing-like-it")
	if err == nil {
		t.Fatal("want an error")
	}
	if strings.Contains(err.Error(), "did you mean") {
		t.Errorf("error = %q, want no guess when nothing is close", err)
	}
}

func TestScenarioFlagIsRegistered(t *testing.T) {
	// The control for the two above: they would also pass if --scenario
	// were rejected as an unknown flag before anything looked at its
	// value.
	f := newRootCmd().Flags().Lookup("scenario")
	if f == nil {
		t.Fatal("pit <nr> has no --scenario flag")
	}
	if f.DefValue != "" {
		t.Errorf("--scenario defaults to %q, want the configured default to win", f.DefValue)
	}
}
