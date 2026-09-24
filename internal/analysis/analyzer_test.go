package analysis_test

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"reflect"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

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
	g, err := analysis.Entrypoints(context.Background(), fstest.MapFS{}, files, analyzers, nil)
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
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil // no site/, no pages: not an error
	}
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
		g, err := analysis.Entrypoints(context.Background(), fsys, files, []analysis.Analyzer{tc.analyzer}, nil)
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
		{Path: "/orders", Kind: analysis.Page, Files: []string{"app/orders/layout.tsx", "app/orders/page.tsx"}, Analyzer: "Fake", Confidence: analysis.Certain},
		{Path: "/orders/[id]", Kind: analysis.Page, Files: []string{"app/orders/layout.tsx"}, Analyzer: "Fake", Confidence: analysis.Certain},
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
		[]analysis.Analyzer{working, broken}, nil)
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

// hunked is a changed file with its changes at the given new lines.
func hunked(path string, hunks ...diff.Hunk) analysis.File {
	return analysis.Classify(diff.Diff{Files: []diff.File{{Path: path, Change: diff.Modified, Hunks: hunks}}}, nil)[0]
}

func at(start, count int) diff.Hunk { return diff.Hunk{New: diff.Range{Start: start, Count: count}} }

// A file that registers forty routes has changed for the ones whose
// lines changed, not for all forty. The criterion came from ollama's
// server/routes.go.
func TestARouteWithLinesCountsOnlyWhenTheyChanged(t *testing.T) {
	routes := analysistest.New("Fake",
		analysis.Route{Path: "/api/pull", File: "server/routes.go", Kind: analysis.Endpoint, Method: "POST", Lines: diff.Range{Start: 10, Count: 1}},
		analysis.Route{Path: "/api/push", File: "server/routes.go", Kind: analysis.Endpoint, Method: "POST", Lines: diff.Range{Start: 11, Count: 1}},
		analysis.Route{Path: "/api/push", File: "server/push.go", Kind: analysis.Endpoint, Method: "POST", Lines: diff.Range{Start: 20, Count: 15}},
		analysis.Route{Path: "/api/tags", File: "server/routes.go", Kind: analysis.Endpoint, Method: "GET", Lines: diff.Range{Start: 12, Count: 1}},
		analysis.Route{Path: "/api/tags", File: "server/routes.go", Kind: analysis.Endpoint, Method: "HEAD", Lines: diff.Range{Start: 13, Count: 1}},
	)
	for _, tc := range []struct {
		name  string
		files []analysis.File
		want  []string
	}{
		{"one line of the registrations", []analysis.File{hunked("server/routes.go", at(11, 1))}, []string{"/api/push"}},
		{"inside a handler", []analysis.File{hunked("server/push.go", at(30, 2))}, []string{"/api/push"}},
		{"next to a handler", []analysis.File{hunked("server/push.go", at(40, 2), at(1, 3))}, nil},
		// A deletion has no new lines, only a place. Between two
		// one-line registrations it belongs to neither: the pit shop
		// fixture removed DELETE /api/orders/{id} and the guide listed
		// POST /api/orders, the line above it.
		{"a deletion between two", []analysis.File{hunked("server/routes.go", at(11, 0))}, nil},
		// Inside a handler's body, it changes the handler.
		{"a deletion inside a handler", []analysis.File{hunked("server/push.go", at(25, 0))}, []string{"/api/push"}},
		{"a deletion at a handler's edge", []analysis.File{hunked("server/push.go", at(34, 0))}, nil},
		{"a deletion at the top", []analysis.File{hunked("server/routes.go", at(0, 0))}, nil},
	} {
		g := guide(t, tc.files, routes)
		if got := paths(g); !slices.Equal(got, tc.want) {
			t.Errorf("%s: entrypoints %v, want %v", tc.name, got, tc.want)
		}
	}

	// Methods meet at one address.
	g := guide(t, []analysis.File{hunked("server/routes.go", at(12, 2))}, routes)
	if len(g.Entrypoints) != 1 || !slices.Equal(g.Entrypoints[0].Methods, []string{"GET", "HEAD"}) {
		t.Errorf("entrypoints %+v, want /api/tags with GET and HEAD", g.Entrypoints)
	}
	// A route without lines is the whole file, as before.
	whole := analysistest.New("Fake", analysis.Route{Path: "/orders", File: "app/orders/page.tsx", Kind: analysis.Page})
	if got := paths(guide(t, []analysis.File{hunked("app/orders/page.tsx", at(500, 1))}, whole)); !slices.Equal(got, []string{"/orders"}) {
		t.Errorf("a route without lines: %v", got)
	}
}

