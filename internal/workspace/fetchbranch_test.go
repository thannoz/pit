package workspace

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/proc"
)

func TestSiblingURL(t *testing.T) {
	for origin, want := range map[string]string{
		"https://bitbucket.org/acme/shop.git":        "https://bitbucket.org/lisa/shop.git",
		"https://lisa@bitbucket.org/acme/shop.git":   "https://lisa@bitbucket.org/lisa/shop.git",
		"https://bitbucket.org/acme/shop":            "https://bitbucket.org/lisa/shop",
		"git@bitbucket.org:acme/shop.git":            "git@bitbucket.org:lisa/shop.git",
		"ssh://git@bitbucket.org/acme/shop.git":      "ssh://git@bitbucket.org/lisa/shop.git",
		"ssh://git@bitbucket.org:7999/acme/shop.git": "ssh://git@bitbucket.org:7999/lisa/shop.git",
	} {
		got, err := siblingURL(origin, "lisa/shop")
		if err != nil || got != want {
			t.Errorf("%s: %q, %v; want %q", origin, got, err, want)
		}
	}
	for _, origin := range []string{"/home/lisa/shop", "https://bitbucket.org"} {
		if _, err := siblingURL(origin, "lisa/shop"); err == nil {
			t.Errorf("%s: a fork's address from it", origin)
		}
	}
}

// A service that keeps no ref for pull requests: the branch is fetched,
// from the repository or from a fork, and the reviewer's own branches
// stay as they were.
func TestFetchBranch(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	repo := newTestRepo(t)
	// Origin is on a service; git is told where that is on disk, and
	// the fork beside it.
	service := t.TempDir()
	for _, path := range []string{"acme", "lisa"} {
		mkdir(t, filepath.Join(service, path))
	}
	git(t, service, "clone", "--quiet", "--bare", repo.Remote, filepath.Join(service, "acme", "shop.git"))
	git(t, service, "clone", "--quiet", "--bare", repo.Remote, filepath.Join(service, "lisa", "shop.git"))
	git(t, repo.Repo.Root, "remote", "set-url", "origin", "https://example.invalid/acme/shop.git")
	git(t, repo.Repo.Root, "config", "url."+service+"/.insteadOf", "https://example.invalid/")

	commit := func(bare, branch, content string) string {
		work := filepath.Join(t.TempDir(), "w")
		git(t, service, "clone", "--quiet", bare, work)
		configureIdentity(t, work)
		git(t, work, "checkout", "--quiet", "-b", branch)
		writeFile(t, filepath.Join(work, "feature.txt"), content)
		git(t, work, "add", "feature.txt")
		git(t, work, "commit", "--quiet", "-m", content)
		git(t, work, "push", "--quiet", "origin", branch)
		return strings.TrimSpace(repo.rev(work, "HEAD"))
	}
	own := commit(filepath.Join(service, "acme", "shop.git"), "fix-tax", "own branch")
	fork := commit(filepath.Join(service, "lisa", "shop.git"), "fix-tax", "from the fork")
	branches := repo.Branches()

	sha, err := FetchBranch(t.Context(), proc.Exec{}, repo.Repo, 7, "", "fix-tax")
	if err != nil || sha != own {
		t.Fatalf("own: %q, %v; want %q", sha, err, own)
	}
	sha, err = FetchBranch(t.Context(), proc.Exec{}, repo.Repo, 8, "lisa/shop", "fix-tax")
	if err != nil || sha != fork {
		t.Fatalf("fork: %q, %v; want %q", sha, err, fork)
	}
	if got, _ := ResolveRef(t.Context(), proc.Exec{}, repo.Repo.Root, LocalRef(8)); got != fork {
		t.Errorf("refs/pit/8 = %q", got)
	}
	if got := repo.Branches(); strings.Join(got, ",") != strings.Join(branches, ",") {
		t.Errorf("branches %v, were %v", got, branches)
	}

	_, err = FetchBranch(t.Context(), proc.Exec{}, repo.Repo, 9, "gone/shop", "fix-tax")
	if err == nil || !strings.Contains(err.Error(), "cannot fetch #9's branch fix-tax from the fork gone/shop") || !strings.Contains(errs.Hint(err), "deleted") {
		t.Errorf("err = %v", err)
	}
	if _, err := FetchBranch(t.Context(), proc.Exec{}, repo.Repo, 9, "", "nope"); err == nil || !strings.Contains(err.Error(), "from the repository") {
		t.Errorf("err = %v", err)
	}
	if _, err := FetchBranch(t.Context(), proc.Exec{}, repo.Repo, 0, "", "fix-tax"); err == nil {
		t.Error("fetched #0")
	}
	if _, err := FetchBranch(t.Context(), proc.Exec{}, repo.Repo, 9, "", ""); err == nil {
		t.Error("fetched no branch")
	}
}
