package review_test

import (
	"slices"
	"testing"
	"time"

	"github.com/thannoz/pit/internal/analysis"
	"github.com/thannoz/pit/internal/review"
	"github.com/thannoz/pit/internal/runtime"
	"github.com/thannoz/pit/internal/state"
)

var t0 = time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)

func at(s int) time.Time { return t0.Add(time.Duration(s) * time.Second) }

// Request lines as the servers people run in development write them.
func TestVisitsInTheFormatsServersLogThem(t *testing.T) {
	lines := []string{
		` GET /orders 200 in 45ms`, // Next.js, next dev
		`[GIN] 2026/09/24 - 12:00:01 | 200 |    1.2ms |  172.18.0.1 | GET      "/api/tags"`,
		`172.18.0.1 - - [24/Sep/2026:12:00:02 +0000] "GET /orders/42?tab=items HTTP/1.1" 200 612 "-" "Mozilla/5.0"`, // nginx
		`INFO:     172.18.0.1:51234 - "POST /api/orders HTTP/1.1" 201 Created`,                                      // uvicorn
		`Started GET "/partners/cursor" for 172.18.0.1 at 2026-09-24 12:00:04 +0000`,                                // Rails
		`DELETE /api/orders/7 204 3.456 ms - -`,                                                                     // morgan
		`2026/09/24 12:00:06 "GET http://localhost:8080/health HTTP/1.1" from 172.18.0.1:4321 - 200 2B in 31µs`,     // chi
		`[Thu Sep 24 12:00:07 2026] 172.18.0.1:5555 [200]: GET /index.php`,                                          // php -S
		// Not requests this service answered.
		`calling GET https://api.stripe.com/v1/charges`,
		`ready - started server on 0.0.0.0:3000`,
		`GET`,
	}
	var logged []runtime.LogLine
	for i, l := range lines {
		logged = append(logged, runtime.LogLine{At: at(i), Text: l})
	}
	var got []string
	for _, v := range review.Visits(logged) {
		got = append(got, v.Method+" "+v.Path)
	}
	want := []string{
		"GET /orders", "GET /api/tags", "GET /orders/42", "POST /api/orders", "GET /partners/cursor",
		"DELETE /api/orders/7", "GET /health", "GET /index.php",
	}
	if !slices.Equal(got, want) {
		t.Errorf("visits\n got %q\nwant %q", got, want)
	}
}

func coveredPaths(list review.Checklist, c map[int]time.Time) []string {
	var out []string
	for i := range list.Items {
		if _, ok := c[i]; ok {
			out = append(out, list.Items[i].Address())
		}
	}
	return out
}

func TestWhichItemsAVisitCovers(t *testing.T) {
	list := review.Checklist{Head: "abc", Items: []review.Item{
		{Kind: analysis.Page, Path: "/orders/{id}"},
		{Kind: analysis.Page, Path: "/docs/{path...}"},
		{Kind: analysis.Endpoint, Methods: []string{"POST"}, Path: "/api/orders"},
		{Kind: analysis.Endpoint, Methods: []string{"DELETE"}, Path: "/api/orders/{id}"},
		{Kind: analysis.Page, Path: "/"},
		{Kind: analysis.Endpoint, Path: "/…/playlists"},
		{Kind: analysis.Page, Path: "/settings"},
	}}
	since, probed := at(10), at(20)
	visits := []review.Visit{
		{Method: "GET", Path: "/orders/42", At: at(30)},
		{Method: "GET", Path: "/orders/42/items", At: at(31)}, // a longer address is another page
		{Method: "HEAD", Path: "/docs/install/vite", At: at(32)},
		{Method: "GET", Path: "/api/orders", At: at(33)},   // not the method the item is about
		{Method: "GET", Path: "/api/orders/7", At: at(34)}, // not the method either
		{Method: "GET", Path: "/", At: at(15)},             // pit's probe when it reused the sandbox
		{Method: "GET", Path: "/jellyfin/playlists", At: at(36)},
		{Method: "GET", Path: "/settings/", At: at(5)}, // before this commit came up
		{Method: "POST", Path: "/api/orders", At: at(40)},
	}
	c := review.Covered(list, visits, since, probed)
	if got, want := coveredPaths(list, c), []string{"/orders/{id}", "/docs/{path...}", "POST /api/orders"}; !slices.Equal(got, want) {
		t.Errorf("covered %v, want %v", got, want)
	}
	visits = append(visits, review.Visit{Method: "GET", Path: "/orders/7", At: at(45)})
	if c = review.Covered(list, visits, since, probed); c[0] != at(45) {
		t.Errorf("/orders/{id} covered at %v, want the last visit", c[0])
	}

	// The root counts once pit's probe is behind it; a trailing slash
	// is the same page.
	c = review.Covered(list, append(visits, review.Visit{Method: "GET", Path: "/", At: at(50)},
		review.Visit{Method: "GET", Path: "/settings/", At: at(51)}), since, probed)
	if got := coveredPaths(list, c); !slices.Contains(got, "/") || !slices.Contains(got, "/settings") {
		t.Errorf("covered %v, want / and /settings too", got)
	}
}

