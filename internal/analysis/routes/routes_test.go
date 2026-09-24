package routes

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/thannoz/pit/internal/errs"
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

func TestForSelectsByTheConfiguredName(t *testing.T) {
	for _, framework := range []string{"", Auto} {
		got, err := For(framework)
		if err != nil || len(got) != len(All()) {
			t.Errorf("For(%q) = %d heuristics, %v; want all %d", framework, len(got), err, len(All()))
		}
	}

	got, err := For("nextjs")
	if err != nil {
		t.Fatalf("For(nextjs): %v", err)
	}
	if len(got) != 1 || got[0].Name() != "Next.js" {
		t.Errorf("For(nextjs) = %v", got)
	}
}

func TestEveryFrameworkCanBeSelected(t *testing.T) {
	for _, framework := range Frameworks() {
		if _, err := For(framework); err != nil {
			t.Errorf("For(%q): %v", framework, err)
		}
	}
}

func TestAnUnknownFrameworkIsNamedWithASuggestion(t *testing.T) {
	_, err := For("next")
	if err == nil {
		t.Fatal("no error for a framework pit has no heuristic for")
	}
	var e *errs.Error
	if !errors.As(err, &e) || !strings.Contains(e.Hint, `"nextjs"`) {
		t.Errorf("hint does not suggest nextjs: %v (%+v)", err, e)
	}

	_, err = For("rails")
	if !errors.As(err, &e) || !strings.Contains(e.Hint, "auto, nextjs") {
		t.Errorf("hint does not list what is known: %+v", e)
	}
}

// The same contract for the linkers: a name, and a project in their
// language may simply not be there.
func TestLinkersHoldToTheContract(t *testing.T) {
	seen := map[string]bool{}
	for _, l := range Linkers() {
		if l.Name() == "" {
			t.Errorf("%T has no name", l)
		}
		if seen[l.Name()] {
			t.Errorf("two linkers are called %q", l.Name())
		}
		seen[l.Name()] = true
		for name, fsys := range map[string]fstest.MapFS{
			"empty":     {},
			"docs only": {"README.md": {Data: []byte("# nothing\n")}},
		} {
			links, err := l.Links(context.Background(), fsys)
			if err != nil || len(links) != 0 {
				t.Errorf("%s on %s: %v, %v", l.Name(), name, links, err)
			}
		}
	}
}
