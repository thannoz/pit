package workspace

import (
	"context"
	"fmt"
	"strings"

	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/proc"
)

// LocalRefPrefix is where pit keeps the pull requests it has fetched.
// A namespace of its own means pit never creates a local branch, so
// `git branch` and the user's own refs stay exactly as they were.
const LocalRefPrefix = "refs/pit"

// LocalRef is where pull request pr is stored after fetching.
func LocalRef(pr int) string {
	return fmt.Sprintf("%s/%d", LocalRefPrefix, pr)
}

// RemotePullRef is the ref a pull request lives at on the remote.
// Hosting services disagree about this, and the value is not
// discoverable, so it has to be known per host. Only GitHub is
// exercised today; the GitLab entry is here because getting it wrong
// silently is worse than having it unused.
func RemotePullRef(host string, pr int) string {
	switch {
	case strings.Contains(host, "gitlab"):
		return fmt.Sprintf("refs/merge-requests/%d/head", pr)
	default:
		return fmt.Sprintf("refs/pull/%d/head", pr)
	}
}

// Fetch downloads pull request pr into pit's own ref namespace and
// returns the commit it points at.
//
// Nothing the user can see changes: no branch is created, no branch is
// moved, HEAD stays where it was and the working tree is untouched.
// That is the whole point -- a reviewer must never have to stash work
// to look at someone else's pull request.
func Fetch(ctx context.Context, r Runner, repo Repo, pr int) (string, error) {
	if pr <= 0 {
		return "", errs.New("%d is not a pull request number", pr)
	}

	remoteRef := RemotePullRef(repo.Identity.Host, pr)
	refspec := remoteRef + ":" + LocalRef(pr)

	// --force matters: a pull request branch gets force-pushed all the
	// time, and without it the second fetch of the same number fails.
	// --no-tags keeps someone else's tags out of the local repository.
	_, err := r.Output(ctx, proc.Command{
		Name: "git",
		Args: []string{"fetch", "--no-tags", "--force", "--quiet", DefaultRemote, refspec},
		Dir:  repo.Root,
	})
	if err != nil {
		return "", errs.Wrap(err, "cannot fetch pull request #%d from %s", pr, repo.Identity).
			WithHint("check that #%d exists and that you have access to the repository", pr)
	}

	return ResolveRef(ctx, r, repo.Root, LocalRef(pr))
}

// ResolveRef returns the commit a ref points at.
func ResolveRef(ctx context.Context, r Runner, dir, ref string) (string, error) {
	out, err := r.Output(ctx, proc.Command{
		Name: "git",
		Args: []string{"rev-parse", "--verify", ref + "^{commit}"},
		Dir:  dir,
	})
	if err != nil {
		return "", errs.Wrap(err, "cannot resolve %s", ref)
	}
	return strings.TrimSpace(string(out)), nil
}

// DeleteRef removes the ref a fetched pull request is stored under. It
// is not an error to delete one that was never fetched, so cleanup can
// run without first checking.
func DeleteRef(ctx context.Context, r Runner, repo Repo, pr int) error {
	ref := LocalRef(pr)

	if _, err := ResolveRef(ctx, r, repo.Root, ref); err != nil {
		return nil // nothing to remove
	}

	if _, err := r.Output(ctx, proc.Command{
		Name: "git",
		Args: []string{"update-ref", "-d", ref},
		Dir:  repo.Root,
	}); err != nil {
		return errs.Wrap(err, "cannot remove %s", ref)
	}
	return nil
}