func TestRecordingVisits(t *testing.T) {
	store, err := state.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	box := state.Sandbox{PR: 7, RepoRef: "shop-1234", Checked: []state.Check{
		{Address: "/a", SHA: "abc", At: at(1)},                 // looked at by hand, this commit
		{Address: "/b", SHA: "old", At: at(1)},                 // looked at an earlier commit
		{Address: "/c", SHA: "abc", At: at(100), Undone: true}, // taken back after the visit
		{Address: "/d", SHA: "abc", At: at(10), Undone: true},  // taken back before the visit
	}}
	if err := store.Update(func(f *state.File) error { f.Put(box); return nil }); err != nil {
		t.Fatal(err)
	}
	list := review.Checklist{Head: "abc", Items: items("/a", "/b", "/c", "/d", "/e")}
	covered := map[int]time.Time{0: at(50), 1: at(50), 2: at(50), 3: at(50), 4: at(50)}

	box, err = review.RecordVisits(store, box, list, covered)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]state.Check{}
	for _, c := range box.Checked {
		got[c.Address] = c
	}
	for address, want := range map[string]struct {
		sha             string
		visited, undone bool
	}{
		"/a": {"abc", false, false}, // the reviewer's own mark stays
		"/b": {"abc", true, false},  // a visit at this commit replaces the stale one
		"/c": {"abc", false, true},  // the later word is the reviewer's
		"/d": {"abc", true, false},  // looked at again after taking it back
		"/e": {"abc", true, false},
	} {
		c := got[address]
		if c.SHA != want.sha || c.Visited != want.visited || c.Undone != want.undone {
			t.Errorf("%s: %+v, want sha %s visited %v undone %v", address, c, want.sha, want.visited, want.undone)
		}
	}
}

// The two together, as pit what runs them: a mark taken back stays
// taken back against the visits before, and comes back with one after.
// Found by hand in the shop fixture, which the parts alone did not show.
func TestAVisitAfterTakingAMarkBackCounts(t *testing.T) {
	store, err := state.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	box := state.Sandbox{PR: 7, RepoRef: "shop-1234", Checked: []state.Check{{Address: "/", SHA: "abc", At: at(100), Undone: true}}}
	if err := store.Update(func(f *state.File) error { f.Put(box); return nil }); err != nil {
		t.Fatal(err)
	}
	list := review.Checklist{Head: "abc", Items: []review.Item{{Kind: analysis.Page, Path: "/"}}}
	visit := func(s int) review.Visit { return review.Visit{Method: "GET", Path: "/", At: at(s)} }

	for _, tc := range []struct {
		visits []review.Visit
		marked bool
	}{
		{[]review.Visit{visit(50)}, false},
		{[]review.Visit{visit(50), visit(150)}, true},
	} {
		got, err := review.RecordVisits(store, box, list, review.Covered(list, tc.visits, at(0), at(0)))
		if err != nil {
			t.Fatal(err)
		}
		if marked := !got.Checked[0].Undone; marked != tc.marked {
			t.Errorf("visits %v: marked %v, want %v", tc.visits, marked, tc.marked)
		}
	}
}

// What pit inspect loaded is pit's doing, not the reviewer's.
func TestCoveredLeavesOutPitsOwnBrowsing(t *testing.T) {
	list := review.Checklist{Items: []review.Item{{Path: "/orders/{id}", Kind: analysis.Page}}}
	base := time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)
	own := state.Span{From: base.Add(10 * time.Second), To: base.Add(12 * time.Second)}
	visits := []review.Visit{{Method: "GET", Path: "/orders/1001", At: base.Add(11 * time.Second)}}
	if c := review.Covered(list, visits, base, base, own); len(c) != 0 {
		t.Errorf("counted pit's own request: %v", c)
	}
	// Half a second after the span may still be pit's, with the clocks
	// apart; five seconds after is the reviewer.
	visits = append(visits, review.Visit{Method: "GET", Path: "/orders/1001", At: base.Add(12*time.Second + 500*time.Millisecond)})
	if c := review.Covered(list, visits, base, base, own); len(c) != 0 {
		t.Errorf("counted pit's own request, logged a moment late: %v", c)
	}
	visits = append(visits, review.Visit{Method: "GET", Path: "/orders/1001", At: base.Add(17 * time.Second)})
	if c := review.Covered(list, visits, base, base, own); c[0] != base.Add(17*time.Second) {
		t.Errorf("covered = %v", c)
	}
}
