package sandbox_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/thannoz/pit/internal/proc"
	"github.com/thannoz/pit/internal/workspace"
)

// newUpstream builds a clone whose "remote" carries pull request refs,
// so the whole of Up can run against the real git binary without a
// network or an account.
func newUpstream(t *testing.T) workspace.Repo {
	t.Helper()

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("skipping: git is not installed")
	}

	base := t.TempDir()
	upstream := filepath.Join(base, "upstream")
	clone := filepath.Join(base, "clone")

	if err := os.MkdirAll(upstream, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	git(t, upstream, "init", "--quiet", "--initial-branch=main")
	identify(t, upstream)
	writeFile(t, filepath.Join(upstream, "docker-compose.yml"), "services:\n  web:\n    image: nginx\n")
	git(t, upstream, "add", "-A")
	git(t, upstream, "commit", "--quiet", "-m", "initial")

	git(t, base, "clone", "--quiet", upstream, clone)
	identify(t, clone)

	repo := workspace.Repo{
		Root: clone,
		Identity: workspace.Identity{
			Host: workspace.LocalHost, Owner: filepath.Dir(upstream), Name: "upstream",
		},
	}
	addPullRequest(t, clone, 7)
	return repo
}

// addPullRequest publishes a pull request ref on the clone's remote.
func addPullRequest(t *testing.T, clone string, pr int) {
	t.Helper()

	upstream := remoteOf(t, clone)
	branch := "pit-test-pr-" + strconv.Itoa(pr)

	git(t, upstream, "checkout", "--quiet", "-B", branch)
	writeFile(t, filepath.Join(upstream, "pr.txt"), "change for #"+strconv.Itoa(pr)+"\n")
	git(t, upstream, "add", "-A")
	git(t, upstream, "commit", "--quiet", "-m", "change for #"+strconv.Itoa(pr))
	git(t, upstream, "update-ref", workspace.RemotePullRef(workspace.LocalHost, pr), head(t, upstream))
	git(t, upstream, "checkout", "--quiet", "main")
}

// advancePullRequest adds a commit to a pull request's branch, the way
// an author pushing a fix does.
func advancePullRequest(t *testing.T, clone string, pr int) {
	t.Helper()

	upstream := remoteOf(t, clone)
	branch := "pit-test-pr-" + strconv.Itoa(pr)

	git(t, upstream, "checkout", "--quiet", branch)
	writeFile(t, filepath.Join(upstream, "pr.txt"), "another change for #"+strconv.Itoa(pr)+"\n")
	git(t, upstream, "add", "-A")
	git(t, upstream, "commit", "--quiet", "-m", "another change for #"+strconv.Itoa(pr))
	git(t, upstream, "update-ref", workspace.RemotePullRef(workspace.LocalHost, pr), head(t, upstream))
	git(t, upstream, "checkout", "--quiet", "main")
}

func remoteOf(t *testing.T, clone string) string {
	t.Helper()

	out, err := proc.Exec{}.Output(t.Context(), proc.Command{
		Name: "git", Args: []string{"remote", "get-url", "origin"}, Dir: clone,
	})
	if err != nil {
		t.Fatalf("git remote get-url: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func head(t *testing.T, dir string) string {
	t.Helper()

	out, err := proc.Exec{}.Output(t.Context(), proc.Command{
		Name: "git", Args: []string{"rev-parse", "HEAD"}, Dir: dir,
	})
	if err != nil {
		t.Fatalf("git rev-parse: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func identify(t *testing.T, dir string) {
	t.Helper()
	git(t, dir, "config", "user.email", "pit@example.test")
	git(t, dir, "config", "user.name", "pit tests")
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()

	out, err := proc.Exec{}.Output(t.Context(), proc.Command{Name: "git", Args: args, Dir: dir})
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile(%q): %v", path, err)
	}
}
