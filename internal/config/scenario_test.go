package config_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/thannoz/pit/internal/config"
	"github.com/thannoz/pit/internal/errs"
)

func chainOf(t *testing.T, text, name string) []string {
	t.Helper()

	c, err := config.Parse([]byte(text))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	chain, err := c.Chain(name)
	if err != nil {
		t.Fatalf("Chain(%q): %v", name, err)
	}

	names := make([]string, 0, len(chain))
	for _, s := range chain {
		names = append(names, s.Name)
	}
	return names
}

const threeLevels = `
web:
  service: web
  port: 3000
data:
  scenarios:
    - name: teilerstattung
      extends: standard
    - name: standard
      extends: leer
    - name: leer
`

func TestChainResolvesBaseFirst(t *testing.T) {
	got := chainOf(t, threeLevels, "teilerstattung")

	want := []string{"leer", "standard", "teilerstattung"}
	if !slices.Equal(got, want) {
		t.Errorf("Chain = %v, want %v", got, want)
	}
}

func TestChainOfAScenarioWithoutExtends(t *testing.T) {
	got := chainOf(t, threeLevels, "leer")

	if !slices.Equal(got, []string{"leer"}) {
		t.Errorf("Chain = %v, want just the scenario itself", got)
	}
}

func TestChainReportsACycle(t *testing.T) {
	cases := map[string]struct {
		scenarios string
		start     string
		want      string
	}{
		"a scenario that extends itself": {
			scenarios: "    - name: a\n      extends: a\n",
			start:     "a",
			want:      "a → a",
		},
		"two that extend each other": {
			scenarios: "    - name: a\n      extends: b\n    - name: b\n      extends: a\n",
			start:     "a",
			want:      "a → b → a",
		},
		"a loop the chain only runs into": {
			// The walk starts outside the loop, so the message must
			// not read as if "a" were at fault.
			scenarios: "    - name: a\n      extends: b\n    - name: b\n      extends: c\n    - name: c\n      extends: b\n",
			start:     "a",
			want:      "b → c → b",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c, err := config.Parse([]byte("web:\n  service: web\n  port: 80\ndata:\n  scenarios:\n" + tc.scenarios))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}

			_, err = c.Chain(tc.start)
			if err == nil {
				t.Fatal("want an error rather than a walk that never ends")
			}

			var ce *config.CycleError
			if !errors.As(err, &ce) {
				t.Fatalf("error is %T, want a *config.CycleError", err)
			}
			if got := strings.Join(ce.Path, " → "); got != tc.want {
				t.Errorf("path = %q, want %q", got, tc.want)
			}
			if errs.Hint(err) == "" {
				t.Error("the error carries no hint")
			}
		})
	}
}

func TestChainReportsAParentThatIsNotConfigured(t *testing.T) {
	c, err := config.Parse([]byte("web:\n  service: web\n  port: 80\ndata:\n  scenarios:\n    - name: a\n      extends: gone\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	_, err = c.Chain("a")
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "gone") {
		t.Errorf("error = %q, want it to name the missing parent", err)
	}
}
