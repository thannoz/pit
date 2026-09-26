package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/proc"
)

func TestStateDirPrefersExplicitSetting(t *testing.T) {
	t.Setenv(stateDirEnv, "/tmp/somewhere")

	got, err := StateDir()
	if err != nil {
		t.Fatalf("StateDir: %v", err)
	}
	if got != "/tmp/somewhere" {
		t.Errorf("StateDir() = %q, want the explicit setting", got)
	}
}

func TestStateDirFollowsXDG(t *testing.T) {
	t.Setenv(stateDirEnv, "")
	t.Setenv("XDG_STATE_HOME", "/xdg/state")

	got, err := StateDir()
	if err != nil {
		t.Fatalf("StateDir: %v", err)
	}
	if want := filepath.Join("/xdg/state", "pit"); got != want {
		t.Errorf("StateDir() = %q, want %q", got, want)
	}
}

func TestStateDirOnWindows(t *testing.T) {
	env := map[string]string{"LOCALAPPDATA": `C:\Users\someone\AppData\Local`}
	home := func() (string, error) { return `C:\Users\someone`, nil }
	got, err := stateDir("windows", func(k string) string { return env[k] }, home)
	if want := filepath.Join(env["LOCALAPPDATA"], "pit"); err != nil || got != want {
		t.Errorf("windows: %q, %v; want %q", got, err, want)
	}
	// Elsewhere it is not asked; and XDG and the setting still win.
	if got, _ := stateDir("linux", func(k string) string { return env[k] }, home); got != filepath.Join(`C:\Users\someone`, ".local", "state", "pit") {
		t.Errorf("linux: %q", got)
	}
	env["XDG_STATE_HOME"] = "/xdg"
	if got, _ := stateDir("windows", func(k string) string { return env[k] }, home); got != filepath.Join("/xdg", "pit") {
		t.Errorf("xdg: %q", got)
	}
	env[stateDirEnv] = "/set"
	if got, _ := stateDir("windows", func(k string) string { return env[k] }, home); got != "/set" {
		t.Errorf("setting: %q", got)
	}
}

// TestWorktreePathIsOutsideTheRepository guards the reason the state
// directory exists at all: a worktree inside the repository would be
// picked up by watchers, linters and editor indexing.
func TestWorktreePathIsOutsideTheRepository(t *testing.T) {
	id := Identity{Host: "github.com", Owner: "acme", Name: "shop"}
	const repoRoot = "/Users/someone/code/shop"

	path := id.WorktreeDir("/state/pit", 482)

	if strings.HasPrefix(path, repoRoot) {
		t.Errorf("WorktreeDir() = %q, which is inside the repository", path)
	}
	if !strings.Contains(path, id.Hash()) {
		t.Errorf("WorktreeDir() = %q, want the hash in it so two clones cannot collide", path)
	}
	if !strings.HasSuffix(path, "pr-482") {
		t.Errorf("WorktreeDir() = %q, want it to name the pull request", path)
	}
}

func TestWorktreeDirsDifferPerRepository(t *testing.T) {
	a := Identity{Host: "github.com", Owner: "acme", Name: "shop"}
	b := Identity{Host: "github.com", Owner: "other", Name: "shop"}

	if a.WorktreeDir("/state", 1) == b.WorktreeDir("/state", 1) {
		t.Error("two repositories share a worktree directory")
	}
}

func TestAddWorktreeRefusesAnUnfetchedPullRequest(t *testing.T) {
	r := &stubRunner{fail: map[string]bool{"rev-parse --verify refs/pit/9^{commit}": true}}

	_, err := AddWorktree(t.Context(), r, Repo{Root: "/repo"}, t.TempDir(), 9)
	if err == nil {
		t.Fatal("want an error for a pull request that was never fetched")
	}
	if !strings.Contains(err.Error(), "#9") {
		t.Errorf("error = %q, want it to name the pull request", err)
	}
	if errs.Hint(err) == "" {
		t.Error("the error carries no hint")
	}
}

// TestAddWorktreeLeavesTheRepositoryUntouched is the acceptance
// criterion for T-103.
func TestAddWorktreeLeavesTheRepositoryUntouched(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	repo := newTestRepo(t)
	want := repo.PublishPullRequest(7, "feature\n")
	state := t.TempDir()

	mustFetch(t, repo, 7)
	branchesBefore, headBefore, statusBefore := repo.Branches(), repo.Head(), repo.Status()

	wt, err := AddWorktree(t.Context(), proc.Exec{}, repo.Repo, state, 7)
	if err != nil {
		t.Fatalf("AddWorktree: %v", err)
	}

	if wt.SHA != want {
		t.Errorf("SHA = %q, want the pull request head %q", wt.SHA, want)
	}
	if got := repo.Branches(); !slices.Equal(got, branchesBefore) {
		t.Errorf("branches changed from %v to %v", branchesBefore, got)
	}
	if got := repo.Head(); got != headBefore {
		t.Errorf("HEAD moved from %q to %q", headBefore, got)
	}
	if got := repo.Status(); got != statusBefore {
		t.Errorf("the working tree changed:\nbefore %q\nafter  %q", statusBefore, got)
	}

	// And the pull request's content really is there.
	content, err := os.ReadFile(filepath.Join(wt.Path, "feature.txt"))
	if err != nil {
		t.Fatalf("reading the checked out file: %v", err)
	}
	if string(content) != "feature\n" {
		t.Errorf("feature.txt = %q, want the pull request's version", content)
	}
}

