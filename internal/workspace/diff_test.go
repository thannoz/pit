package workspace

import (
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"

	"github.com/thannoz/pit/internal/diff"
	"github.com/thannoz/pit/internal/proc"
)

// awkwardPullRequest publishes a pull request that exercises every way
// a diff parser tends to go quietly wrong: a path with a space, one with
// an umlaut, one with a tab in it, a rename, a binary file, a deletion,
// a changed mode, and a file changed in two separate places.
func (r *testRepo) awkwardPullRequest(pr int) (base, head string) {
	r.t.Helper()
	up := r.Remote

	// What main looks like before the branch leaves it.
	put(r.t, filepath.Join(up, "src", "moved.go"), strings.Repeat("package moved\n// line\n", 20))
	put(r.t, filepath.Join(up, "src", "gone.go"), "package gone\n")
	put(r.t, filepath.Join(up, "scripts", "run.sh"), "#!/bin/sh\necho run\n")
	put(r.t, filepath.Join(up, "twice.txt"), numbered(40))
	git(r.t, up, "add", "-A")
	git(r.t, up, "commit", "--quiet", "-m", "base for the awkward pull request")
	base = r.rev(up, "HEAD")

	git(r.t, up, "checkout", "--quiet", "-B", "awkward")
	put(r.t, filepath.Join(up, "docs", "with space.md"), "a file with a space\n")
	put(r.t, filepath.Join(up, "docs", "Übersicht.md"), "a file with an umlaut\n")
	// Windows allows no tab in a file's name; git elsewhere does.
	if tabsInNames {
		put(r.t, filepath.Join(up, "docs", "tab\there.md"), "a file with a tab in its name\n")
	}
	put(r.t, filepath.Join(up, "logo.png"), string([]byte{0x89, 'P', 'N', 'G', 0, 0, 1, 2, 0, 3}))
	git(r.t, up, "mv", "src/moved.go", "src/renamed.go")
	git(r.t, up, "rm", "--quiet", "src/gone.go")
	// Two separate places, far enough apart to be two hunks.
	lines := strings.Split(numbered(40), "\n")
	lines[2] = "changed near the top"
	lines[35] = "changed near the bottom"
	put(r.t, filepath.Join(up, "twice.txt"), strings.Join(lines, "\n"))
	if err := os.Chmod(filepath.Join(up, "scripts", "run.sh"), 0o755); err != nil {
		r.t.Fatalf("chmod: %v", err)
	}
	git(r.t, up, "add", "-A")
	// And through git: Windows keeps no executable bit for git to find.
	git(r.t, up, "update-index", "--chmod=+x", "scripts/run.sh")
	git(r.t, up, "commit", "--quiet", "-m", "the awkward change")
	head = r.rev(up, "HEAD")

	git(r.t, up, "update-ref", RemotePullRef(LocalHost, pr), head)
	git(r.t, up, "checkout", "--quiet", "main")
	git(r.t, up, "reset", "--quiet", "--hard", base)
	return base, head
}

// tabsInNames says the system allows a tab in a file's name.
var tabsInNames = goruntime.GOOS != "windows"

// put writes a file, making its directory first; the fixture spreads
// its files across several.
func put(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	writeFile(t, path, content)
}

func numbered(n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		b.WriteString("line ")
		b.WriteString(strings.Repeat("x", i%7))
		b.WriteString("\n")
	}
	return b.String()
}

func changesOf(t *testing.T, r *testRepo, pr int) diff.Diff {
	t.Helper()

	head, err := Fetch(t.Context(), proc.Exec{}, r.Repo, pr)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	base, err := FetchBase(t.Context(), proc.Exec{}, r.Repo, pr, "main")
	if err != nil {
		t.Fatalf("FetchBase: %v", err)
	}
	d, err := Changes(t.Context(), proc.Exec{}, r.Repo, base, head)
	if err != nil {
		t.Fatalf("Changes: %v", err)
	}
	return d
}

func fileAt(t *testing.T, d diff.Diff, path string) diff.File {
	t.Helper()
	for _, f := range d.Files {
		if f.Path == path {
			return f
		}
	}
	var got []string
	for _, f := range d.Files {
		got = append(got, f.Path)
	}
	t.Fatalf("%q is not in the diff; it has %q", path, got)
	return diff.File{}
}

// TestChangesReadsTheWholeDiff is the acceptance criterion for T-601
// at the level of a real repository: every file git itself lists, with
// the kind of change and where it happened.
func TestChangesReadsTheWholeDiff(t *testing.T) {
	r := newTestRepo(t)
	r.awkwardPullRequest(7)

	d := changesOf(t, r, 7)

	// What git lists when asked directly, which is the definition of
	// "the whole diff".
	raw := r.run(r.Repo.Root, "diff", "--name-only", "-z", "--find-renames", d.Base, d.Head)
	listed := strings.Split(strings.TrimSuffix(raw, "\x00"), "\x00")
	if len(d.Files) != len(listed) {
		t.Errorf("read %d files, git lists %d: %q", len(d.Files), len(listed), listed)
	}

	tests := []struct {
		path   string
		change diff.Change
	}{
		{"docs/with space.md", diff.Added},
		{"docs/Übersicht.md", diff.Added},
		{"logo.png", diff.Added},
		{"src/renamed.go", diff.Renamed},
		{"src/gone.go", diff.Deleted},
		{"scripts/run.sh", diff.Modified},
		{"twice.txt", diff.Modified},
	}
	if tabsInNames {
		tests = append(tests, struct {
			path   string
			change diff.Change
		}{"docs/tab\there.md", diff.Added})
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if f := fileAt(t, d, tt.path); f.Change != tt.change {
				t.Errorf("change = %q, want %q", f.Change, tt.change)
			}
		})
	}
}

