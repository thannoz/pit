package workspace

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/thannoz/pit/internal/proc"
)

// TestFullCycle walks the whole of P1 in one go: find the repository,
// fetch a pull request, check it out, work with it, and leave no trace.
// It runs against the real git binary and never touches the network --
// the "remote" is a directory next to the clone.
func TestFullCycle(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	repo := newTestRepo(t)
	state := t.TempDir()
	const pr = 42

	head := repo.PublishPullRequest(pr, "the reviewed change\n")
	before := snapshot(t, repo)

	// 1. Find the repository from an arbitrary directory inside it.
	found, err := Discover(t.Context(), proc.Exec{}, repo.Repo.Root)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if found.Identity.Host != LocalHost {
		t.Errorf("Host = %q, want %q for a filesystem remote", found.Identity.Host, LocalHost)
	}

	// 2. Fetch the pull request.
	sha, err := Fetch(t.Context(), proc.Exec{}, found, pr)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if sha != head {
		t.Fatalf("Fetch returned %q, want %q", sha, head)
	}

	// 3. Check it out.
	wt, err := AddWorktree(t.Context(), proc.Exec{}, found, state, pr)
	if err != nil {
		t.Fatalf("AddWorktree: %v", err)
	}

	// 4. The reviewed change is there to look at.
	content, err := os.ReadFile(filepath.Join(wt.Path, "feature.txt"))
	if err != nil {
		t.Fatalf("reading the checked out change: %v", err)
	}
	if string(content) != "the reviewed change\n" {
		t.Errorf("feature.txt = %q, want the pull request's version", content)
	}

	// 5. And the reviewer's own repository never moved.
	assertUnchanged(t, repo, before)

	// 6. Clean up leaves nothing.
	if err := Remove(t.Context(), proc.Exec{}, found, state, pr); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	assertUnchanged(t, repo, before)

	if _, err := os.Stat(wt.Path); !os.IsNotExist(err) {
		t.Errorf("%s survived cleanup", wt.Path)
	}
	if entries, err := os.ReadDir(state); err != nil || len(entries) != 0 {
		t.Errorf("the state directory still holds %v", entries)
	}
	if got := len(worktreeEntries(t, repo)); got != 1 {
		t.Errorf("git still knows %d worktrees, want only the main one", got)
	}
}

// TestFullCycleSurvivesAForcePush repeats the cycle across a rewritten
// pull request, which is the ordinary case rather than an edge one.
func TestFullCycleSurvivesAForcePush(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	repo := newTestRepo(t)
	state := t.TempDir()
	const pr = 42

	for i, want := range []string{"first round\n", "second round\n", "third round\n"} {
		repo.PublishPullRequest(pr, want)

		if _, err := Fetch(t.Context(), proc.Exec{}, repo.Repo, pr); err != nil {
			t.Fatalf("round %d, Fetch: %v", i+1, err)
		}
		wt, err := AddWorktree(t.Context(), proc.Exec{}, repo.Repo, state, pr)
		if err != nil {
			t.Fatalf("round %d, AddWorktree: %v", i+1, err)
		}

		content, err := os.ReadFile(filepath.Join(wt.Path, "feature.txt"))
		if err != nil {
			t.Fatalf("round %d, reading the change: %v", i+1, err)
		}
		if string(content) != want {
			t.Errorf("round %d: feature.txt = %q, want %q", i+1, content, want)
		}
	}

	if err := Remove(t.Context(), proc.Exec{}, repo.Repo, state, pr); err != nil {
		t.Fatalf("Remove: %v", err)
	}
}

// TestTwoReviewsAtOnce is the case that makes the whole thing worth
// building: a reviewer has one pull request open and opens another.
func TestTwoReviewsAtOnce(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	repo := newTestRepo(t)
	state := t.TempDir()

	repo.PublishPullRequest(7, "change from seven\n")
	repo.PublishPullRequest(8, "change from eight\n")

	worktrees := map[int]Worktree{}
	for _, pr := range []int{7, 8} {
		if _, err := Fetch(t.Context(), proc.Exec{}, repo.Repo, pr); err != nil {
			t.Fatalf("Fetch #%d: %v", pr, err)
		}
		wt, err := AddWorktree(t.Context(), proc.Exec{}, repo.Repo, state, pr)
		if err != nil {
			t.Fatalf("AddWorktree #%d: %v", pr, err)
		}
		worktrees[pr] = wt
	}

	if worktrees[7].Path == worktrees[8].Path {
		t.Fatal("both pull requests were checked out into the same directory")
	}
	for pr, want := range map[int]string{7: "change from seven\n", 8: "change from eight\n"} {
		content, err := os.ReadFile(filepath.Join(worktrees[pr].Path, "feature.txt"))
		if err != nil {
			t.Fatalf("reading #%d: %v", pr, err)
		}
		if string(content) != want {
			t.Errorf("#%d holds %q, want %q", pr, content, want)
		}
	}

	for _, pr := range []int{7, 8} {
		if err := Remove(t.Context(), proc.Exec{}, repo.Repo, state, pr); err != nil {
			t.Fatalf("Remove #%d: %v", pr, err)
		}
	}
	if entries, err := os.ReadDir(state); err != nil || len(entries) != 0 {
		t.Errorf("the state directory still holds %v", entries)
	}
}

// repoState is everything about the reviewer's repository that pit must
// never change.
type repoState struct {
	branches []string
	head     string
	status   string
}

func snapshot(t *testing.T, repo *testRepo) repoState {
	t.Helper()
	return repoState{branches: repo.Branches(), head: repo.Head(), status: repo.Status()}
}

func assertUnchanged(t *testing.T, repo *testRepo, before repoState) {
	t.Helper()

	now := snapshot(t, repo)
	if !slices.Equal(now.branches, before.branches) {
		t.Errorf("branches changed from %v to %v", before.branches, now.branches)
	}
	if now.head != before.head {
		t.Errorf("HEAD moved from %q to %q", before.head, now.head)
	}
	if now.status != before.status {
		t.Errorf("the working tree changed:\nbefore %q\nafter  %q", before.status, now.status)
	}
}