func TestAddWorktreeCheckoutIsDetached(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	repo := newTestRepo(t)
	repo.PublishPullRequest(7, "feature\n")
	mustFetch(t, repo, 7)

	wt, err := AddWorktree(t.Context(), proc.Exec{}, repo.Repo, t.TempDir(), 7)
	if err != nil {
		t.Fatalf("AddWorktree: %v", err)
	}

	// A branch here would show up in the reviewer's `git branch`.
	out, err := proc.Exec{}.Output(t.Context(), proc.Command{
		Name: "git", Args: []string{"symbolic-ref", "-q", "HEAD"}, Dir: wt.Path,
	})
	if err == nil {
		t.Errorf("HEAD in the worktree is on branch %q, want a detached checkout", strings.TrimSpace(string(out)))
	}
}

func TestAddWorktreeTwiceReusesTheSameCheckout(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	repo := newTestRepo(t)
	repo.PublishPullRequest(7, "feature\n")
	mustFetch(t, repo, 7)
	state := t.TempDir()

	first, err := AddWorktree(t.Context(), proc.Exec{}, repo.Repo, state, 7)
	if err != nil {
		t.Fatalf("first AddWorktree: %v", err)
	}
	// Running pit again on the same pull request is the normal case.
	second, err := AddWorktree(t.Context(), proc.Exec{}, repo.Repo, state, 7)
	if err != nil {
		t.Fatalf("second AddWorktree: %v", err)
	}

	if first != second {
		t.Errorf("second call returned %+v, want the same worktree as %+v", second, first)
	}
}

func TestAddWorktreeReplacesAStaleCheckout(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	repo := newTestRepo(t)
	repo.PublishPullRequest(7, "first version\n")
	mustFetch(t, repo, 7)
	state := t.TempDir()

	if _, err := AddWorktree(t.Context(), proc.Exec{}, repo.Repo, state, 7); err != nil {
		t.Fatalf("first AddWorktree: %v", err)
	}

	// The author pushes again; the checkout on disk is now out of date.
	updated := repo.PublishPullRequest(7, "second version\n")
	mustFetch(t, repo, 7)

	wt, err := AddWorktree(t.Context(), proc.Exec{}, repo.Repo, state, 7)
	if err != nil {
		t.Fatalf("second AddWorktree: %v", err)
	}
	if wt.SHA != updated {
		t.Errorf("SHA = %q, want the new head %q", wt.SHA, updated)
	}

	content, err := os.ReadFile(filepath.Join(wt.Path, "feature.txt"))
	if err != nil {
		t.Fatalf("reading the checked out file: %v", err)
	}
	if string(content) != "second version\n" {
		t.Errorf("feature.txt = %q, want the updated version", content)
	}
}

func mustFetch(t *testing.T, repo *testRepo, pr int) string {
	t.Helper()
	sha, err := Fetch(t.Context(), proc.Exec{}, repo.Repo, pr)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	return sha
}

// commitToPullRequest adds a commit to pull request pr upstream, on top
// of what it had: files written, paths removed. It returns the SHA.
func commitToPullRequest(t *testing.T, repo *testRepo, pr int, write map[string]string, remove ...string) string {
	t.Helper()
	branch := fmt.Sprintf("pit-test-pr-%d", pr)
	if _, err := (proc.Exec{}).Output(t.Context(), proc.Command{
		Name: "git", Args: []string{"rev-parse", "--verify", "--quiet", branch}, Dir: repo.Remote,
	}); err == nil {
		git(t, repo.Remote, "checkout", "--quiet", branch)
	} else {
		git(t, repo.Remote, "checkout", "--quiet", "-b", branch)
	}
	for name, content := range write {
		path := filepath.Join(repo.Remote, name)
		mkdir(t, filepath.Dir(path))
		writeFile(t, path, content)
		git(t, repo.Remote, "add", name)
	}
	for _, name := range remove {
		git(t, repo.Remote, "rm", "--quiet", name)
	}
	git(t, repo.Remote, "commit", "--quiet", "-m", "pull request change")
	sha := repo.rev(repo.Remote, "HEAD")
	git(t, repo.Remote, "update-ref", RemotePullRef(LocalHost, pr), sha)
	git(t, repo.Remote, "checkout", "--quiet", "main")
	return sha
}

