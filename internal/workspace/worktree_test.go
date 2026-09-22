package workspace

import (
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
