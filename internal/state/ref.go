package state

import (
	"fmt"
	"path"
	"strconv"
	"strings"
)

// Ref names a pull request, optionally qualified by its repository:
// "482" or "acme/shop#482" or "github.com/acme/shop#482".
//
// The qualified form exists because pit's record is global. A number on
// its own is usually enough, and when it is not, the answer has to be
// something a reviewer can type -- not "go and stand somewhere else".
type Ref struct {
	// Repo is the repository part, or empty when none was given.
	Repo string
	// PR is the pull request number.
	PR int
}

// ParseRef reads a pull request reference.
func ParseRef(s string) (Ref, error) {
	s = strings.TrimSpace(s)

	repo, number, qualified := strings.Cut(s, "#")
	if !qualified {
		repo, number = "", s
	}

	pr, err := strconv.Atoi(number)
	if err != nil || pr <= 0 {
		return Ref{}, fmt.Errorf("%q is not a pull request number", s)
	}
	return Ref{Repo: strings.TrimSpace(repo), PR: pr}, nil
}

// String renders the reference the way it can be typed back in.
func (r Ref) String() string {
	if r.Repo == "" {
		return strconv.Itoa(r.PR)
	}
	return r.Repo + "#" + strconv.Itoa(r.PR)
}

// Matches reports whether a sandbox is the one this reference names.
// An unqualified reference matches on the number alone; a qualified one
// also accepts the short "owner/name" form of a repository.
func (r Ref) Matches(s Sandbox) bool {
	if s.PR != r.PR {
		return false
	}
	if r.Repo == "" {
		return true
	}
	return s.Repo == r.Repo || strings.HasSuffix(s.Repo, "/"+r.Repo)
}

// ShortRepo is the repository without the part that carries no
// information: the host, or the path leading up to a local clone.
func (s Sandbox) ShortRepo() string { return shortRepo(s.Repo) }

// QualifiedRef is how this sandbox can be named unambiguously.
func (s Sandbox) QualifiedRef() string {
	return fmt.Sprintf("%s#%d", shortRepo(s.Repo), s.PR)
}

// Describe names the sandbox the way a person would recognise it.
func (s Sandbox) Describe() string {
	parts := fmt.Sprintf("#%d", s.PR)
	if s.Title != "" {
		parts += fmt.Sprintf(" %q", s.Title)
	}
	if s.Branch != "" {
		parts += " (" + s.Branch + ")"
	}
	return parts + " in " + shortRepo(s.Repo)
}

// LocalHost marks a repository whose remote is a filesystem path. It
// mirrors the constant in internal/workspace.
const LocalHost = "local"

// shortRepo drops what carries no information in a list.
//
// For a hosted repository that is the host: it is the same for
// everything a person works on. For a filesystem remote the rest is an
// absolute path, and the directory it ends in is the only part anyone
// recognises -- "local//tmp/pit-t317/shop-upstream" is noise.
func shortRepo(repo string) string {
	host, rest, ok := strings.Cut(repo, "/")
	if !ok {
		return repo
	}

	switch {
	case host == LocalHost:
		return path.Base(rest)
	case strings.Contains(host, "."):
		return rest
	default:
		return repo
	}
}