func use(from, to string) analysis.Link { return analysis.Link{From: from, To: to} }

func guideWith(t *testing.T, files []analysis.File, a analysis.Analyzer, links ...analysis.Link) analysis.Guide {
	t.Helper()
	g, err := analysis.Entrypoints(context.Background(), fstest.MapFS{}, files,
		[]analysis.Analyzer{a}, []analysis.Linker{analysistest.Links("Fake", links...)})
	if err != nil {
		t.Fatalf("Entrypoints: %v", err)
	}
	return g
}

func pages(paths ...string) *analysistest.Fake {
	var routes []analysis.Route
	for _, p := range paths {
		routes = append(routes, analysis.Route{Path: "/" + p, File: "app/" + p + "/page.tsx", Kind: analysis.Page})
	}
	return analysistest.New("Fake", routes...)
}

// The criterion of T-606: a changed leaf component leads to the three
// pages that render it, however deep it sits in each.
func TestALeafComponentLeadsToThePagesThatRenderIt(t *testing.T) {
	g := guideWith(t, changed(nil, "components/Price.tsx"),
		pages("cart", "checkout", "orders", "settings"),
		use("app/cart/page.tsx", "components/Price.tsx"),
		use("components/Summary.tsx", "components/Price.tsx"),
		use("app/checkout/page.tsx", "components/Summary.tsx"),
		use("components/OrderRow.tsx", "components/Summary.tsx"),
		use("components/OrderTable.tsx", "components/OrderRow.tsx"),
		use("app/orders/page.tsx", "components/OrderTable.tsx"),
		use("app/settings/page.tsx", "components/Avatar.tsx"),
	)
	if got, want := paths(g), []string{"/cart", "/checkout", "/orders"}; !slices.Equal(got, want) {
		t.Errorf("entrypoints %v, want %v", got, want)
	}
	if len(g.Unplaced) != 0 {
		t.Errorf("unplaced %v", unplaced(g))
	}
	// Each says how.
	want := map[string]analysis.Trail{
		"/cart":     {"components/Price.tsx", "app/cart/page.tsx"},
		"/checkout": {"components/Price.tsx", "components/Summary.tsx", "app/checkout/page.tsx"},
		"/orders":   {"components/Price.tsx", "components/Summary.tsx", "components/OrderRow.tsx", "components/OrderTable.tsx", "app/orders/page.tsx"},
	}
	for _, e := range g.Entrypoints {
		if len(e.Via) != 1 || !slices.Equal(e.Via[0], want[e.Path]) {
			t.Errorf("%s via %v, want %v", e.Path, e.Via, want[e.Path])
		}
		if !slices.Equal(e.Files, []string{"components/Price.tsx"}) {
			t.Errorf("%s files %v: the changed file, not the page", e.Path, e.Files)
		}
	}
}

// Imports go round in circles in real projects. The walk ends anyway,
// and still finds what is beyond the circle.
func TestACycleEnds(t *testing.T) {
	g := guideWith(t, changed(nil, "lib/a.ts"), pages("orders"),
		use("lib/b.ts", "lib/a.ts"),
		use("lib/a.ts", "lib/b.ts"),
		use("lib/c.ts", "lib/b.ts"),
		use("lib/b.ts", "lib/c.ts"),
		use("app/orders/page.tsx", "lib/c.ts"),
	)
	if got := paths(g); !slices.Equal(got, []string{"/orders"}) {
		t.Errorf("entrypoints %v", got)
	}
}

