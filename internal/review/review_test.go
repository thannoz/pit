package review_test

import (
	"context"
	"slices"
	"testing"
	"testing/fstest"

	"github.com/thannoz/pit/internal/analysis"
	"github.com/thannoz/pit/internal/analysis/analysistest"
	"github.com/thannoz/pit/internal/config"
	"github.com/thannoz/pit/internal/diff"
	"github.com/thannoz/pit/internal/review"
)

func build(t *testing.T, yaml, scenario string, routes []analysis.Route, changed ...string) review.Checklist {
	t.Helper()
	cfg, err := config.Parse([]byte(yaml))
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	var d diff.Diff
	for _, p := range changed {
		d.Files = append(d.Files, diff.File{Path: p, Change: diff.Modified})
	}
	c, err := review.Build(context.Background(), review.Input{
		Diff: d, Head: fstest.MapFS{}, Config: cfg, Scenario: scenario, URL: "http://localhost:41234/",
		Analyzers: []analysis.Analyzer{analysistest.New("Fake", routes...)},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return c
}

const shop = `web:
  service: web
  port: 3000
data:
  scenarios:
    - name: standard
      params:
        id: "42"
    - name: empty
review:
  ignore: ["legacy/**"]
`

func TestLinksAreFilledFromTheScenario(t *testing.T) {
	routes := []analysis.Route{
		{Path: "/orders/{id}", File: "app/orders/[id]/page.tsx", Kind: analysis.Page},
		{Path: "/partners/{slug}", File: "app/partners/[slug]/page.tsx", Kind: analysis.Page},
		{Path: "/…/playlists", File: "server/playlists.go", Kind: analysis.Endpoint, Method: "POST",
			Doubts: []analysis.Doubt{{Confidence: analysis.Uncertain, Reason: "mounted under a prefix pit cannot read"}}},
	}
	c := build(t, shop, "standard", routes, "app/orders/[id]/page.tsx", "app/partners/[slug]/page.tsx", "server/playlists.go")

	got := map[string]review.Item{}
	for _, it := range c.Items {
		got[it.Path] = it
	}
	if u := got["/orders/{id}"].URL; u != "http://localhost:41234/orders/42" {
		t.Errorf("/orders/{id} URL = %q", u)
	}
	// No example value: no link, and what is missing.
	if it := got["/partners/{slug}"]; it.URL != "" || !slices.Equal(it.Missing, []string{"slug"}) {
		t.Errorf("/partners/{slug}: URL %q, missing %v", it.URL, it.Missing)
	}
	// An address pit could not read all of is not a link either.
	if u := got["/…/playlists"].URL; u != "" {
		t.Errorf("/…/playlists has a link: %q", u)
	}

	// Another scenario, other values.
	c = build(t, shop, "empty", routes[:1], "app/orders/[id]/page.tsx")
	if it := c.Items[0]; it.URL != "" || !slices.Equal(it.Missing, []string{"id"}) {
		t.Errorf("scenario empty: %+v", it)
	}
}

// The order a reviewer with little time should go in: certain first,
// pages before endpoints, and the numbers follow the order.
func TestTheListIsInTheOrderToWorkThrough(t *testing.T) {
	likely := []analysis.Doubt{{Confidence: analysis.Likely, Reason: "shows only while loading"}}
	c := build(t, shop, "standard", []analysis.Route{
		{Path: "/b", File: "b.tsx", Kind: analysis.Page, Doubts: likely},
		{Path: "/api/x", File: "x.ts", Kind: analysis.Endpoint},
		{Path: "/z", File: "z.tsx", Kind: analysis.Page},
		{Path: "/a", File: "a.tsx", Kind: analysis.Page},
	}, "b.tsx", "x.ts", "z.tsx", "a.tsx")

	var order []string
	for i, it := range c.Items {
		order = append(order, it.Path)
		if it.Number != i+1 {
			t.Errorf("%s is number %d at place %d", it.Path, it.Number, i+1)
		}
	}
	if want := []string{"/a", "/z", "/api/x", "/b"}; !slices.Equal(order, want) {
		t.Errorf("order %v, want %v", order, want)
	}
}

func TestIgnoredFilesAndMigrationsAreNotUnplaced(t *testing.T) {
	c := build(t, shop, "standard", nil, "legacy/old.ts", "db/migrations/0002_vat.sql", "lib/money.go")
	var unplaced []string
	for _, f := range c.Unplaced {
		unplaced = append(unplaced, f.Path)
	}
	if want := []string{"lib/money.go"}; !slices.Equal(unplaced, want) {
		t.Errorf("unplaced %v, want %v", unplaced, want)
	}
	if len(c.Warnings) != 1 || c.Warnings[0].Kind != analysis.SchemaChange {
		t.Errorf("warnings %+v, want the migration", c.Warnings)
	}
}

// A scenario loaded from the reviewer's file brings its example values
// from there; the pull request's file does not have it (T-706).
func TestLinksAreFilledFromValuesGivenApart(t *testing.T) {
	cfg, err := config.Parse([]byte(shop))
	if err != nil {
		t.Fatal(err)
	}
	c, err := review.Build(context.Background(), review.Input{
		Diff:      diff.Diff{Files: []diff.File{{Path: "app/orders/[id]/page.tsx", Change: diff.Modified}}},
		Head:      fstest.MapFS{},
		Config:    cfg,
		Scenario:  "voucher",
		Params:    map[string]string{"id": "1002"},
		URL:       "http://localhost:41234/",
		Analyzers: []analysis.Analyzer{analysistest.New("Fake", analysis.Route{Path: "/orders/{id}", File: "app/orders/[id]/page.tsx", Kind: analysis.Page})},
	})
	if err != nil {
		t.Fatal(err)
	}
	if u := c.Items[0].URL; u != "http://localhost:41234/orders/1002" {
		t.Errorf("URL = %q", u)
	}
}
