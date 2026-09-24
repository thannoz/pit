package analysis_test

import (
	"context"
	"errors"
	"io/fs"
	"path"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/thannoz/pit/internal/analysis"
	"github.com/thannoz/pit/internal/analysis/analysistest"
	"github.com/thannoz/pit/internal/diff"
)

// changed classifies paths the way a pull request would bring them.
func changed(ignore []string, paths ...string) []analysis.File {
	var d diff.Diff
	for _, p := range paths {
		d.Files = append(d.Files, diff.File{Path: p, Change: diff.Modified})
	}
	return analysis.Classify(d, ignore)
}

func guide(t *testing.T, files []analysis.File, analyzers ...analysis.Analyzer) analysis.Guide {
	t.Helper()
	g, err := analysis.Entrypoints(context.Background(), fstest.MapFS{}, files, analyzers)
	if err != nil {
		t.Fatalf("Entrypoints: %v", err)
	}
	return g
}

func paths(g analysis.Guide) []string {
	var out []string
	for _, e := range g.Entrypoints {
		out = append(out, e.Path)
	}
	return out
}

func unplaced(g analysis.Guide) []string {
	var out []string
	for _, f := range g.Unplaced {
		out = append(out, f.Path)
	}
	return out
}

// htmlPages is a heuristic of a kind the core has never seen: it reads
// the tree it is given, and every .html file under site/ is a page.
type htmlPages struct{}

func (htmlPages) Name() string { return "static site" }

func (htmlPages) Routes(_ context.Context, fsys fs.FS) ([]analysis.Route, error) {
	var out []analysis.Route
	err := fs.WalkDir(fsys, "site", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || path.Ext(p) != ".html" {
			return err
		}
		out = append(out, analysis.Route{
			Path: "/" + strings.TrimSuffix(strings.TrimPrefix(p, "site/"), ".html"),
			File: p,
			Kind: analysis.Page,
		})
		return nil
	})
	return out, err
}

// The criterion of T-603: a heuristic is exchangeable without a change
// to the core. The same call, with the same files, takes a fake that
// reads nothing and a heuristic that walks a real tree, and gives each
// one's answer.
func TestAHeuristicIsExchangeable(t *testing.T) {
	files := changed(nil, "site/about.html", "site/contact.html", "lib/format.go")
	fsys := fstest.MapFS{
		"site/about.html":   {Data: []byte("<h1>About</h1>")},
		"site/contact.html": {Data: []byte("<h1>Contact</h1>")},
		"site/index.html":   {Data: []byte("<h1>Home</h1>")},
	}

	fake := analysistest.New("Fake", analysis.Route{Path: "/about-us", File: "site/about.html", Kind: analysis.Page})
	for _, tc := range []struct {
		analyzer analysis.Analyzer
		want     []string
	}{
		{fake, []string{"/about-us"}},
		{htmlPages{}, []string{"/about", "/contact"}},
	} {
		g, err := analysis.Entrypoints(context.Background(), fsys, files, []analysis.Analyzer{tc.analyzer})
		if err != nil {
			t.Fatalf("%s: %v", tc.analyzer.Name(), err)
		}
		if got := paths(g); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: entrypoints %v, want %v", tc.analyzer.Name(), got, tc.want)
		}
		for _, e := range g.Entrypoints {
			if e.Analyzer != tc.analyzer.Name() {
				t.Errorf("%s: %s credited to %q", tc.analyzer.Name(), e.Path, e.Analyzer)
			}
		}
	}

	// The tree the heuristic reads is the one the caller passed, not
	// one the core found on its own.
	if seen := fake.Seen(); len(seen) != 1 {
		t.Fatalf("fake asked %d times, want once", len(seen))
	} else if _, err := fs.Stat(seen[0], "site/about.html"); err != nil {
		t.Errorf("fake was handed another tree: %v", err)
	}
}

// A route counts when its file changed; the other routes of the
// project are what a changed component will need later, not what the
// reviewer needs now.
func TestOnlyChangedFilesLeadSomewhere(t *testing.T) {
	a := analysistest.New("Fake",
		analysis.Route{Path: "/orders", File: "app/orders/page.tsx", Kind: analysis.Page},
		analysis.Route{Path: "/settings", File: "app/settings/page.tsx", Kind: analysis.Page},
	)
	g := guide(t, changed(nil, "app/orders/page.tsx"), a)
	if got, want := paths(g), []string{"/orders"}; !reflect.DeepEqual(got, want) {
		t.Errorf("entrypoints %v, want %v", got, want)
	}
}

