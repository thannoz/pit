package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/proc"
)

// Worktree is a pull request checked out on disk, separate from the
// reviewer's own working copy.
type Worktree struct {
	// Path is the directory the pull request is checked out in.
	Path string
	// PR is the pull request number it holds.
	PR int
	// SHA is the commit that was checked out.
	SHA string
}

// AddWorktree checks pull request pr out into its own directory. The
// pull request must have been fetched first.
//
// The checkout is detached: pit never creates a branch, so `git branch`
// in the reviewer's repository stays exactly as they left it.
func AddWorktree(ctx context.Context, r Runner, repo Repo, stateDir string, pr int) (Worktree, error) {
	sha, err := ResolveRef(ctx, r, repo.Root, LocalRef(pr))
	if err != nil {
		return Worktree{}, errs.Wrap(err, "pull request #%d has not been fetched", pr).
			WithHint("fetch it first")
	}

	path := repo.Identity.WorktreeDir(stateDir, pr)

	// An existing worktree at the same path is reused when it already
	// holds the right commit, and replaced when it does not. Re-running
	// pit on the same pull request is the normal case, not an error.
	if existing, ok := worktreeAt(ctx, r, repo, path); ok {
		if existing == sha {
			return Worktree{Path: path, PR: pr, SHA: sha}, nil
		}
		if err := RemoveWorktree(ctx, r, repo, path); err != nil {
			return Worktree{}, err
		}
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Worktree{}, errs.Wrap(err, "cannot create %s", filepath.Dir(path))
	}

	if _, err := r.Output(ctx, proc.Command{
		Name: "git",
		Args: []string{"worktree", "add", "--detach", "--quiet", path, sha},
		Dir:  repo.Root,
	}); err != nil {
		return Worktree{}, errs.Wrap(err, "cannot check pull request #%d out into %s", pr, path).
			WithHint("remove %s and try again", path)
	}

	return Worktree{Path: path, PR: pr, SHA: sha}, nil
}

// RemoveWorktree deletes a worktree and forgets it. It is not an error
// to remove one that is not there, so cleanup can run unconditionally.
func RemoveWorktree(ctx context.Context, r Runner, repo Repo, path string) error {
	// --force is needed for two ordinary cases: the review left changes
	// in the worktree, and the directory was deleted by hand.
	if _, err := r.Output(ctx, proc.Command{
		Name: "git",
		Args: []string{"worktree", "remove", "--force", path},
		Dir:  repo.Root,
	}); err != nil {
		// git refuses if it does not know the path. Either way the goal
		// is for it to be gone, so fall through to pruning.
		if _, statErr := os.Stat(path); statErr == nil {
			if rmErr := os.RemoveAll(path); rmErr != nil {
				return errs.Wrap(rmErr, "cannot remove %s", path)
			}
		}
	}

	// Pruning clears the administrative entry git keeps under .git, which
	// otherwise makes the path unusable for the next worktree.
	if _, err := r.Output(ctx, proc.Command{
		Name: "git",
		Args: []string{"worktree", "prune"},
		Dir:  repo.Root,
	}); err != nil {
		return errs.Wrap(err, "cannot prune stale worktree entries")
	}
	return nil
}

// worktreeAt reports the commit checked out at path, if git knows about
// a worktree there.
func worktreeAt(ctx context.Context, r Runner, repo Repo, path string) (string, bool) {
	out, err := r.Output(ctx, proc.Command{
		Name: "git",
		Args: []string{"worktree", "list", "--porcelain"},
		Dir:  repo.Root,
	})
	if err != nil {
		return "", false
	}

	// The porcelain format is stanzas separated by blank lines:
	//   worktree /path
	//   HEAD <sha>
	//   detached
	var current string
	for _, line := range strings.Split(string(out), "\n") {
		field, value, _ := strings.Cut(strings.TrimSpace(line), " ")
		switch field {
		case "worktree":
			current = value
		case "HEAD":
			if sameDir(current, path) {
				return value, true
			}
		}
	}
	return "", false
}

// sameDir compares two paths allowing for symlinked parents, which is
// how macOS reports anything under /var.
func sameDir(a, b string) bool {
	if a == b {
		return true
	}
	ra, err := filepath.EvalSymlinks(a)
	if err != nil {
		return false
	}
	rb, err := filepath.EvalSymlinks(b)
	if err != nil {
		return false
	}
	return ra == rb
}
