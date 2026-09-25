package report

import (
	"strings"
	"testing"

	"github.com/thannoz/pit/internal/inspect"
	"github.com/thannoz/pit/internal/notes"
)

var voucher = notes.Note{
	ID: 1, Text: "The refund total ignores the voucher", URL: "http://localhost:43077/orders/1002",
	SHA: "3701136aa94", Scenario: "refunded",
	Problems: []inspect.Problem{
		{Kind: inspect.Exception, Level: "error", Text: "TypeError: Cannot set properties of null (setting 'textContent')", Source: "http://localhost:43077/app.js:12"},
		{Kind: inspect.Request, Level: "error", Method: "GET", URL: "http://localhost:43077/api/basket", Status: 404, Text: "Not Found"},
		{Kind: inspect.Console, Level: "warning", Text: "voucher code is deprecated", Source: "http://localhost:43077/app.js:3"},
		{Kind: inspect.Request, Level: "error", Method: "GET", URL: "https://cdn.example/logo.png", Text: "net::ERR_NAME_NOT_RESOLVED"},
		{Kind: inspect.Browser, Level: "error", Text: "Refused to load the script because it violates the Content Security Policy", URL: "http://localhost:43077/orders/1002"},
	},
	Pending: []string{"GET http://localhost:43077/api/slow"},
}

// TestAComment is the acceptance criterion for T-804: the comment reads
// as one a reviewer would have written, with nothing left to fix by
// hand -- no addresses of the reviewer's machine, no pit internals, a
// way for the author to see each thing for themselves.
func TestAComment(t *testing.T) {
	list := []notes.Note{
		{ID: 4, Text: "No way back from the order page", URL: "http://localhost:43077/orders/1001?tab=items", SHA: "3701136aa94", Scenario: "refunded", Uncaptured: "not asked to"},
		voucher,
		{ID: 2, Text: "Totals overlap\non a narrow window", URL: "http://localhost:43077/", SHA: "3701136aa94", Scenario: "refunded"},
	}
	got := Comment(Input{PR: 7, Notes: list, Head: "3701136aa94", Pictures: map[int]string{1: "https://github.com/user-attachments/assets/1.png"}})
	want := "### Review notes\n" +
		"\n" +
		"3 things I found while trying out 3701136 locally.\n" +
		"\n" +
		"To see them yourself, with [pit](https://github.com/thannoz/pit):\n" +
		"\n" +
		"```sh\n" +
		"pit 7 --scenario=refunded\n" +
		"```\n" +
		"\n" +
		"#### 1. The refund total ignores the voucher\n" +
		"\n" +
		"Page: `/orders/1002`\n" +
		"\n" +
		"- **Uncaught exception:** `TypeError: Cannot set properties of null (setting 'textContent')` at `/app.js:12`\n" +
		"- **Failed request:** `GET /api/basket` answered 404 Not Found\n" +
		"- **Console warning:** `voucher code is deprecated` at `/app.js:3`\n" +
		"- **Failed request:** `GET https://cdn.example/logo.png`: net::ERR_NAME_NOT_RESOLVED\n" +
		"- **Browser error:** `Refused to load the script because it violates the Content Security Policy` at `/orders/1002`\n" +
		"- **No answer:** `GET /api/slow` was still waiting after several seconds\n" +
		"\n" +
		"![Screenshot of /orders/1002](https://github.com/user-attachments/assets/1.png)\n" +
		"\n" +
		"#### 2. Totals overlap on a narrow window\n" +
		"\n" +
		"Page: `/`\n" +
		"\n" +
		"The browser reported no errors on the page.\n" +
		"\n" +
		"#### 4. No way back from the order page\n" +
		"\n" +
		"Page: `/orders/1001?tab=items`\n"
	if got != want {
		t.Errorf("the comment is\n%s\nwant\n%s", got, want)
	}
	if strings.Contains(got, "localhost") {
		t.Error("the comment gives the reviewer's address")
	}
}

// Where notes differ, each tells its own: the commit, the data.
func TestNotesTakenOnDifferentSetups(t *testing.T) {
	other := notes.Note{ID: 2, Text: "Totals overlap", URL: "http://localhost:43077/", SHA: "9a8b7c6d5e4", Scenario: "empty", Snapshot: "sn_7f3a1b", Edited: true}
	got := Comment(Input{PR: 7, Notes: []notes.Note{voucher, other}, Head: "9a8b7c6d5e4"})
	for _, want := range []string{
		"2 things I found while trying this out locally.\n\n#### 1.",
		"Page: `/orders/1002` · commit 3701136 (the pull request has moved on since)\n",
		"Page: `/` · commit 9a8b7c6\n",
		"To see it yourself, with [pit](https://github.com/thannoz/pit):\n\n```sh\npit 7 --scenario=refunded\n```\n",
		"```sh\npit 7 --scenario=empty\n```\n\nThe data came from a snapshot of mine, `sn_7f3a1b`, which only I have; the scenario is the one it was taken on. I had changed the data by hand before this; the scenario alone may not show it.\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the comment lacks %q:\n%s", want, got)
		}
	}
	if strings.Count(got, "```sh") != 2 {
		t.Errorf("each note needs its own command:\n%s", got)
	}
}

