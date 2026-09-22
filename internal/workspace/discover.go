package workspace

import (
	"context"
	"strings"

	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/proc"
)

// DefaultRemote is the remote pit reads the repository identity from.
const DefaultRemote = "origin"

// Runner runs external commands. internal/proc.Exec satisfies it; tests
// pass a stub. The interface is declared here, next to its use, rather
// than in proc, so proc stays a provider of a type instead of a
// contract everything has to agree on.
type Runner interface {
	Output(ctx context.Context, c proc.Command) ([]byte, error)
}

// Repo is a git repository on disk together with the identity pit knows
// it by.
type Repo struct {
	// Root is the absolute path of the repository's top level.
	Root string
	// Identity names the repository independently of where it sits.
	Identity Identity
}

// Discover finds the repository containing dir and works out its
// identity. dir may be any directory inside the repository.
func Discover(ctx context.Context, r Runner, dir string) (Repo, error) {
	root, err := Root(ctx, r, dir)
	if err != nil {
		return Repo{}, err
	}

	raw, err := RemoteURL(ctx, r, root, DefaultRemote)
	if err != nil {
		return Repo{}, err
	}

	id, err := ParseRemoteURL(raw)
	if err != nil {
		return Repo{}, err
	}

	return Repo{Root: root, Identity: id}, nil
}

// Root returns the top level of the repository containing dir.
func Root(ctx context.Context, r Runner, dir string) (string, error) {
	out, err := r.Output(ctx, proc.Command{
		Name: "git",
		Args: []string{"rev-parse", "--show-toplevel"},
		Dir:  dir,
	})
	if err != nil {
		return "", errs.Wrap(err, "%s is not inside a git repository", describe(dir)).
			WithHint("run pit from within a repository, or clone one first")
	}
	return strings.TrimSpace(string(out)), nil
}

// RemoteURL returns the URL configured for the named remote.
func RemoteURL(ctx context.Context, r Runner, dir, remote string) (string, error) {
	out, err := r.Output(ctx, proc.Command{
		Name: "git",
		Args: []string{"remote", "get-url", remote},
		Dir:  dir,
	})
	if err != nil {
		return "", errs.Wrap(err, "the repository has no remote called %q", remote).
			WithHint("add one with `git remote add %s <url>`, or check `git remote -v`", remote)
	}
	return strings.TrimSpace(string(out)), nil
}

// describe keeps error messages readable when dir is empty, which means
// the process's own working directory.
func describe(dir string) string {
	if dir == "" {
		return "the current directory"
	}
	return dir
}
