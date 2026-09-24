package cli

import "testing"

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

// A misspelled --scenario used to be answered here, before the pull
// request was even looked up. It cannot be any more: the pull request
// may be the thing that adds the scenario, so the answer waits until
// its .pit.yaml has been read. The message and its suggestion are
// tested where they are now produced, in internal/sandbox.

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