// A new commit updates the checkout where it is. Containers bind-mount
// its directories -- ./migrations into the database, ./src into the
// application -- and a container that is not recreated keeps the
// directory it was given. Replaced, that directory is a deleted one, and
// the container sees it empty: the demo's database found no migrations
// and no fixtures after an update.
func TestAddWorktreeUpdatesACheckoutInPlace(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	repo := newTestRepo(t)
	commitToPullRequest(t, repo, 7, map[string]string{
		".gitignore":           "node_modules/\n",
		"migrations/001_a.sql": "CREATE TABLE a();\n",
		"obsolete.txt":         "going\n",
	})
	mustFetch(t, repo, 7)
	state := t.TempDir()

	first, err := AddWorktree(t.Context(), proc.Exec{}, repo.Repo, state, 7)
	if err != nil {
		t.Fatalf("first AddWorktree: %v", err)
	}
	dirBefore, err := os.Stat(filepath.Join(first.Path, "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	rootBefore, err := os.Stat(first.Path)
	if err != nil {
		t.Fatal(err)
	}
	// What a review leaves behind: an edit to a tracked file, a stray
	// file, and an installed dependency the project ignores.
	writeFile(t, filepath.Join(first.Path, "README.md"), "edited during the review\n")
	writeFile(t, filepath.Join(first.Path, "stray.txt"), "left over\n")
	mkdir(t, filepath.Join(first.Path, "node_modules"))
	writeFile(t, filepath.Join(first.Path, "node_modules", "dep.js"), "cached\n")

	updated := commitToPullRequest(t, repo, 7, map[string]string{"migrations/002_b.sql": "CREATE TABLE b();\n"}, "obsolete.txt")
	mustFetch(t, repo, 7)

	second, err := AddWorktree(t.Context(), proc.Exec{}, repo.Repo, state, 7)
	if err != nil {
		t.Fatalf("second AddWorktree: %v", err)
	}
	if second.SHA != updated || second.Path != first.Path {
		t.Fatalf("second = %+v, want %s at %s", second, updated, first.Path)
	}

	for name, before := range map[string]os.FileInfo{"migrations": dirBefore, ".": rootBefore} {
		after, err := os.Stat(filepath.Join(second.Path, name))
		if err != nil {
			t.Fatal(err)
		}
		if !os.SameFile(before, after) {
			t.Errorf("%s is a new directory; a container that mounted the old one sees nothing", name)
		}
	}
	// The checkout is the new commit's, and nothing else.
	for name, want := range map[string]string{
		"migrations/002_b.sql": "CREATE TABLE b();\n",
		"README.md":            "upstream\n",
		"node_modules/dep.js":  "cached\n", // ignored: the project's own cache survives
	} {
		if got, err := os.ReadFile(filepath.Join(second.Path, name)); err != nil || string(got) != want {
			t.Errorf("%s = %q, %v; want %q", name, got, err, want)
		}
	}
	for _, gone := range []string{"obsolete.txt", "stray.txt"} {
		if _, err := os.Stat(filepath.Join(second.Path, gone)); err == nil {
			t.Errorf("%s is still there", gone)
		}
	}
	// And git still knows the checkout as the worktree it is.
	if sha, ok := worktreeAt(t.Context(), proc.Exec{}, repo.Repo, second.Path); !ok || sha != updated {
		t.Errorf("git has the worktree at %q, %v", sha, ok)
	}
}

// A checkout that cannot be moved -- here its directory was deleted by
// hand -- is replaced, as before.
func TestAddWorktreeReplacesACheckoutItCannotMove(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	repo := newTestRepo(t)
	commitToPullRequest(t, repo, 7, map[string]string{"a.txt": "one\n"})
	mustFetch(t, repo, 7)
	state := t.TempDir()

	first, err := AddWorktree(t.Context(), proc.Exec{}, repo.Repo, state, 7)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(first.Path); err != nil {
		t.Fatal(err)
	}
	updated := commitToPullRequest(t, repo, 7, map[string]string{"a.txt": "two\n"})
	mustFetch(t, repo, 7)

	second, err := AddWorktree(t.Context(), proc.Exec{}, repo.Repo, state, 7)
	if err != nil {
		t.Fatalf("AddWorktree: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(second.Path, "a.txt")); err != nil || string(got) != "two\n" || second.SHA != updated {
		t.Errorf("a.txt = %q, %v at %s; want the new commit", got, err, second.SHA)
	}
}

// On macOS /var is a link to /private/var, and git lists worktrees by
// the resolved path. A worktree whose directory is gone still has to be
// recognised by the path pit knows it under.
func TestSameDirThroughALinkAndAMissingDirectory(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "private", "var")
	mkdir(t, real)
	link := filepath.Join(base, "var")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("no symlinks here: %v", err)
	}
	gone := filepath.Join("state", "pr-7") // never created
	if !sameDir(filepath.Join(link, gone), filepath.Join(real, gone)) {
		t.Error("a missing directory under a link is not recognised by its resolved path")
	}
	if sameDir(filepath.Join(link, "state", "pr-7"), filepath.Join(real, "state", "pr-8")) {
		t.Error("two different directories count as the same")
	}
}
