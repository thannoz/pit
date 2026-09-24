package routes

import (
	"context"
	"testing"
	"testing/fstest"
)

// Every heuristic registered here has to hold to the contract the core
// relies on. The tests run over All(), so a new one is checked the
// moment it is added, without anyone remembering to.

func TestNamesAreDistinct(t *testing.T) {
	seen := map[string]bool{}
	for _, a := range All() {
		name := a.Name()
		if name == "" {
			t.Errorf("%T has no name; an address it finds could not say where it came from", a)
		}
		if seen[name] {
			t.Errorf("two heuristics are called %q", name)
		}
		seen[name] = true
	}
}

// A project that does not use a framework is the common case -- every
// project uses at most a few of them -- and it must not be an error.
func TestAProjectWithoutTheFrameworkHasNoRoutes(t *testing.T) {
	projects := map[string]fstest.MapFS{
		"empty": {},
		"docs only": {
			"README.md":     {Data: []byte("# nothing to serve\n")},
			"docs/intro.md": {Data: []byte("still nothing\n")},
		},
	}
	for _, a := range All() {
		for name, fsys := range projects {
			routes, err := a.Routes(context.Background(), fsys)
			if err != nil {
				t.Errorf("%s on %s: %v", a.Name(), name, err)
			}
			if len(routes) != 0 {
				t.Errorf("%s on %s found %v", a.Name(), name, routes)
			}
		}
	}
}
