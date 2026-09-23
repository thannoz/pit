package data

import (
	"strings"

	"github.com/thannoz/pit/internal/config"
	"github.com/thannoz/pit/internal/errs"
)

// Select returns the scenario a sandbox should start from.
//
// requested is what the reviewer asked for; when it is empty the
// configured default is used. A repository that configures neither
// gets an empty scenario and no error: most projects start with no
// data setup at all, and demanding one would make pit useless for them.
func Select(c *config.Config, requested string) (Scenario, error) {
	name := requested
	if name == "" {
		name = c.Data.Default
	}
	if name == "" {
		return Scenario{}, nil
	}

	found, ok := c.Scenario(name)
	if !ok {
		return Scenario{}, unknown(c, name)
	}

	// The chain, not just this scenario: applying only its own
	// commands would produce data quietly missing its base, which is
	// the kind of wrong state the data concept exists to prevent.
	chain, err := c.Chain(name)
	if err != nil {
		return Scenario{}, err
	}

	sc := Scenario{
		Name:        found.Name,
		Description: found.Description,
		Steps:       make([]Step, 0, len(chain)),
	}
	for _, s := range chain {
		sc.Steps = append(sc.Steps, Step{Scenario: s.Name, Apply: s.Apply})
	}
	return sc, nil
}

// unknown reports a name that is not configured, and lists what is. A
// typo is the likeliest reason to land here, and the answer to it is
// the set of real names.
func unknown(c *config.Config, name string) error {
	e := errs.New("there is no scenario named %q", name)

	names := c.ScenarioNames()
	if len(names) == 0 {
		return e.WithHint("no scenarios are configured; add one under data.scenarios in the .pit.yaml")
	}
	return e.WithHint("configured scenarios: %s", strings.Join(names, ", "))
}
