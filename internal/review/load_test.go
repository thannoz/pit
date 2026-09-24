package review_test

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/proc"
	"github.com/thannoz/pit/internal/review"
	"github.com/thannoz/pit/internal/state"
)

// A sandbox set up before pit fetched the branch a pull request goes
// into cannot say what changed, and the message says how to fix that.
func TestLoadWithoutTheBaseSaysWhatToDo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	dir := t.TempDir()
	if out, err := exec.CommandContext(t.Context(), "git", "init", "-q", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	_, err := review.Load(t.Context(), proc.Exec{}, state.Sandbox{PR: 7, RepoRoot: dir, Worktree: dir, SHA: "HEAD"})
	if err == nil {
		t.Fatal("no error without a base")
	}
	if !strings.Contains(err.Error(), "which branch #7 goes into") || !strings.Contains(errs.Hint(err), "`pit 7`") {
		t.Errorf("err = %v, hint = %q", err, errs.Hint(err))
	}
}