// Past a page lie the pages that link to it, which is not what the
// change touched.
func TestTheWalkStopsAtTheFirstAddress(t *testing.T) {
	g := guideWith(t, changed(nil, "components/Price.tsx"), pages("cart", "home"),
		use("app/cart/page.tsx", "components/Price.tsx"),
		use("app/home/page.tsx", "app/cart/page.tsx"),
	)
	if got := paths(g); !slices.Equal(got, []string{"/cart"}) {
		t.Errorf("entrypoints %v, want only /cart", got)
	}
}

func TestTheWalkIsBounded(t *testing.T) {
	chain := []analysis.Link{}
	prev := "lib/0.ts"
	for i := 1; i <= analysis.MaxDepth+2; i++ {
		next := fmt.Sprintf("lib/%d.ts", i)
		chain = append(chain, use(next, prev))
		prev = next
	}
	chain = append(chain, use("app/far/page.tsx", prev))
	g := guideWith(t, changed(nil, "lib/0.ts"), pages("far"), chain...)
	if len(g.Entrypoints) != 0 {
		t.Errorf("found %v %d files away; MaxDepth is %d", paths(g), analysis.MaxDepth+3, analysis.MaxDepth)
	}
	if got := unplaced(g); !slices.Equal(got, []string{"lib/0.ts"}) {
		t.Errorf("unplaced %v", got)
	}
}

// A file every page uses lists the nearest few, and says how many there
// are in all.
func TestAFileUsedEverywhereListsTheNearest(t *testing.T) {
	var names []string
	var links []analysis.Link
	for i := range analysis.MaxReach + 5 {
		name := fmt.Sprintf("p%02d", i)
		names = append(names, name)
		if i%2 == 0 {
			links = append(links, use("app/"+name+"/page.tsx", "lib/cn.ts"))
		} else {
			links = append(links, use("components/"+name+".tsx", "lib/cn.ts"), use("app/"+name+"/page.tsx", "components/"+name+".tsx"))
		}
	}
	g := guideWith(t, changed(nil, "lib/cn.ts"), pages(names...), links...)

	if len(g.Entrypoints) != analysis.MaxReach {
		t.Fatalf("%d entrypoints, want %d", len(g.Entrypoints), analysis.MaxReach)
	}
	if want := []analysis.Reach{{File: "lib/cn.ts", Addresses: analysis.MaxReach + 5}}; !slices.Equal(g.Wide, want) {
		t.Errorf("wide %v, want %v", g.Wide, want)
	}
	// The eight direct users come first; two of the indirect ones fill
	// the list.
	direct := 0
	for _, e := range g.Entrypoints {
		if len(e.Via[0]) == 2 {
			direct++
		}
	}
	if direct != 8 {
		t.Errorf("%d direct users listed, want all 8", direct)
	}
}

// In Go a link is a line using a declaration. The walk goes from the
// lines that changed to the declarations they are in, to the lines that
// use those, and so on -- not to every line of every file.
func TestLinksNarrowedToLines(t *testing.T) {
	handlers := analysistest.New("Fake",
		analysis.Route{Path: "/api/chat", File: "server/routes.go", Method: "POST", Kind: analysis.Endpoint, Lines: diff.Range{Start: 10, Count: 1}},
		analysis.Route{Path: "/api/chat", File: "server/chat.go", Method: "POST", Kind: analysis.Endpoint, Lines: diff.Range{Start: 5, Count: 20}},
		analysis.Route{Path: "/api/tags", File: "server/tags.go", Method: "GET", Kind: analysis.Endpoint, Lines: diff.Range{Start: 5, Count: 20}},
	)
	links := []analysis.Link{
		// chat.go line 12 calls openai.ToChat (openai/chat.go 1-30).
		{From: "server/chat.go", At: diff.Range{Start: 12, Count: 1}, To: "openai/chat.go", Target: diff.Range{Start: 1, Count: 30}},
		// tags.go line 9 calls openai.ToList (openai/chat.go 40-60).
		{From: "server/tags.go", At: diff.Range{Start: 9, Count: 1}, To: "openai/chat.go", Target: diff.Range{Start: 40, Count: 21}},
	}
	for _, tc := range []struct {
		change diff.Hunk
		want   []string
	}{
		{at(20, 3), []string{"/api/chat"}},
		{at(45, 1), []string{"/api/tags"}},
		{at(35, 1), nil}, // between the two: nothing uses it
	} {
		g := guideWith(t, []analysis.File{hunked("openai/chat.go", tc.change)}, handlers, links...)
		if got := paths(g); !slices.Equal(got, tc.want) {
			t.Errorf("change at %d: entrypoints %v, want %v", tc.change.New.Start, got, tc.want)
		}
	}
}

