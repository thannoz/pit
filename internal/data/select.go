package data

import (
	"errors"
	"strings"

	"github.com/thannoz/pit/internal/config"
	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/suggest"
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
		restores, err := c.Restores(s)
		if err != nil {
			return Scenario{}, err
		}
		if len(restores) > 0 {
			sc.Migrate = c.Data.Migrate
		}
		sc.Steps = append(sc.Steps, Step{Scenario: s.Name, Restores: restores, Apply: s.Apply})
	}
	return sc, nil
}

// unknown reports a name that is not configured.
//
// A typo is the likeliest reason to land here, so the nearest real
// name comes first and the full list after it: the guess is what the
// reader usually needs, and the list is what they need when the guess
// is wrong.
func unknown(c *config.Config, name string) error {
	e := errs.New("there is no scenario named %q", name)

	names := c.ScenarioNames()
	switch near := suggest.Closest(name, names); {
	case len(names) == 0:
		e = e.WithHint("no scenarios are configured; add one under data.scenarios in the .pit.yaml")
	case near != "":
		e = e.WithHint("did you mean %q? configured scenarios: %s", near, strings.Join(names, ", "))
	default:
		e = e.WithHint("configured scenarios: %s", strings.Join(names, ", "))
	}
	return unknownScenario{e}
}

// unknownScenario is the error of a scenario that is not configured,
// for a caller that has something better to say about it. It says what
// the error it carries says, hint and all.
type unknownScenario struct{ err *errs.Error }

func (u unknownScenario) Error() string { return u.err.Error() }
func (u unknownScenario) Unwrap() error { return u.err }

// IsUnknownScenario reports whether err is about a scenario that is not
// configured.
func IsUnknownScenario(err error) bool {
	var u unknownScenario
	return errors.As(err, &u)
}
