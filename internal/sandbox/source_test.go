package sandbox_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thannoz/pit/internal/forge"
)

// A service that keeps no ref for its pull requests names a branch:
// that is what is reviewed.
func TestUpFetchesTheBranchAServiceNames(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	m, req, _ := upFixture(t)
	upstream := remoteOf(t, req.Repo.Root)
	git(t, upstream, "checkout", "--quiet", "-b", "fix-tax")
	writeFile(t, filepath.Join(upstream, "branch.txt"), "from the branch\n")
	git(t, upstream, "add", "-A")
	git(t, upstream, "commit", "--quiet", "-m", "on the branch")
	git(t, upstream, "checkout", "--quiet", "main")

	req.PR.Source = forge.Source{Branch: "fix-tax"}
	box, err := m.Up(t.Context(), req, &quietReporter{})
	if err != nil {
		t.Fatalf("Up: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(box.Worktree, "branch.txt")); err != nil || string(got) != "from the branch\n" {
		t.Errorf("branch.txt = %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(box.Worktree, "pr.txt")); err == nil {
		t.Error("the worktree holds refs/pull/7/head, not the branch")
	}
}

func TestUpSaysWhenTheForkIsGone(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	m, req, _ := upFixture(t)
	req.PR.Source = forge.Source{Branch: "fix-tax", Lost: true}
	_, err := m.Up(t.Context(), req, &quietReporter{})
	if err == nil || !strings.Contains(err.Error(), "#7's branch fix-tax is in a repository the service no longer shows") {
		t.Errorf("err = %v", err)
	}
}
