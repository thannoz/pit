package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thannoz/pit/internal/proc"
)

// worktreeEntries lists the paths git currently knows worktrees at.
func worktreeEntries(t *testing.T, repo *testRepo) []string {
	t.Helper()

	out := repo.run(repo.Repo.Root, "worktree", "list", "--porcelain")
	var paths []string
	for _, line := range strings.Split(out, "\n") {
		if p, ok := strings.CutPrefix(strings.TrimSpace(line), "worktree "); ok {
			paths = append(paths, p)
		}
	}
	return paths
}

// setupWorktree fetches a pull request and checks it out, returning the
// state directory and the worktree.
func setupWorktree(t *testing.T, repo *testRepo, pr int) (string, Worktree) {
	t.Helper()

	repo.PublishPullRequest(pr, "feature\n")
	mustFetch(t, repo, pr)
	state := t.TempDir()

	wt, err := AddWorktree(t.Context(), proc.Exec{}, repo.Repo, state, pr)
	if err != nil {
		t.Fatalf("AddWorktree: %v", err)
	}
	return state, wt
}

// TestRemoveLeavesNothingBehind is the acceptance criterion for T-104.
func TestRemoveLeavesNothingBehind(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	repo := newTestRepo(t)
	state, wt := setupWorktree(t, repo, 7)

	if got := len(worktreeEntries(t, repo)); got != 2 {
		t.Fatalf("git knows %d worktrees before removal, want 2", got)
	}

	if err := Remove(t.Context(), proc.Exec{}, repo.Repo, state, 7); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	if got := worktreeEntries(t, repo); len(got) != 1 {
		t.Errorf("git still knows %v, want only the main worktree", got)
	}
	if _, err := os.Stat(wt.Path); !os.IsNotExist(err) {
		t.Errorf("%s still exists", wt.Path)
	}
	if _, err := ResolveRef(t.Context(), proc.Exec{}, repo.Repo.Root, LocalRef(7)); err == nil {
		t.Errorf("%s still resolves", LocalRef(7))
	}
	if _, err := os.Stat(repo.Repo.Identity.RepoDir(state)); !os.IsNotExist(err) {
		t.Errorf("the repository directory was left behind, empty")
	}
}

func TestRemoveWithUncommittedChanges(t *testing.T) {
	// A reviewer edits a file to try something out. That must not turn
	// cleanup into an error they have to resolve by hand.
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	repo := newTestRepo(t)
	state, wt := setupWorktree(t, repo, 7)

	writeFile(t, filepath.Join(wt.Path, "feature.txt"), "edited while reviewing\n")
	writeFile(t, filepath.Join(wt.Path, "scratch.txt"), "a new file\n")

	if err := Remove(t.Context(), proc.Exec{}, repo.Repo, state, 7); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(wt.Path); !os.IsNotExist(err) {
		t.Errorf("%s still exists", wt.Path)
	}
}

func TestRemoveAfterTheDirectoryWasDeletedByHand(t *testing.T) {
	// Someone clears their disk and deletes the directory. git still
	// has an administrative record, and without pruning that record the
	// path cannot be used again.
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	repo := newTestRepo(t)
	state, wt := setupWorktree(t, repo, 7)

	if err := os.RemoveAll(wt.Path); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}

	if err := Remove(t.Context(), proc.Exec{}, repo.Repo, state, 7); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if got := worktreeEntries(t, repo); len(got) != 1 {
		t.Errorf("git still knows %v, want only the main worktree", got)
	}
}

func TestRemoveIsRepeatable(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	repo := newTestRepo(t)
	state, _ := setupWorktree(t, repo, 7)

	for i := range 3 {
		if err := Remove(t.Context(), proc.Exec{}, repo.Repo, state, 7); err != nil {
			t.Fatalf("Remove call %d: %v", i+1, err)
		}
	}
}

func TestRemoveOnSomethingThatWasNeverSetUp(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	repo := newTestRepo(t)

	if err := Remove(t.Context(), proc.Exec{}, repo.Repo, t.TempDir(), 404); err != nil {
		t.Errorf("Remove for a pull request that was never set up: %v", err)
	}
}

func TestRemoveKeepsOtherPullRequests(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	repo := newTestRepo(t)
	state, keep := setupWorktree(t, repo, 7)

	repo.PublishPullRequest(8, "another feature\n")
	mustFetch(t, repo, 8)
	drop, err := AddWorktree(t.Context(), proc.Exec{}, repo.Repo, state, 8)
	if err != nil {
		t.Fatalf("AddWorktree: %v", err)
	}

	if err := Remove(t.Context(), proc.Exec{}, repo.Repo, state, 8); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	if _, err := os.Stat(drop.Path); !os.IsNotExist(err) {
		t.Errorf("%s still exists", drop.Path)
	}
	// The other review is still open; removing one must not touch it.
	if _, err := os.Stat(keep.Path); err != nil {
		t.Errorf("the other pull request's worktree is gone: %v", err)
	}
	if _, err := ResolveRef(t.Context(), proc.Exec{}, repo.Repo.Root, LocalRef(7)); err != nil {
		t.Errorf("the other pull request's ref is gone: %v", err)
	}
	if _, err := os.Stat(repo.Repo.Identity.RepoDir(state)); err != nil {
		t.Errorf("the repository directory was removed although it still holds a review: %v", err)
	}
}
