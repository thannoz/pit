// Package forge abstracts the service a pull request lives on. GitHub
// is the only implementation today; the interface exists so that adding
// GitLab is a new file rather than a rewrite.
package forge

import (
	"context"
	"fmt"
	"strings"
)

// State is where a pull request stands. pit cares because reviewing a
// merged or closed pull request is nearly always a mistake, and saying
// so is cheaper than letting someone wonder why the change looks odd.
type State string

// The states a pull request can be in, as pit spells them. GitHub
// shouts its own ("MERGED"); parseState folds them onto these.
const (
	Open   State = "open"
	Merged State = "merged"
	Closed State = "closed"
)

// PR is what pit needs to know about a pull request.
type PR struct {
	// Number is the pull request's own number.
	Number int
	// Title and Author are shown to the reviewer, so they can confirm
	// they are looking at what they meant to.
	Title  string
	Author string
	// Branch is the head branch's name. It is for display only: pit
	// fetches by ref number, not by branch, because a fork's branch
	// name is not resolvable in the target repository.
	Branch string
	// HeadSHA is the commit under review.
	HeadSHA string
	// BaseSHA is the commit it would be merged into. P9 compares
	// against it; P6 diffs against it.
	BaseSHA string
	// BaseBranch is where it is headed.
	BaseBranch string
	// State says whether it is still open.
	State State
	// Draft marks a pull request its author has not finished.
	Draft bool
	// URL is the page a human would open.
	URL string
	// Limited says the metadata came from the commit rather than from
	// a hosting service, so Branch, State and URL are unknown. Worth
	// saying once, because "open" then means "not known to be closed".
	Limited bool
}

// Describe renders the pull request the way a reviewer would recognise
// it.
func (p PR) Describe() string {
	if p.Title == "" {
		return fmt.Sprintf("#%d", p.Number)
	}

	by := "@" + p.Author
	if p.Limited {
		// Not a login: a commit records a person's name, and printing
		// it with an @ would claim an account that may not exist.
		by = p.Author
	}
	s := fmt.Sprintf("#%d %q by %s", p.Number, p.Title, by)
	switch {
	case p.Draft:
		s += " (draft)"
	case p.State != Open:
		s += " (" + string(p.State) + ")"
	}
	return s
}

// Forge reads pull requests from a hosting service.
//
// Only what pit needs today is declared. Go interfaces are satisfied
// implicitly, so adding the diff in P6 and comments in P8 means adding
// methods then -- not carrying empty ones that fail if anyone calls
// them.
type Forge interface {
	// PullRequest returns the metadata of one pull request.
	PullRequest(ctx context.Context, number int) (PR, error)
}

// Commenter posts comments on pull requests. It is apart from Forge
// because not every forge can: pit reads a pull request from a plain
// git server by its commits, and there is nowhere there to comment.
type Commenter interface {
	// Comment posts a comment, in Markdown, on a pull request and
	// returns where it can be seen.
	Comment(ctx context.Context, number int, body string) (string, error)
}

// parseState maps a service's own spelling onto pit's.
func parseState(raw string) State {
	switch strings.ToLower(raw) {
	case "merged":
		return Merged
	case "closed":
		return Closed
	default:
		return Open
	}
}
