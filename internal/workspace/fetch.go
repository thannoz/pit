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
	// The base goes too: a sandbox that is gone should not leave the
	// branch it was compared against lying around under pit's name.
	for _, ref := range []string{LocalRef(pr), BaseRef(pr)} {
		if _, err := ResolveRef(ctx, r, repo.Root, ref); err != nil {
			continue // nothing to remove
		}

		if _, err := r.Output(ctx, proc.Command{
			Name: "git",
			Args: []string{"update-ref", "-d", ref},
			Dir:  repo.Root,
		}); err != nil {
			return errs.Wrap(err, "cannot remove %s", ref)
		}
	}
	return nil
}

// PullRequests fetches pull requests for one repository. It exists so
// that other packages can ask for a pull request's commit without
// knowing how git names one.
type PullRequests struct {
	Runner Runner
	Repo   Repo
}

// FetchPullRequest downloads a pull request and returns its commit.
func (p PullRequests) FetchPullRequest(ctx context.Context, number int) (string, error) {
	return Fetch(ctx, p.Runner, p.Repo, number)
}

// ChangedFiles lists the paths that differ between two commits,
// relative to the repository root.
//
// It is how pit tells a change to the frontend from a change to the
// backend: what a diff touches decides what has to be built again.
func ChangedFiles(ctx context.Context, r Runner, repo Repo, from, to string) ([]string, error) {
	out, err := r.Output(ctx, proc.Command{
		Name: "git",
		Args: []string{"diff", "--name-only", from, to},
		Dir:  repo.Root,
	})
	if err != nil {
		return nil, errs.Wrap(err, "cannot compare %s with %s", short(from), short(to))
	}

	var files []string
	for _, line := range strings.Split(string(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			files = append(files, line)
		}
	}
	return files, nil
}

// short is a commit at the length people actually read.
func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// FetchBranch downloads a pull request's commit from its branch into
// pit's ref for the pull request, for a service that keeps no ref of
// its own for pull requests: Bitbucket. fork is the owner/name of the
// fork the branch is in, or empty for the repository itself; a fork is
// fetched from the address origin has, with the fork's path in it, so
// that whatever lets origin in lets the fork in.
func FetchBranch(ctx context.Context, r Runner, repo Repo, pr int, fork, branch string) (string, error) {
	if pr <= 0 {
		return "", errs.New("%d is not a pull request number", pr)
	}
	if branch == "" {
		return "", errs.New("#%d names no branch to fetch", pr)
	}
	source := DefaultRemote
	if fork != "" {
		// The address as written: git applies the reviewer's
		// insteadOf rules to the fork's too when it fetches.
		out, err := r.Output(ctx, proc.Command{Name: "git", Args: []string{"config", "--get", "remote." + DefaultRemote + ".url"}, Dir: repo.Root})
		if err != nil {
			return "", errs.Wrap(err, "cannot read where %s is", DefaultRemote)
		}
		if source, err = siblingURL(strings.TrimSpace(string(out)), fork); err != nil {
			return "", err
		}
	}
	_, err := r.Output(ctx, proc.Command{
		Name: "git",
		Args: []string{"fetch", "--no-tags", "--force", "--quiet", source, "refs/heads/" + branch + ":" + LocalRef(pr)},
		Dir:  repo.Root,
	})
	if err != nil {
		where := "the repository"
		if fork != "" {
			where = "the fork " + fork
		}
		return "", errs.Wrap(err, "cannot fetch #%d's branch %s from %s", pr, branch, where).
			WithHint("the branch may be gone, or the fork private or deleted")
	}
	return ResolveRef(ctx, r, repo.Root, LocalRef(pr))
}

// siblingURL is origin's address with another repository's path in it:
// https://bitbucket.org/acme/shop.git and lisa/shop make
// https://bitbucket.org/lisa/shop.git, and the same for SSH.
func siblingURL(origin, path string) (string, error) {
	suffix := ""
	if strings.HasSuffix(origin, ".git") {
		suffix = ".git"
	}
	// scp-like: git@bitbucket.org:acme/shop.git
	if !strings.Contains(origin, "://") {
		host, _, ok := strings.Cut(origin, ":")
		if !ok {
			return "", errs.New("cannot tell a fork's address from %s", origin)
		}
		return host + ":" + path + suffix, nil
	}
	scheme, rest, _ := strings.Cut(origin, "://")
	host, _, ok := strings.Cut(rest, "/")
	if !ok {
		return "", errs.New("cannot tell a fork's address from %s", origin)
	}
	return scheme + "://" + host + "/" + path + suffix, nil
}
