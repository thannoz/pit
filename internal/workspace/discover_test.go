package workspace

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/proc"
)

// stubRunner answers git invocations from a table, so the logic around
// git can be tested without git.
type stubRunner struct {
	// replies maps the joined arguments to the output to return.
	replies map[string]string
	// fail lists argument prefixes that should return an error.
	fail map[string]bool
	// calls records what was asked for.
	calls []proc.Command
}

func (s *stubRunner) Output(_ context.Context, c proc.Command) ([]byte, error) {
	s.calls = append(s.calls, c)
	key := strings.Join(c.Args, " ")
	if s.fail[key] {
		return nil, errors.New("git said no")
	}
	out, ok := s.replies[key]
	if !ok {
		return nil, errors.New("unexpected call: " + key)
	}
	return []byte(out), nil
}

func TestDiscover(t *testing.T) {
	r := &stubRunner{replies: map[string]string{
		"rev-parse --show-toplevel": "/Users/someone/code/shop\n",
		"remote get-url origin":     "git@github.com:acme/shop.git\n",
	}}

	repo, err := Discover(t.Context(), r, "/Users/someone/code/shop/internal/deep")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	if repo.Root != "/Users/someone/code/shop" {
		t.Errorf("Root = %q, want the repository top level", repo.Root)
	}
	if want := (Identity{Host: "github.com", Owner: "acme", Name: "shop"}); repo.Identity != want {
		t.Errorf("Identity = %+v, want %+v", repo.Identity, want)
	}
}

func TestDiscoverAsksGitFromTheGivenDirectory(t *testing.T) {
	const from = "/somewhere/deep"
	r := &stubRunner{replies: map[string]string{
		"rev-parse --show-toplevel": "/repo\n",
		"remote get-url origin":     "https://github.com/acme/shop\n",
	}}

	if _, err := Discover(t.Context(), r, from); err != nil {
		t.Fatalf("Discover: %v", err)
	}

	if len(r.calls) != 2 {
		t.Fatalf("made %d git calls, want 2", len(r.calls))
	}
	if r.calls[0].Dir != from {
		t.Errorf("rev-parse ran in %q, want %q", r.calls[0].Dir, from)
	}
	// Once the root is known, everything else runs from there.
	if r.calls[1].Dir != "/repo" {
		t.Errorf("remote get-url ran in %q, want the repository root", r.calls[1].Dir)
	}
}

func TestDiscoverOutsideARepositoryExplainsItself(t *testing.T) {
	r := &stubRunner{fail: map[string]bool{"rev-parse --show-toplevel": true}}

	_, err := Discover(t.Context(), r, "/not/a/repo")
	if err == nil {
		t.Fatal("want an error outside a repository")
	}
	if !strings.Contains(err.Error(), "not inside a git repository") {
		t.Errorf("error = %q, want it to say what is wrong", err)
	}
	if errs.Hint(err) == "" {
		t.Error("the error carries no hint")
	}
}

func TestDiscoverWithoutARemoteExplainsItself(t *testing.T) {
	r := &stubRunner{
		replies: map[string]string{"rev-parse --show-toplevel": "/repo\n"},
		fail:    map[string]bool{"remote get-url origin": true},
	}

	_, err := Discover(t.Context(), r, "/repo")
	if err == nil {
		t.Fatal("want an error without a remote")
	}
	if !strings.Contains(err.Error(), "origin") {
		t.Errorf("error = %q, want it to name the missing remote", err)
	}
	if errs.Hint(err) == "" {
		t.Error("the error carries no hint")
	}
}

// TestDiscoverAgainstRealGit exercises the same path against the real
// binary, because the stub cannot catch a wrong argument. It needs no
// network: the repository and its remote are both on disk.
func TestDiscoverAgainstRealGit(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	requireGit(t)

	root := t.TempDir()
	gitInit(t, root)
	git(t, root, "remote", "add", "origin", "git@github.com:acme/shop.git")

	// A subdirectory, to prove the search walks upwards.
	sub := filepath.Join(root, "internal", "deep")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	repo, err := Discover(t.Context(), proc.Exec{}, sub)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	// macOS resolves TempDir through /private, so compare suffixes.
	if !strings.HasSuffix(repo.Root, strings.TrimPrefix(root, "/private")) {
		t.Errorf("Root = %q, want it to point at %q", repo.Root, root)
	}
	if want := (Identity{Host: "github.com", Owner: "acme", Name: "shop"}); repo.Identity != want {
		t.Errorf("Identity = %+v, want %+v", repo.Identity, want)
	}
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("skipping: git is not installed")
	}
}

func gitInit(t *testing.T, dir string) {
	t.Helper()
	git(t, dir, "init", "--quiet", "--initial-branch=main")
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	out, err := proc.Exec{}.Output(t.Context(), proc.Command{Name: "git", Args: args, Dir: dir})
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}
