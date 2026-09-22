package forge

import (
	"context"
	"strings"

	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/proc"
)

// LocalHost marks a repository whose remote is a filesystem path. It
// mirrors the constant in internal/workspace, which forge deliberately
// does not import: a forge is chosen from a host name, not from a git
// repository.
const LocalHost = "local"

// For returns the Forge that serves a repository, or explains why
// there is none.
//
// Checking here rather than waiting for the first failed command means
// a reviewer who points pit at a repository it cannot read finds out
// straight away, with the reason, instead of watching a worktree get
// created and then seeing gh complain.
func For(host, repo string, r Runner) (Forge, error) {
	h := strings.ToLower(host)

	switch {
	case h == "github.com" || strings.HasPrefix(h, "github."):
		return GitHub{Runner: r, Repo: repo}, nil

	case strings.Contains(h, "gitlab"):
		return nil, errs.New("%s is a GitLab repository, which pit cannot read yet", repo).
			WithHint("GitLab support is planned; until then pit works with GitHub repositories")

	case h == LocalHost || h == "":
		return nil, errs.New("this repository has no remote on a hosting service").
			WithHint("pit reviews pull requests, so it needs a remote; check `git remote -v`")

	default:
		// A self-hosted GitHub Enterprise instance is not recognisable
		// from its name, so say what is known rather than guessing.
		return nil, errs.New("pit does not know how to read pull requests from %s", host).
			WithHint("pit works with GitHub; if %s is GitHub Enterprise, please report it", host)
	}
}

// Check reports whether gh is usable before pit relies on it.
//
// The two failures it finds are the ones a new user hits first, and
// both have a one-line fix. Discovering them at the moment of use
// instead would mean a half-built sandbox to clean up.
func (g GitHub) Check(ctx context.Context) error {
	if _, err := g.Runner.Output(ctx, proc.Command{Name: "gh", Args: []string{"--version"}}); err != nil {
		// The cause is wrapped rather than repeated: it is worth
		// having under --verbose, but the first line should read as
		// one sentence, not as the same fact said twice.
		return errs.Wrap(err, "pit reads pull requests through the GitHub CLI (gh)").
			WithHint("install it from https://cli.github.com, then run `gh auth login`")
	}

	if _, err := g.Runner.Output(ctx, proc.Command{Name: "gh", Args: []string{"auth", "status"}}); err != nil {
		return errs.Wrap(err, "the GitHub CLI is installed but has no account").
			WithHint("run `gh auth login`")
	}
	return nil
}
