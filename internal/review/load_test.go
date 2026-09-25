package review_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/proc"
	"github.com/thannoz/pit/internal/review"
	"github.com/thannoz/pit/internal/state"
	"github.com/thannoz/pit/internal/workspace"
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

// A scenario only the reviewer's .pit.yaml has was loaded from there,
// and its example values come from there too (T-706).
func TestLoadTakesTheValuesOfAScenarioOnlyTheReviewerHas(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	const compose = "services:\n  web:\n    image: nginx\n"
	root, worktree := t.TempDir(), t.TempDir()
	files := map[string]string{
		filepath.Join(root, "docker-compose.yml"):     compose,
		filepath.Join(root, ".pit.yaml"):              "version: 1\nweb: {service: web, port: 80}\ndata:\n  scenarios:\n    - {name: voucher, params: {id: \"1002\"}}\n",
		filepath.Join(worktree, ".git"):               "",
		filepath.Join(worktree, "docker-compose.yml"): compose,
		filepath.Join(worktree, ".pit.yaml"):          "version: 1\nweb: {service: web, port: 80}\ndata:\n  scenarios:\n    - {name: standard, params: {id: \"1001\"}}\n",
	}
	for path, content := range files {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{
		{"init", "-q"}, {"add", "-A"},
		{"-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-qm", "base"},
		{"update-ref", workspace.BaseRef(7), "HEAD"},
	} {
		if out, err := exec.CommandContext(t.Context(), "git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	in, err := review.Load(t.Context(), proc.Exec{}, state.Sandbox{PR: 7, RepoRoot: root, Worktree: worktree, SHA: "HEAD", Scenario: "voucher"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if in.Params["id"] != "1002" {
		t.Errorf("params = %v", in.Params)
	}

	// The pull request's own scenario keeps its own values.
	in, err = review.Load(t.Context(), proc.Exec{}, state.Sandbox{PR: 7, RepoRoot: root, Worktree: worktree, SHA: "HEAD", Scenario: "standard"})
	if err != nil || in.Params != nil {
		t.Errorf("params = %v, %v", in.Params, err)
	}
}
