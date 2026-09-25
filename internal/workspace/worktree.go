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
	return AddWorktreeAt(ctx, r, repo, repo.Identity.WorktreeDir(stateDir, pr), pr, sha)
}

// AddWorktreeAt checks a commit out at path for pull request pr: its
// own commit, or the one it goes into.
func AddWorktreeAt(ctx context.Context, r Runner, repo Repo, path string, pr int, sha string) (Worktree, error) {
	// An existing worktree at the same path is reused when it already
	// holds the right commit, and moved to the new one when it does not.
	// Re-running pit on the same pull request is the normal case, not an
	// error.
	if existing, ok := worktreeAt(ctx, r, repo, path); ok {
		if existing == sha {
			return Worktree{Path: path, PR: pr, SHA: sha}, nil
		}
		if updateInPlace(ctx, r, path, sha) == nil {
			return Worktree{Path: path, PR: pr, SHA: sha}, nil
		}
		// A checkout git cannot move -- its directory deleted by hand,
		// its index broken -- is replaced, as it was before there was
		// anything to keep.
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

// updateInPlace moves a worktree to another commit where it is.
//
// Replacing it would be simpler and wrong: the sandbox's containers
// bind-mount its directories -- ./migrations into the database, ./src
// into the application -- and a container that is not recreated keeps
// the directory it was given. A replaced directory is a deleted one, and
// the container sees it empty. Checked out in place, the directories
// stay the same ones and show the new commit's files.
//
// The result is the commit and nothing else: changes to tracked files
// are discarded and files git does not know are removed, as a fresh
// checkout would have them. What the project ignores stays -- installed
// dependencies, build caches -- which a fresh checkout would have thrown
// away for nothing.
func updateInPlace(ctx context.Context, r Runner, path, sha string) error {
	for _, args := range [][]string{
		{"checkout", "--quiet", "--force", "--detach", sha},
		{"clean", "--quiet", "--force", "-d"},
	} {
		if _, err := r.Output(ctx, proc.Command{Name: "git", Args: args, Dir: path}); err != nil {
			return err
		}
	}
	return nil
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
//
// A path that no longer exists -- a worktree deleted by hand -- is
// resolved through the nearest parent that does: git still lists it,
// under /private/var, and pit has to recognise it to clear it away.
func sameDir(a, b string) bool {
	if a == b {
		return true
	}
	return resolve(a) == resolve(b)
}

// resolve follows the symlinks in the part of p that exists.
func resolve(p string) string {
	p = filepath.Clean(p)
	var rest []string
	for {
		if real, err := filepath.EvalSymlinks(p); err == nil {
			return filepath.Join(append([]string{real}, rest...)...)
		}
		parent := filepath.Dir(p)
		if parent == p {
			return filepath.Join(append([]string{p}, rest...)...)
		}
		rest = append([]string{filepath.Base(p)}, rest...)
		p = parent
	}
}

// Remove tears down everything pit created for pull request pr: the
// worktree, git's record of it, the fetched ref and, once it is empty,
// the directory the repository's sandboxes lived in.
//
// It is safe to call when none of that exists. Cleanup runs on paths
// where setup failed half way, so it has to cope with any subset of the
// work having happened.
func Remove(ctx context.Context, r Runner, repo Repo, stateDir string, pr int) error {
	if err := RemoveWorktree(ctx, r, repo, repo.Identity.WorktreeDir(stateDir, pr)); err != nil {
		return err
	}
	if err := DeleteRef(ctx, r, repo, pr); err != nil {
		return err
	}

	// An empty repository directory is litter. A non-empty one holds
	// another pull request, so leave it alone -- which os.Remove does
	// for us by refusing.
	_ = os.Remove(repo.Identity.RepoDir(stateDir))
	return nil
}