func TestFilesMeetingAtOneAddressAreListedTogether(t *testing.T) {
	a := analysistest.New("Fake",
		// One file serving two addresses, the way a layout does.
		analysis.Route{Path: "/orders", File: "app/orders/layout.tsx", Kind: analysis.Page},
		analysis.Route{Path: "/orders/[id]", File: "app/orders/layout.tsx", Kind: analysis.Page},
		analysis.Route{Path: "/orders", File: "app/orders/page.tsx", Kind: analysis.Page},
	)
	g := guide(t, changed(nil, "app/orders/page.tsx", "app/orders/layout.tsx"), a)

	want := []analysis.Entrypoint{
		{Path: "/orders", Kind: analysis.Page, Files: []string{"app/orders/layout.tsx", "app/orders/page.tsx"}, Analyzer: "Fake"},
		{Path: "/orders/[id]", Kind: analysis.Page, Files: []string{"app/orders/layout.tsx"}, Analyzer: "Fake"},
	}
	if !reflect.DeepEqual(g.Entrypoints, want) {
		t.Errorf("entrypoints\n got %+v\nwant %+v", g.Entrypoints, want)
	}
}

// Two heuristics can know the same address -- a framework and a
// generic fallback, say. The reviewer should see it once, and the
// order of the analyzers decides whose it is.
func TestTheFirstAnalyzerNamesASharedAddress(t *testing.T) {
	// The later analyzer's file comes first in the diff, so an
	// implementation that went by the order of the files would credit
	// the wrong one.
	first := analysistest.New("First", analysis.Route{Path: "/orders", File: "b/orders.tsx", Kind: analysis.Page})
	second := analysistest.New("Second", analysis.Route{Path: "/orders", File: "a/orders.tsx", Kind: analysis.Page})

	g := guide(t, changed(nil, "a/orders.tsx", "b/orders.tsx"), first, second)
	if len(g.Entrypoints) != 1 {
		t.Fatalf("entrypoints %+v, want one", g.Entrypoints)
	}
	e := g.Entrypoints[0]
	if e.Analyzer != "First" {
		t.Errorf("credited to %q, want First", e.Analyzer)
	}
	if want := []string{"a/orders.tsx", "b/orders.tsx"}; !reflect.DeepEqual(e.Files, want) {
		t.Errorf("files %v, want %v", e.Files, want)
	}
}

// review.ignore keeps a file out of the guide the same way it keeps it
// off the checklist, even when it serves a page.
func TestIgnoredFilesLeadNowhere(t *testing.T) {
	a := analysistest.New("Fake",
		analysis.Route{Path: "/legacy", File: "app/legacy/page.tsx", Kind: analysis.Page},
		analysis.Route{Path: "/orders", File: "app/orders/page.tsx", Kind: analysis.Page},
	)
	g := guide(t, changed([]string{"app/legacy/**"}, "app/legacy/page.tsx", "app/orders/page.tsx"), a)
	if got, want := paths(g), []string{"/orders"}; !reflect.DeepEqual(got, want) {
		t.Errorf("entrypoints %v, want %v", got, want)
	}
	if len(g.Unplaced) != 0 {
		t.Errorf("an ignored file came back as unplaced: %v", unplaced(g))
	}
}

// What no heuristic can place stays in front of the reviewer, in the
// order of the diff. Tests and docs are not on the checklist, so they
// are not unplaced either -- they were never meant to be placed.
func TestFilesNoHeuristicPlacesAreKept(t *testing.T) {
	a := analysistest.New("Fake", analysis.Route{Path: "/orders", File: "app/orders/page.tsx", Kind: analysis.Page})
	g := guide(t, changed(nil,
		"components/Price.tsx",
		"app/orders/page.tsx",
		"app/orders/page.test.tsx",
		"README.md",
		"lib/money.go",
	), a)

	if got, want := unplaced(g), []string{"components/Price.tsx", "lib/money.go"}; !reflect.DeepEqual(got, want) {
		t.Errorf("unplaced %v, want %v", got, want)
	}
	if g.Unplaced[0].Kind != analysis.Component {
		t.Errorf("unplaced file lost its classification: %+v", g.Unplaced[0])
	}
}

func TestWithoutHeuristicsEverythingIsUnplaced(t *testing.T) {
	g := guide(t, changed(nil, "app/orders/page.tsx", "lib/money.go"))
	if len(g.Entrypoints) != 0 {
		t.Errorf("entrypoints %v without a heuristic", paths(g))
	}
	if got, want := unplaced(g), []string{"app/orders/page.tsx", "lib/money.go"}; !reflect.DeepEqual(got, want) {
		t.Errorf("unplaced %v, want %v", got, want)
	}
}

// A guide that silently lacks one framework's pages looks exactly like
// a pull request that does not touch them.
func TestAFailingHeuristicFailsTheGuide(t *testing.T) {
	broken := analysistest.New("Next.js")
	broken.Err = errors.New("app/orders/page.tsx: unexpected token")
	working := analysistest.New("Fake", analysis.Route{Path: "/orders", File: "app/orders/page.tsx", Kind: analysis.Page})

	_, err := analysis.Entrypoints(context.Background(), fstest.MapFS{}, changed(nil, "app/orders/page.tsx"),
		[]analysis.Analyzer{working, broken})
	if err == nil {
		t.Fatal("no error from a heuristic that failed")
	}
	if !errors.Is(err, broken.Err) {
		t.Errorf("cause lost: %v", err)
	}
	if !strings.Contains(err.Error(), "Next.js") {
		t.Errorf("error does not say which heuristic failed: %v", err)
	}
}
