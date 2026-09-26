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
// GitHub, GitLab, Gitea, Forgejo and Bitbucket are asked for a pull
// request's title, author and state. Everywhere else pit falls back to
// reading the commit, which is less but is not nothing -- and is the
// difference between working on a company's own server and against a
// local repository, and refusing to.
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
	if isGitea(o.Host) {
		return Gitea{Host: o.Host, Repo: o.Repo, Token: GiteaTokenFromEnv()}, nil
	}
	if isBitbucket(o.Host) {
		return BitbucketFromEnv(o.Repo), nil
	}

	var git Forge
	if o.Resolver != nil {
		git = Git{Runner: o.Runner, Resolver: o.Resolver, Dir: o.Dir}
	}
	if o.Host == LocalHost || o.Host == "" {
		if git == nil {
			return nil, errs.New("pit cannot read pull requests from %s", o.Host).
				WithHint("this is a bug in pit; please report it")
		}
		return git, nil
	}
	// A company's own server has a name of its own; whether it runs
	// Gitea or Forgejo is asked the first time it matters.
	return Probing{Gitea: Gitea{Host: o.Host, Repo: o.Repo, Token: GiteaTokenFromEnv()}, Git: git}, nil
}

// Probing is a host pit does not know by name: it is asked whether it
// runs Gitea or Forgejo, and when it does not, the commit is read.
type Probing struct {
	Gitea Gitea
	// Git reads the commit; nil where only commenting was asked for.
	Git Forge
}

var (
	_ Forge     = Probing{}
	_ Commenter = Probing{}
)

// PullRequest asks the host's API, or reads the commit.
func (p Probing) PullRequest(ctx context.Context, number int) (PR, error) {
	if p.Gitea.IsGitea(ctx) {
		return p.Gitea.PullRequest(ctx, number)
	}
	if p.Git == nil {
		return PR{}, errs.New("pit cannot read pull requests from %s", p.Gitea.Host)
	}
	return p.Git.PullRequest(ctx, number)
}

// Comment posts on a host that runs Gitea or Forgejo.
func (p Probing) Comment(ctx context.Context, number int, body string) (string, error) {
	if !p.Gitea.IsGitea(ctx) {
		return "", errs.New("pit can post comments on GitHub, GitLab, Gitea, Forgejo and Bitbucket, and %s is none it recognises", p.Gitea.Host).
			WithHint("the comment is above; paste it into the pull request yourself")
	}
	return p.Gitea.Comment(ctx, number, body)
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