// What all notes share is told once, above them.
func TestSharedCaveatsAreToldOnce(t *testing.T) {
	a, b := voucher, voucher
	b.ID = 2
	a.Edited, b.Edited = true, true
	got := Comment(Input{PR: 7, Notes: []notes.Note{a, b}})
	if strings.Count(got, "changed the data by hand") != 1 || strings.Count(got, "```sh") != 1 ||
		!strings.Contains(got, "```\n\nI had changed the data by hand before this; the scenario alone may not show it.\n\n#### 1.") {
		t.Errorf("the comment is\n%s", got)
	}
}

func TestOneNote(t *testing.T) {
	n := notes.Note{ID: 3, Text: "Checkout button does nothing", URL: "http://localhost:43077/cart", SHA: "3701136aa94"}
	got := Comment(Input{PR: 482, Notes: []notes.Note{n}})
	for _, want := range []string{
		"One thing I found while trying out 3701136 locally.\n",
		"To see it yourself, with [pit](https://github.com/thannoz/pit):\n\n```sh\npit 482\n```\n",
		"#### 3. Checkout button does nothing\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the comment lacks %q:\n%s", want, got)
		}
	}
}

// Notes without a recorded commit still make a sentence.
func TestNotesWithoutACommit(t *testing.T) {
	n := notes.Note{ID: 1, Text: "Checkout button does nothing", URL: "http://localhost:43077/cart"}
	if got := Comment(Input{PR: 482, Notes: []notes.Note{n}, Head: "9a8b7c6d5e4"}); !strings.Contains(got, "One thing I found while trying this out locally.\n") {
		t.Errorf("the comment is\n%s", got)
	}
}

func TestNoNotesNoComment(t *testing.T) {
	if got := Comment(Input{PR: 7}); got != "" {
		t.Errorf("got %q", got)
	}
}

// A message from the page is set as code whatever it holds, and kept
// to a line of a sensible length.
func TestMessagesAreSetSafely(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"plain", "`plain`"},
		{"use `npm ci`", "`` use `npm ci` ``"},
		{"run `npm ci` first", "``run `npm ci` first``"},
		{"`quoted`", "`` `quoted` ``"},
		{"a ``b`` c", "```a ``b`` c```"},
	} {
		if got := code(tc.in); got != tc.want {
			t.Errorf("code(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	long := strings.Repeat("x", 1000)
	if got := clip("line one\n  line two"); got != "line one line two" {
		t.Errorf("clip = %q", got)
	}
	if got := clip(long); len([]rune(got)) != maxText+1 || !strings.HasSuffix(got, "…") {
		t.Errorf("clip kept %d", len([]rune(got)))
	}
	n := voucher
	n.Problems = []inspect.Problem{{Kind: inspect.Exception, Level: "error", Text: "Error: " + long + "\n    at x (app.js:1)"}}
	if got := Comment(Input{PR: 7, Notes: []notes.Note{n}}); strings.Contains(got, "at x (app.js:1)") || strings.Count(got, "x") > maxText+100 {
		t.Errorf("a long message went in whole")
	}
	n.Text = "Brackets [in] the ![alt]"
	n.URL = "http://localhost:43077/search?q=[x]"
	if got := Comment(Input{PR: 7, Notes: []notes.Note{n}, Pictures: map[int]string{1: "https://example.com/1.png"}}); !strings.Contains(got, `![Screenshot of /search?q=\[x\]](https://example.com/1.png)`) {
		t.Errorf("the picture's text is not escaped:\n%s", got)
	}
}

func TestPathOf(t *testing.T) {
	page := "http://localhost:43077/orders/1002"
	for _, tc := range []struct{ in, want string }{
		{"http://localhost:43077/app.js:12", "/app.js:12"},
		{"http://localhost:43077/orders?page=2#top", "/orders?page=2#top"},
		{"http://localhost:43077", "/"},
		{"https://cdn.example/logo.png", "https://cdn.example/logo.png"},
		{"app.js:3", "app.js:3"},
	} {
		if got := pathOf(tc.in, page); got != tc.want {
			t.Errorf("pathOf(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestShaRef(t *testing.T) {
	for _, tc := range []struct{ sha, head, want string }{
		{"3701136aa94", "3701136aa94", "3701136"},
		{"3701136aa94", "", "3701136"},
		{"3701136aa94", "3701136", "3701136"},
		{"3701136aa94", "9a8b7c6d5e4", "3701136 (the pull request has moved on since)"},
		{"", "9a8b7c6d5e4", "a commit pit did not record"},
	} {
		if got := shaRef(tc.sha, tc.head); got != tc.want {
			t.Errorf("shaRef(%q, %q) = %q, want %q", tc.sha, tc.head, got, tc.want)
		}
	}
}
