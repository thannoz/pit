package forge

import (
	"context"
	"strings"

	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/proc"
)

// Resolver fetches a pull request's ref and reports the commit it
// points at. internal/workspace provides one; the interface is here so
// that forge does not depend on it.
type Resolver interface {
	FetchPullRequest(ctx context.Context, number int) (string, error)
}

// Git reads what it can about a pull request from git alone.
//
// It exists because a hosting service is a source of convenience, not a
// requirement. The ref a pull request lives at is a convention pit
// already knows, and the commit carries a subject and an author. That
// is enough to review, and it is the difference between pit working on
// GitLab, on a company's own server and against a local repository --
// and pit not working there at all.
type Git struct {
	// Runner runs git.
	Runner Runner
	// Resolver fetches the pull request's ref.
	Resolver Resolver
	// Dir is the repository the commit is read from.
	Dir string
}

// PullRequest fetches the pull request's ref and describes the commit.
//
// The fetch is what makes the metadata available at all, and it is the
// same fetch the setup performs a moment later -- git makes the second
// one a no-op.
func (g Git) PullRequest(ctx context.Context, number int) (PR, error) {
	if number <= 0 {
		return PR{}, errs.New("%d is not a pull request number", number)
	}

	sha, err := g.Resolver.FetchPullRequest(ctx, number)
	if err != nil {
		return PR{}, err
	}

	subject, author, err := g.describe(ctx, sha)
	if err != nil {
		return PR{}, err
	}

	return PR{
		Number:  number,
		Title:   subject,
		Author:  author,
		HeadSHA: sha,
		// A hosting service would know the branch, whether the pull
		// request is still open, and where to find it in a browser.
		// git does not, and guessing would be worse than admitting it.
		State:   Open,
		Limited: true,
	}, nil
}

// describe reads a commit's subject and author.
func (g Git) describe(ctx context.Context, sha string) (subject, author string, err error) {
	out, err := g.Runner.Output(ctx, proc.Command{
		Name: "git",
		// %s is the subject, %an the author's name; the separator is
		// one that will not appear in either.
		Args: []string{"show", "--no-patch", "--format=%s%x1f%an", sha},
		Dir:  g.Dir,
	})
	if err != nil {
		return "", "", errs.Wrap(err, "cannot read the commit %s", short(sha))
	}

	subject, author, _ = strings.Cut(strings.TrimSpace(string(out)), "\x1f")
	return subject, author, nil
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
