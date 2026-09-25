package cli

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/thannoz/pit/internal/notes"
	"github.com/thannoz/pit/internal/state"
)

// noted keeps notes on a sandbox's pull request directly.
func noted(t *testing.T, box state.Sandbox, list ...notes.Note) {
	t.Helper()
	m, err := manager()
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range list {
		if _, err := m.Notes(box.Repo, box.RepoRef, box.PR).Add(n, nil); err != nil {
			t.Fatal(err)
		}
	}
}

func TestReportWritesTheComment(t *testing.T) {
	m, box := runningBox(t)
	if err := m.Store.Update(func(f *state.File) error {
		box.SHA = "9a8b7c6d5e4"
		f.Put(box)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	noted(t, box,
		notes.Note{Text: "The refund total ignores the voucher", URL: "http://localhost:41234/orders/1001", SHA: "3701136aa94", Scenario: "refunded", Problems: broken.Problems},
		notes.Note{Text: "Totals overlap", URL: "http://localhost:41234/", SHA: "3701136aa94", Scenario: "refunded"},
		notes.Note{Text: "Checkout button does nothing", URL: "http://localhost:41234/cart", SHA: "3701136aa94", Scenario: "refunded", Uncaptured: "not asked to"},
	)

	out, _, err := run(t, "report", "482")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"### Review notes\n\n3 things I found while trying out 3701136 locally. The pull request has moved on since; some of this may be fixed already.\n",
		"```sh\npit 482 --scenario=refunded\n```\n",
		"#### 1. The refund total ignores the voucher\n\nPage: `/orders/1001`\n",
		"- **Failed request:** `GET /api/cart` answered 500 Internal Server Error\n",
		"#### 3. Checkout button does nothing\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the comment lacks %q:\n%s", want, out)
		}
	}

	out, _, err = run(t, "report", "482", "--notes", "3,1")
	if err != nil || !strings.Contains(out, "2 things I found") || strings.Contains(out, "Totals overlap") {
		t.Errorf("%v:\n%s", err, out)
	}
	if _, _, err := run(t, "report", "482", "--notes", "1,8"); err == nil || !strings.Contains(err.Error(), "#482 has no note 8") {
		t.Errorf("err = %v", err)
	}

	out, _, err = run(t, "report", "482", "--json")
	var r struct {
		PR   int    `json:"pr"`
		Body string `json:"body"`
	}
	if err != nil || json.Unmarshal([]byte(out), &r) != nil || r.PR != 482 || !strings.HasPrefix(r.Body, "### Review notes") {
		t.Errorf("%v:\n%s", err, out)
	}
}

func TestReportWithoutNotes(t *testing.T) {
	runningBox(t)
	_, _, err := run(t, "report", "482")
	if err == nil || !strings.Contains(err.Error(), "there are no notes on #482") {
		t.Errorf("err = %v", err)
	}
}

// The comment can be written after the sandbox is gone; the commit is
// then the one the notes were taken on.
func TestReportAfterTheSandboxIsGone(t *testing.T) {
	id := atRepo(t, "github.com", "acme", "shop")
	box := recorded(482, id.String(), id.Ref(), "refunds", time.Minute)
	withManager(t)
	noted(t, box, notes.Note{Text: "Checkout button does nothing", URL: "http://localhost:41234/cart", SHA: "3701136aa94"})
	out, _, err := run(t, "report", "482")
	if err != nil || !strings.Contains(out, "One thing I found while trying out 3701136 locally.") {
		t.Errorf("%v:\n%s", err, out)
	}
}
