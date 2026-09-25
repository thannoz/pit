package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/thannoz/pit/internal/forge"
	"github.com/thannoz/pit/internal/forge/forgetest"
	"github.com/thannoz/pit/internal/notes"
	"github.com/thannoz/pit/internal/state"
)

// realCommenterFor is the one pit uses, kept before init replaces it.
var realCommenterFor = commenterFor

// No test posts anywhere, nor asks gh anything: one that means to post
// says where, with withCommenter.
func init() {
	commenterFor = func(context.Context, string) (forge.Commenter, error) {
		return nil, errors.New("tests post nowhere")
	}
}

// withCommenter makes the pull request's comments go to a fake, and
// notes whether pit went looking for where to post.
func withCommenter(t *testing.T) (*forgetest.Fake, *int) {
	t.Helper()
	fake := forgetest.New(forge.PR{Number: 482, URL: "https://github.com/acme/shop/pull/482"})
	looked := 0
	previous := commenterFor
	commenterFor = func(context.Context, string) (forge.Commenter, error) {
		looked++
		return fake, nil
	}
	t.Cleanup(func() { commenterFor = previous })
	return fake, &looked
}

// runReport runs pit report with what the user types, or with nobody
// there to type when stdin is nil.
func runReport(t *testing.T, stdin io.Reader, args ...string) (string, error) {
	t.Helper()
	if stdin == nil {
		stdin = devNull(t)
	}
	var out bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(stdin)
	cmd.SetArgs(append([]string{"report"}, args...))
	cmd.SetContext(t.Context())
	err := postProcess(cmd.Execute())
	return out.String(), err
}

// noted keeps notes on a sandbox's pull request directly.
func noted(t *testing.T, box state.Sandbox, list ...notes.Note) {
	t.Helper()
	m, err := manager()
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range list {
		if _, err := m.Notes(box.Repo, box.RepoRef, box.PR).Add(n, notes.Files{}); err != nil {
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

	out, err := runReport(t, nil, "482", "--dry-run")
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

	out, err = runReport(t, nil, "482", "--notes", "3,1", "--dry-run")
	if err != nil || !strings.Contains(out, "2 things I found") || strings.Contains(out, "Totals overlap") {
		t.Errorf("%v:\n%s", err, out)
	}
	if _, err := runReport(t, nil, "482", "--notes", "1,8"); err == nil || !strings.Contains(err.Error(), "#482 has no note 8") {
		t.Errorf("err = %v", err)
	}

	out, err = runReport(t, nil, "482", "--json")
	var r reportJSON
	if err != nil || json.Unmarshal([]byte(out), &r) != nil || r.PR != 482 || !strings.HasPrefix(r.Body, "### Review notes") || r.Posted {
		t.Errorf("%v:\n%s", err, out)
	}
}

func TestReportWithoutNotes(t *testing.T) {
	runningBox(t)
	_, err := runReport(t, nil, "482")
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
	out, err := runReport(t, nil, "482", "--dry-run")
	if err != nil || !strings.Contains(out, "One thing I found while trying out 3701136 locally.") {
		t.Errorf("%v:\n%s", err, out)
	}
}

// threeNotes is a running sandbox with three notes, the first with a
// screenshot.
func threeNotes(t *testing.T) (notes.Book, state.Sandbox) {
	t.Helper()
	m, box := runningBox(t)
	b := m.Notes(box.Repo, box.RepoRef, box.PR)
	for i, text := range []string{"The refund total ignores the voucher", "Totals overlap", "Checkout button does nothing"} {
		var files notes.Files
		if i == 0 {
			files = notes.Files{Screenshot: []byte("\x89PNG"), GIF: []byte("GIF89a")}
		}
		if _, err := b.Add(notes.Note{Text: text, URL: "http://localhost:41234/", SHA: "3701136aa94"}, files); err != nil {
			t.Fatal(err)
		}
	}
	return b, box
}

// TestReportNeverPostsWithoutConsent is the acceptance criterion for
// T-806: whatever way pit report is run, nothing reaches the pull
// request unless someone said yes to it.
func TestReportNeverPostsWithoutConsent(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stdin  io.Reader
		args   []string
		want   string
		looked bool
	}{
		{"nobody to ask", nil, nil, "There was nobody to ask; `pit report 482 --yes` posts it without asking.", true},
		{"no", strings.NewReader("n\n"), nil, "Post this comment on #482? [y/N]: Not posted.", true},
		{"enter", strings.NewReader("\n"), nil, "Not posted.", true},
		{"something else", strings.NewReader("sure\n"), nil, "Not posted.", true},
		{"dry run", strings.NewReader("y\n"), []string{"--dry-run"}, "Not posted: --dry-run.", false},
		{"json", strings.NewReader("y\n"), []string{"--json"}, `"posted": false`, false},
		{"json dry run", nil, []string{"--json", "--dry-run"}, `"posted": false`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			threeNotes(t)
			fake, looked := withCommenter(t)
			out, err := runReport(t, tc.stdin, append([]string{"482"}, tc.args...)...)
			if err != nil {
				t.Fatal(err)
			}
			if len(fake.Comments()) != 0 {
				t.Errorf("posted: %+v", fake.Comments())
			}
			if !strings.Contains(out, tc.want) {
				t.Errorf("output lacks %q:\n%s", tc.want, out)
			}
			if (*looked > 0) != tc.looked {
				t.Errorf("looked for where to post %d times", *looked)
			}
		})
	}
	t.Run("contradiction", func(t *testing.T) {
		threeNotes(t)
		fake, _ := withCommenter(t)
		if _, err := runReport(t, nil, "482", "--dry-run", "--yes"); err == nil || len(fake.Comments()) != 0 {
			t.Errorf("err = %v, posted %d", err, len(fake.Comments()))
		}
	})
}