func TestChangesKnowsWhereARenameCameFrom(t *testing.T) {
	r := newTestRepo(t)
	r.awkwardPullRequest(7)

	f := fileAt(t, changesOf(t, r, 7), "src/renamed.go")
	if f.OldPath != "src/moved.go" {
		t.Errorf("OldPath = %q, want src/moved.go", f.OldPath)
	}
	// An unchanged rename has no lines to point at.
	if len(f.Hunks) != 0 {
		t.Errorf("a pure rename has hunks: %+v", f.Hunks)
	}
}

func TestChangesFindsEachPlaceInsideAFile(t *testing.T) {
	// Line ranges are what later tells a reviewer where to look, so
	// two separate changes have to stay two.
	r := newTestRepo(t)
	r.awkwardPullRequest(7)

	f := fileAt(t, changesOf(t, r, 7), "twice.txt")
	if len(f.Hunks) != 2 {
		t.Fatalf("hunks = %+v, want two", f.Hunks)
	}
	if f.Hunks[0].New != (diff.Range{Start: 3, Count: 1}) {
		t.Errorf("first hunk = %+v, want line 3", f.Hunks[0].New)
	}
	if f.Hunks[1].New != (diff.Range{Start: 36, Count: 1}) {
		t.Errorf("second hunk = %+v, want line 36", f.Hunks[1].New)
	}
	if f.Added != 2 || f.Deleted != 2 {
		t.Errorf("counts = +%d -%d, want +2 -2", f.Added, f.Deleted)
	}
}

func TestChangesMarksBinaryFiles(t *testing.T) {
	r := newTestRepo(t)
	r.awkwardPullRequest(7)

	f := fileAt(t, changesOf(t, r, 7), "logo.png")
	if !f.Binary {
		t.Error("a PNG is not marked as binary")
	}
	if len(f.Hunks) != 0 || f.Added != 0 {
		t.Errorf("a binary file has lines: %+v", f)
	}
}

func TestChangesSeesAModeChangeWithoutHunks(t *testing.T) {
	// Making a script executable is a change a reviewer should see,
	// and it has no lines.
	r := newTestRepo(t)
	r.awkwardPullRequest(7)

	f := fileAt(t, changesOf(t, r, 7), "scripts/run.sh")
	if len(f.Hunks) != 0 {
		t.Errorf("a mode change has hunks: %+v", f.Hunks)
	}
}

func TestChangesKnowsADeletedFileByWhereItWas(t *testing.T) {
	r := newTestRepo(t)
	r.awkwardPullRequest(7)

	f := fileAt(t, changesOf(t, r, 7), "src/gone.go")
	if len(f.Hunks) != 1 || f.Hunks[0].New.Count != 0 || f.Hunks[0].Old.Count != 1 {
		t.Errorf("hunks = %+v, want one that removes the only line", f.Hunks)
	}
}

func TestChangesMeasuresFromWhereTheBranchLeft(t *testing.T) {
	// The target moves on after a branch leaves it. Diffing against
	// its tip would show those later changes as if the pull request
	// had made them -- in reverse.
	r := newTestRepo(t)
	r.awkwardPullRequest(7)

	// main gains a file after the branch was cut.
	writeFile(t, filepath.Join(r.Remote, "later.txt"), "added to main afterwards\n")
	git(t, r.Remote, "add", "later.txt")
	git(t, r.Remote, "commit", "--quiet", "-m", "later work on main")

	d := changesOf(t, r, 7)
	for _, f := range d.Files {
		if f.Path == "later.txt" {
			t.Errorf("a change made to main afterwards is reported as the pull request's: %+v", f)
		}
	}
}

func TestFetchBaseLeavesTheReviewersRefsAlone(t *testing.T) {
	// Fetching into origin/main would move the reviewer's own
	// remote-tracking branch under them.
	r := newTestRepo(t)
	r.awkwardPullRequest(7)
	before := r.run(r.Repo.Root, "for-each-ref", "refs/remotes", "refs/heads")

	if _, err := FetchBase(t.Context(), proc.Exec{}, r.Repo, 7, "main"); err != nil {
		t.Fatalf("FetchBase: %v", err)
	}

	if after := r.run(r.Repo.Root, "for-each-ref", "refs/remotes", "refs/heads"); after != before {
		t.Errorf("the reviewer's refs changed:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func TestDeleteRefRemovesTheBaseToo(t *testing.T) {
	r := newTestRepo(t)
	r.awkwardPullRequest(7)
	changesOf(t, r, 7)

	if err := DeleteRef(t.Context(), proc.Exec{}, r.Repo, 7); err != nil {
		t.Fatalf("DeleteRef: %v", err)
	}
	if left := r.run(r.Repo.Root, "for-each-ref", LocalRefPrefix); left != "" {
		t.Errorf("refs left behind:\n%s", left)
	}
}

func TestFetchBaseWithoutABranchUsesTheDefault(t *testing.T) {
	// A repository with no hosting service to ask about the target
	// gets the remote's default branch, which is the honest guess.
	r := newTestRepo(t)
	base, _ := r.awkwardPullRequest(7)

	got, err := FetchBase(t.Context(), proc.Exec{}, r.Repo, 7, "")
	if err != nil {
		t.Fatalf("FetchBase: %v", err)
	}
	if got != base {
		t.Errorf("base = %s, want main's tip %s", got, base)
	}
}
