package workspace

import (
	"context"
	"fmt"
	"strings"

	"github.com/thannoz/pit/internal/diff"
	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/proc"
)

// BaseRef is where the branch a pull request targets is kept after
// fetching. A sibling of LocalRef rather than a child: git cannot have
// both refs/pit/482 and refs/pit/482/base, because the first is a file
// where the second needs a directory.
func BaseRef(pr int) string {
	return fmt.Sprintf("%s/%d-base", LocalRefPrefix, pr)
}

// FetchBase fetches the branch a pull request wants to be merged into.
//
// Into a ref of pit's own, like the pull request itself. Fetching into
// origin/main would be simpler and would move the reviewer's
// remote-tracking branch under them -- the kind of side effect pit
// promises not to have.
//
// An empty branch means the remote's default branch, which is the best
// guess there is when no hosting service can be asked.
func FetchBase(ctx context.Context, r Runner, repo Repo, pr int, branch string) (string, error) {
	source := "HEAD"
	if branch != "" {
		source = "refs/heads/" + branch
	}
	target := BaseRef(pr)

	// --refmap= is not decoration. Without it git updates origin/main
	// as well whenever the fetched ref matches the remote's configured
	// refspec -- an explicit destination does not stop it. That is the
	// side effect this function exists to avoid, and it only shows up
	// when someone checks.
	if _, err := r.Output(ctx, proc.Command{
		Name: "git",
		Args: []string{"fetch", "--quiet", "--no-tags", "--force", "--refmap=", DefaultRemote, source + ":" + target},
		Dir:  repo.Root,
	}); err != nil {
		what := "the default branch"
		if branch != "" {
			what = fmt.Sprintf("%q", branch)
		}
		return "", errs.Wrap(err, "cannot fetch %s, which #%d wants to be merged into", what, pr).
			WithHint("check that %s still exists on %s", what, DefaultRemote)
	}
	return ResolveRef(ctx, r, repo.Root, target)
}

// Changes is what a pull request changes: every file, how it changed,
// and where inside it.
//
// Computed here, from the commits pit already has, rather than asked
// of the hosting service. A hosting service caps how much of a diff it
// hands out; git does not, and a review guide built on part of a diff
// would be quietly wrong about the rest. It also means a repository
// with no hosting service to ask gets the same answer.
//
// The comparison is from the merge base -- where the branch left its
// target -- not from the target's tip, so that changes made to the
// target since are not mistaken for the pull request's own. That is
// git's three-dot form, and it is what a hosting service shows too.
func Changes(ctx context.Context, r Runner, repo Repo, base, head string) (diff.Diff, error) {
	mergeBase, err := r.Output(ctx, proc.Command{
		Name: "git", Args: []string{"merge-base", base, head}, Dir: repo.Root,
	})
	if err != nil {
		return diff.Diff{}, errs.Wrap(err, "%s and %s have no commit in common", short(base), short(head)).
			WithHint("the pull request's branch does not come from its target; compare them yourself with `git log`")
	}
	from := strings.TrimSpace(string(mergeBase))

	summary, err := r.Output(ctx, proc.Command{
		Name: "git",
		Args: []string{"diff", "--raw", "--numstat", "-z", "--find-renames", "--no-color", from, head},
		Dir:  repo.Root,
	})
	if err != nil {
		return diff.Diff{}, errs.Wrap(err, "cannot list what changed between %s and %s", short(from), short(head))
	}
	files, err := diff.ParseSummary(summary)
	if err != nil {
		return diff.Diff{}, err
	}

	patch, err := r.Output(ctx, proc.Command{
		Name: "git",
		Args: []string{"diff", "--unified=0", "--find-renames", "--no-color", "--no-ext-diff", from, head},
		Dir:  repo.Root,
	})
	if err != nil {
		return diff.Diff{}, errs.Wrap(err, "cannot read what changed between %s and %s", short(from), short(head))
	}
	if err := diff.AttachHunks(files, patch); err != nil {
		return diff.Diff{}, err
	}

	return diff.Diff{Base: from, Head: head, Files: files}, nil
}