func TestReportPreview(t *testing.T) {
	threeNotes(t)
	withCommenter(t)
	out, err := runReport(t, nil, "482", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"This comment would go on #482 in github.com/acme/shop:\n\n────",
		"────\n### Review notes\n",
		"#### 3. Checkout button does nothing\n\nPage: `/`\n\nThe browser reported no errors on the page.\n────",
		"Screenshots and GIFs are not posted; drag them into the comment if they help:\n  note 1  ",
		"1.png\n  note 1  ",
		"1.gif\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("preview lacks %q:\n%s", want, out)
		}
	}
}

func TestReportPostsWhenToldTo(t *testing.T) {
	b, _ := threeNotes(t)
	fake, _ := withCommenter(t)
	out, err := runReport(t, strings.NewReader("y\n"), "482", "--notes", "1,3")
	if err != nil {
		t.Fatal(err)
	}
	comments := fake.Comments()
	if len(comments) != 1 || comments[0].Number != 482 || !strings.Contains(comments[0].Body, "#### 3. Checkout button does nothing") ||
		strings.Contains(comments[0].Body, "Totals overlap") {
		t.Fatalf("posted %+v", comments)
	}
	if !strings.Contains(out, "Posted: https://github.com/acme/shop/pull/482#issuecomment-1\n") {
		t.Errorf("output:\n%s", out)
	}
	list, _ := b.List()
	if list[0].Posted == "" || list[1].Posted != "" || list[2].Posted == "" {
		t.Errorf("marked %+v", list)
	}

	// The next comment tells what has not been told.
	out, err = runReport(t, nil, "482", "--yes")
	if err != nil {
		t.Fatal(err)
	}
	if got := fake.Comments(); len(got) != 2 || !strings.Contains(got[1].Body, "Totals overlap") || strings.Contains(got[1].Body, "Checkout") {
		t.Errorf("second comment: %+v", got)
	}
	if strings.Contains(out, "[y/N]") {
		t.Errorf("--yes asked:\n%s", out)
	}
	if _, err := runReport(t, nil, "482", "--yes"); err == nil || !strings.Contains(err.Error(), "every note on #482 is in a comment already") {
		t.Errorf("err = %v", err)
	}
	if list, _ := b.List(); !strings.Contains(listed(t), "posted https://github.com/acme/shop/pull/482#issuecomment-2") || len(list) != 3 {
		t.Errorf("the listing does not say where it went")
	}
}

func listed(t *testing.T) string {
	t.Helper()
	out, _, err := run(t, "note", "482")
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestReportJSONPostsOnlyWithYes(t *testing.T) {
	threeNotes(t)
	fake, _ := withCommenter(t)
	out, err := runReport(t, nil, "482", "--json", "--yes")
	if err != nil {
		t.Fatal(err)
	}
	var r reportJSON
	if err := json.Unmarshal([]byte(out), &r); err != nil || !r.Posted || r.URL == "" || len(r.Notes) != 3 || len(fake.Comments()) != 1 {
		t.Errorf("%v: %+v\n%s", err, r, out)
	}
}

// A failure to post leaves the notes as they were, to be posted again.
func TestReportThatCannotBePosted(t *testing.T) {
	b, _ := threeNotes(t)
	fake, _ := withCommenter(t)
	fake.Err = errors.New("the conversation on #482 is locked")
	if _, err := runReport(t, nil, "482", "--yes"); err == nil {
		t.Fatal("no error")
	}
	for _, n := range mustList(t, b) {
		if n.Posted != "" {
			t.Errorf("marked posted: %+v", n)
		}
	}

	// Where pit cannot post, it says so after showing the comment.
	previous := commenterFor
	commenterFor = func(context.Context, string) (forge.Commenter, error) {
		return nil, errors.New("pit can post comments only on GitHub")
	}
	t.Cleanup(func() { commenterFor = previous })
	out, err := runReport(t, strings.NewReader("y\n"), "482")
	if err == nil || !strings.Contains(out, "### Review notes") || strings.Contains(out, "[y/N]") {
		t.Errorf("err = %v:\n%s", err, out)
	}
}

func mustList(t *testing.T, b notes.Book) []notes.Note {
	t.Helper()
	list, err := b.List()
	if err != nil {
		t.Fatal(err)
	}
	return list
}

// Off GitHub and GitLab there is nowhere to post, and pit says so
// without asking anything.
func TestCommentsOnlyOnGitHub(t *testing.T) {
	for repo, want := range map[string]string{
		"local//home/lisa/shop":   "this repository is in a directory on this machine",
		"bitbucket.org/acme/shop": "this repository is on bitbucket.org",
	} {
		_, err := realCommenterFor(t.Context(), repo)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v", repo, err)
		}
	}
}

// On GitLab, the merge request's notes; nothing is asked of gh.
func TestCommentsOnGitLab(t *testing.T) {
	t.Setenv("GITLAB_TOKEN", "glpat-secret")
	c, err := realCommenterFor(t.Context(), "gitlab.com/acme/backend/shop")
	g, ok := c.(forge.GitLab)
	if err != nil || !ok || g.Project != "acme/backend/shop" || g.Token != "glpat-secret" {
		t.Errorf("%#v, %v", c, err)
	}
}
