package config

import (
	"fmt"
	"slices"
	"strings"

	"github.com/thannoz/pit/internal/errs"
)

// Scenario returns the named scenario.
func (c *Config) Scenario(name string) (Scenario, bool) {
	for _, s := range c.Data.Scenarios {
		if s.Name == name {
			return s, true
		}
	}
	return Scenario{}, false
}

// ScenarioNames lists the configured scenarios in the order they appear
// in the file, which is the order the author found sensible.
func (c *Config) ScenarioNames() []string {
	names := make([]string, 0, len(c.Data.Scenarios))
	for _, s := range c.Data.Scenarios {
		names = append(names, s.Name)
	}
	return names
}

// Chain returns the scenarios that make up name, base first.
//
// extends exists so that scenarios can build on each other instead of
// repeating the base data. The order therefore matters in one
// direction only: the base has to be in place before the scenario that
// refines it runs.
//
// The chain is returned rather than a flat list of commands so that a
// command which fails can be traced to the scenario it was written
// under, which is not necessarily the one the reviewer asked for.
func (c *Config) Chain(name string) ([]Scenario, error) {
	var (
		chain []Scenario
		path  []string
		seen  = map[string]bool{}
	)

	for current := name; current != ""; {
		if seen[current] {
			return nil, cycle(append(path, current))
		}

		s, ok := c.Scenario(current)
		if !ok {
			if len(path) == 0 {
				return nil, errs.New("there is no scenario named %q", current)
			}
			return nil, errs.New("scenario %q extends %q, which is not configured", path[len(path)-1], current).
				WithHint("configured scenarios: %s", strings.Join(c.ScenarioNames(), ", "))
		}

		seen[current] = true
		path = append(path, current)
		chain = append(chain, s)
		current = s.Extends
	}

	slices.Reverse(chain)
	return chain, nil
}

// CycleError is an extends chain that never reaches a base.
//
// It has its own type because the validation reports it with a line
// number, while a caller resolving a single scenario only wants to say
// what went wrong.
type CycleError struct {
	// Path is the walk that led back to a scenario already visited,
	// starting at the one that repeats.
	Path []string
}

func (e *CycleError) Error() string {
	return fmt.Sprintf("scenario %q is defined in terms of itself: %s", e.Path[0], strings.Join(e.Path, " → "))
}

// cycle builds the error from the walk so far, trimmed to the part
// that actually loops: the scenarios before the first repetition are
// only how we got there and would read as if they were at fault.
func cycle(path []string) error {
	if i := slices.Index(path, path[len(path)-1]); i > 0 {
		path = path[i:]
	}
	return errs.Hinted(&CycleError{Path: path},
		"extends has to point at a base; a scenario cannot build on something that already builds on it")
}