// One function calling another in the same file is a step of the walk,
// not a file of the trail.
func TestATrailNamesEachFileOnce(t *testing.T) {
	handlers := analysistest.New("Fake",
		analysis.Route{Path: "/api/chat", File: "server/chat.go", Kind: analysis.Endpoint, Lines: diff.Range{Start: 5, Count: 10}})
	g := guideWith(t, []analysis.File{hunked("lib/log.go", at(3, 1))}, handlers,
		analysis.Link{From: "server/chat.go", At: diff.Range{Start: 30, Count: 1}, To: "lib/log.go", Target: diff.Range{Start: 1, Count: 10}},
		analysis.Link{From: "server/chat.go", At: diff.Range{Start: 8, Count: 1}, To: "server/chat.go", Target: diff.Range{Start: 25, Count: 10}},
	)
	if len(g.Entrypoints) != 1 || !slices.Equal(g.Entrypoints[0].Via[0], analysis.Trail{"lib/log.go", "server/chat.go"}) {
		t.Errorf("entrypoints %+v", g.Entrypoints)
	}
}

func TestAFailingLinkerFailsTheGuide(t *testing.T) {
	broken := analysistest.Links("TypeScript")
	broken.Err = errors.New("tsconfig.json: unexpected end")
	_, err := analysis.Entrypoints(context.Background(), fstest.MapFS{}, changed(nil, "lib/a.ts"),
		[]analysis.Analyzer{pages("orders")}, []analysis.Linker{broken})
	if !errors.Is(err, broken.Err) || !strings.Contains(err.Error(), "TypeScript") {
		t.Errorf("err = %v", err)
	}
}

