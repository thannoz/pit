package review_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/proc"
	"github.com/thannoz/pit/internal/review"
	"github.com/thannoz/pit/internal/state"
)

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func commit(t *testing.T, dir string, files map[string]string) string {
	t.Helper()
	for name, content := range files {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-q", "-m", "change")
	return gitIn(t, dir, "rev-parse", "HEAD")
}

func items(paths ...string) []review.Item {
	var out []review.Item
	for i, p := range paths {
		out = append(out, review.Item{Number: i + 1, Path: p})
	}
	return out
}

// The criterion of T-610: the second pit what shows the progress. And a
// check survives a new commit only where nothing that leads to the
// address has changed.
func TestProgressSurvivesWhatDidNotChange(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q")
	first := commit(t, dir, map[string]string{"app/orders/page.tsx": "a", "lib/money.ts": "1", "app/[slug]/page.tsx": "x"})
	second := commit(t, dir, map[string]string{"lib/money.ts": "2"})

	list := review.Checklist{Head: second, Items: []review.Item{
		{Number: 1, Path: "/orders", Files: []string{"app/orders/page.tsx"}},
		{Number: 2, Path: "/orders/{id}", Files: []string{"lib/money.ts"}},
		{Number: 3, Methods: []string{"POST"}, Path: "/api/orders", Files: []string{"lib/money.ts"}},
		{Number: 4, Path: "/{slug}", Files: []string{"app/[slug]/page.tsx"}},
		{Number: 5, Path: "/gone", Files: []string{"app/orders/page.tsx"}},
	}}
	checks := []state.Check{
		{Address: "/orders", SHA: first},                 // nothing leading here changed
		{Address: "/orders/{id}", SHA: first},            // money.ts changed since
		{Address: "POST /api/orders", SHA: second},       // looked at in this very commit
		{Address: "/{slug}", SHA: first},                 // brackets are a name, not a pattern
		{Address: "/gone", SHA: strings.Repeat("0", 40)}, // a commit rebased away
		{Address: "/not/on/the/list", SHA: second},
	}
	review.Progress(t.Context(), proc.Exec{}, dir, checks, &list)

	want := []review.Mark{review.Looked, review.Again, review.Looked, review.Looked, review.Again}
	for i, it := range list.Items {
		if it.Mark != want[i] {
			t.Errorf("%s: mark %d, want %d (changed since %v)", it.Address(), it.Mark, want[i], it.ChangedSince)
		}
	}
	if got := list.Items[1].ChangedSince; !slices.Equal(got, []string{"lib/money.ts"}) {
		t.Errorf("changed since = %v", got)
	}
	if list.Looked() != 3 {
		t.Errorf("Looked() = %d, want 3", list.Looked())
	}
}

func TestRecordKeepsMarksWithTheSandbox(t *testing.T) {
	store, err := state.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	box := state.Sandbox{PR: 7, RepoRef: "shop-1234"}
	if err := store.Update(func(f *state.File) error { f.Put(box); return nil }); err != nil {
		t.Fatal(err)
	}
	list := review.Checklist{Head: "abc", Items: items("/a", "/b", "/c")}

	box, err = review.Record(store, box, list, []int{1, 3}, true)
	if err != nil {
		t.Fatal(err)
	}
	box, err = review.Record(store, box, list, []int{3}, false)
	if err != nil {
		t.Fatal(err)
	}
	// Marking again replaces rather than duplicates.
	if box, err = review.Record(store, box, list, []int{1}, true); err != nil {
		t.Fatal(err)
	}

	f, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	stored, _ := f.Find("shop-1234", 7)
	var got []string
	for _, c := range stored.Checked {
		got = append(got, c.Address+"@"+c.SHA)
	}
	if want := []string{"/a@abc"}; !slices.Equal(got, want) {
		t.Errorf("checked = %v, want %v", got, want)
	}

	_, err = review.Record(store, box, list, []int{4}, true)
	if err == nil || !strings.Contains(err.Error(), "no item 4") || !strings.Contains(errs.Hint(err), "has 3") {
		t.Errorf("err = %v, hint %q", err, errs.Hint(err))
	}
}
