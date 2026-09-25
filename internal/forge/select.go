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

// Options is what For needs to choose a forge.
type Options struct {
	// Host is the service the repository lives on.
	Host string
	// Repo is the repository in owner/name form.
	Repo string
	// Runner runs gh and git.
	Runner Runner
	// Resolver fetches a pull request's ref, for the git fallback.
	Resolver Resolver
	// Dir is the repository root, for reading commits.
	Dir string
}

// For returns the forge that serves a repository.
//
// Only GitHub can be asked for a pull request's title, author and
// state. Everywhere else pit falls back to reading the commit, which
// is less but is not nothing -- and is the difference between working
// on GitLab, on a company's own server and against a local repository,
// and refusing to.
//
// An earlier version returned an error for every host it did not
// recognise. That was honest and useless: it blocked the tool on most
// of the world's repositories, and it blocked testing pit against a
// local one.
func For(o Options) (Forge, error) {
	if o.Runner == nil {
		return nil, errs.New("no way to run commands")
	}

	if isGitHub(o.Host) {
		return GitHub{Runner: o.Runner, Repo: o.Repo}, nil
	}
	if isGitLab(o.Host) {
		return GitLab{Host: o.Host, Project: o.Repo, Token: TokenFromEnv()}, nil
	}

	if o.Resolver == nil {
		return nil, errs.New("pit cannot read pull requests from %s", o.Host).
			WithHint("this is a bug in pit; please report it")
	}
	return Git{Runner: o.Runner, Resolver: o.Resolver, Dir: o.Dir}, nil
}

// isGitHub recognises github.com and the naming self-hosted GitHub
// Enterprise instances usually take. A wrong guess costs a fallback to
// git, not a failure.
func isGitHub(host string) bool {
	h := strings.ToLower(host)
	return h == "github.com" || strings.HasPrefix(h, "github.")
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