// Two functions of one file calling each other: a step inside a file
// does not lengthen the trail, so only the visited set ends this walk.
func TestMutualRecursionInOneFileEnds(t *testing.T) {
	handlers := analysistest.New("Fake",
		analysis.Route{Path: "/api/chat", File: "server/chat.go", Kind: analysis.Endpoint, Lines: diff.Range{Start: 1, Count: 5}})
	done := make(chan analysis.Guide, 1)
	go func() {
		done <- guideWith(t, []analysis.File{hunked("lib/walk.go", at(3, 1))}, handlers,
			// even (1-10) and odd (20-30) call each other.
			analysis.Link{From: "lib/walk.go", At: diff.Range{Start: 5, Count: 1}, To: "lib/walk.go", Target: diff.Range{Start: 20, Count: 11}},
			analysis.Link{From: "lib/walk.go", At: diff.Range{Start: 25, Count: 1}, To: "lib/walk.go", Target: diff.Range{Start: 1, Count: 10}},
			analysis.Link{From: "server/chat.go", At: diff.Range{Start: 3, Count: 1}, To: "lib/walk.go", Target: diff.Range{Start: 20, Count: 11}},
		)
	}()
	select {
	case g := <-done:
		if got := paths(g); !slices.Equal(got, []string{"/api/chat"}) {
			t.Errorf("entrypoints %v", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the walk did not end")
	}
}

func entry(t *testing.T, g analysis.Guide, path string) analysis.Entrypoint {
	t.Helper()
	for _, e := range g.Entrypoints {
		if e.Path == path {
			return e
		}
	}
	t.Fatalf("no entrypoint %s in %v", path, paths(g))
	return analysis.Entrypoint{}
}

// A file serving the address is certain; a file reached through
// another is likely, and says through which.
func TestConfidenceFollowsTheEvidence(t *testing.T) {
	g := guideWith(t, changed(nil, "app/cart/page.tsx", "components/Price.tsx", "lib/money.ts"),
		pages("cart", "checkout"),
		use("app/cart/page.tsx", "components/Price.tsx"),
		use("app/checkout/page.tsx", "components/Price.tsx"),
		use("components/Price.tsx", "lib/money.ts"),
	)
	cart := entry(t, g, "/cart")
	if cart.Confidence != analysis.Certain || len(cart.Doubts) != 0 {
		t.Errorf("/cart: %s %v; its page changed, so it is certain, and says nothing else", cart.Confidence, cart.Doubts)
	}
	checkout := entry(t, g, "/checkout")
	if checkout.Confidence != analysis.Likely {
		t.Errorf("/checkout: %s, want likely", checkout.Confidence)
	}
	want := []string{
		"components/Price.tsx does not serve this address; app/checkout/page.tsx uses it",
		"lib/money.ts does not serve this address; it is used through 2 files, the last app/checkout/page.tsx",
	}
	if !slices.Equal(checkout.Doubts, want) {
		t.Errorf("/checkout doubts\n got %q\nwant %q", checkout.Doubts, want)
	}
}

// The heuristic's doubts travel with the route, and the lowest one
// decides.
func TestAHeuristicsDoubtsDecide(t *testing.T) {
	a := analysistest.New("Fake",
		analysis.Route{Path: "/app/{slug}", File: "app/app/[slug]/page.tsx", Kind: analysis.Page, Doubts: []analysis.Doubt{
			{Confidence: analysis.Likely, Reason: "shows only while loading"},
			{Confidence: analysis.Uncertain, Reason: "middleware.ts can rewrite requests to this address"},
		}},
		analysis.Route{Path: "/api/links", File: "app/api/links/route.ts", Kind: analysis.Endpoint},
	)
	g := guide(t, changed(nil, "app/app/[slug]/page.tsx", "app/api/links/route.ts"), a)

	app := entry(t, g, "/app/{slug}")
	if app.Confidence != analysis.Uncertain || !slices.Equal(app.Doubts, []string{"middleware.ts can rewrite requests to this address"}) {
		t.Errorf("/app/{slug}: %s %q", app.Confidence, app.Doubts)
	}
	if api := entry(t, g, "/api/links"); api.Confidence != analysis.Certain {
		t.Errorf("/api/links: %s, want certain", api.Confidence)
	}
}

// The best evidence wins, and only its reasons are kept: an address a
// changed file serves itself does not become doubtful because another
// changed file reaches it the long way round.
func TestTheBestEvidenceWins(t *testing.T) {
	for _, order := range [][]string{
		{"lib/money.ts", "app/cart/page.tsx"},
		{"app/cart/page.tsx", "lib/money.ts"},
	} {
		g := guideWith(t, changed(nil, order...), pages("cart"), use("app/cart/page.tsx", "lib/money.ts"))
		cart := entry(t, g, "/cart")
		if cart.Confidence != analysis.Certain || len(cart.Doubts) != 0 {
			t.Errorf("order %v: /cart %s %q", order, cart.Confidence, cart.Doubts)
		}
	}
}

func TestAWideFileSaysSo(t *testing.T) {
	var names []string
	var links []analysis.Link
	for i := range analysis.MaxReach + 1 {
		name := fmt.Sprintf("p%02d", i)
		names = append(names, name)
		links = append(links, use("app/"+name+"/page.tsx", "lib/cn.ts"))
	}
	g := guideWith(t, changed(nil, "lib/cn.ts"), pages(names...), links...)
	e := g.Entrypoints[0]
	if !slices.ContainsFunc(e.Doubts, func(d string) bool {
		return strings.HasPrefix(d, fmt.Sprintf("one of %d addresses lib/cn.ts reaches", analysis.MaxReach+1))
	}) {
		t.Errorf("doubts %q do not say the file is used everywhere", e.Doubts)
	}
}
