package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/thannoz/pit/internal/analysis"
	"github.com/thannoz/pit/internal/diff"
	"github.com/thannoz/pit/internal/review"
	"github.com/thannoz/pit/internal/state"
	"github.com/thannoz/pit/internal/ui"
)

var whatBox = state.Sandbox{PR: 1, Title: "Show the currency after the amount", URL: "http://localhost:48827/", BaseBranch: "main"}

var whatList = review.Checklist{
	Base: "62f34c4aaaa", Head: "d47f78ebbbb", URL: "http://localhost:48827/", Scenario: "standard",
	Items: []review.Item{
		{Number: 1, Kind: analysis.Page, Methods: []string{"GET"}, Path: "/orders/{id}", URL: "http://localhost:48827/orders/42",
			Files: []string{"handlers.go", "money.go"}, Confidence: analysis.Certain},
		{Number: 2, Kind: analysis.Endpoint, Methods: []string{"POST"}, Path: "/api/orders", URL: "http://localhost:48827/api/orders",
			Files: []string{"api/orders.go"}, Confidence: analysis.Certain},
		{Number: 3, Kind: analysis.Page, Path: "/partners/{slug}", Missing: []string{"slug"},
			Files: []string{"app/partners/[slug]/page.tsx", "lib/partners/page.tsx", "a.ts", "b.ts"}, Confidence: analysis.Certain},
		{Number: 4, Kind: analysis.Page, Path: "/orders", URL: "http://localhost:48827/orders",
			Files: []string{"money.go"}, Confidence: analysis.Likely,
			Doubts: []string{"money.go does not serve this address; handlers.go uses it", "second", "third"}},
		{Number: 5, Kind: analysis.Endpoint, Methods: []string{"POST"}, Path: "/…/playlists",
			Files: []string{"server/jellyfin/playlists.go"}, Confidence: analysis.Uncertain,
			Doubts: []string{"the router made in (*Router).routes is handed on, and pit cannot see where it is mounted"}},
	},
	Warnings: []analysis.Warning{
		{Kind: analysis.RemovedAddress, Serious: true, File: "main.go", Address: "DELETE /api/orders/{id}",
			Message: "DELETE /api/orders/{id} no longer answers; main.go served it"},
		{Kind: analysis.SchemaChange, File: "migrations/0002_add_vat_id.sql",
			Message: "migrations/0002_add_vat_id.sql changes the schema (ALTER TABLE orders ADD COLUMN vat_id text); check the data that exists before it runs"},
	},
	Unplaced: []analysis.File{
		{File: diff.File{Path: "main.go"}, Kind: analysis.Code},
		{File: diff.File{Path: "lib/format.go"}, Kind: analysis.Code},
	},
}

// The format of 01-vision.md: numbered addresses, the files in
// brackets, warnings marked with "!". Links are whole URLs, which every
// terminal worth using makes clickable; an address without a working
// link is written without a host, so it cannot be clicked by mistake.
func TestWhatOutput(t *testing.T) {
	var out bytes.Buffer
	writeWhat(ui.New(&out, &out), whatBox, whatList)

	want := `#1 Show the currency after the amount
http://localhost:48827/ · d47f78e into main · scenario standard

Affected by this pull request:
  1. http://localhost:48827/orders/42  (handlers.go, money.go)
  2. POST http://localhost:48827/api/orders  (orders.go)
  3. /partners/{slug}  (app/partners/[slug]/page.tsx, lib/partners/page.tsx, a.ts and 1 more)
       no link: slug needs a value; set it in data.scenarios[standard].params
  4. http://localhost:48827/orders  (money.go)  likely
       ? money.go does not serve this address; handlers.go uses it
       ? second
       ? and 1 more
  5. POST /…/playlists  (playlists.go)  uncertain
       ? the router made in (*Router).routes is handed on, and pit cannot see where it is mounted
  !! DELETE /api/orders/{id} no longer answers; main.go served it
  !  migrations/0002_add_vat_id.sql changes the schema (ALTER TABLE orders ADD COLUMN vat_id text); check the data that exists before it runs

No address found for these; look at them yourself:
  main.go  (code, and see the warning above)
  lib/format.go  (code)
`
	if got := out.String(); got != want {
		t.Errorf("output\n--- got\n%s--- want\n%s", got, want)
	}
}

func TestWhatWithNothingToShow(t *testing.T) {
	var out bytes.Buffer
	writeWhat(ui.New(&out, &out), state.Sandbox{PR: 2, URL: "http://localhost:1/"}, review.Checklist{Head: "abc"})
	if !strings.Contains(out.String(), "Nothing in this pull request leads to an address pit knows.") {
		t.Errorf("output:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "abc into the default branch") {
		t.Errorf("no base branch should say so:\n%s", out.String())
	}
}

// --json has a shape of its own, so that a script does not break when
// something inside pit is renamed.
func TestWhatJSON(t *testing.T) {
	var out bytes.Buffer
	if err := writeWhatJSON(&out, whatBox, whatList); err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	items := doc["items"].([]any)
	first := items[0].(map[string]any)
	for key, want := range map[string]any{
		"number": 1.0, "kind": "page", "path": "/orders/{id}", "url": "http://localhost:48827/orders/42", "confidence": "certain",
	} {
		if first[key] != want {
			t.Errorf("items[0].%s = %v, want %v", key, first[key], want)
		}
	}
	third := items[2].(map[string]any)
	if _, ok := third["url"]; ok {
		t.Errorf("an address without a link has a url: %v", third)
	}
	w := doc["warnings"].([]any)[0].(map[string]any)
	if w["kind"] != "removed" || w["serious"] != true {
		t.Errorf("warnings[0] = %v", w)
	}
	if u := doc["unplaced"].([]any); len(u) != 2 {
		t.Errorf("unplaced = %v", u)
	}
}
