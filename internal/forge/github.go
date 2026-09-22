package forge

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/proc"
)

// Runner runs external commands. internal/proc.Exec satisfies it.
type Runner interface {
	Output(ctx context.Context, c proc.Command) ([]byte, error)
}

// GitHub reads pull requests through the gh CLI.
//
// gh rather than the REST API, because gh is already installed and
// already authenticated on the machines pit targets. An API client
// would mean an OAuth device flow, a token in a keychain and a
// callback server before the first pull request could be read.
type GitHub struct {
	// Runner runs gh.
	Runner Runner
	// Repo is the repository in owner/name form.
	Repo string
}

// ghFields are the JSON fields pit asks for. Naming them explicitly
// keeps the response small and makes a field that disappears from gh a
// loud failure rather than a quietly empty struct.
var ghFields = []string{
	"number", "title", "author",
	"headRefName", "headRefOid",
	"baseRefName", "baseRefOid",
	"state", "isDraft", "url",
}

// ghPR mirrors gh's own JSON shape, which is not pit's.
type ghPR struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Author struct {
		Login string `json:"login"`
	} `json:"author"`
	HeadRefName string `json:"headRefName"`
	HeadRefOid  string `json:"headRefOid"`
	BaseRefName string `json:"baseRefName"`
	BaseRefOid  string `json:"baseRefOid"`
	State       string `json:"state"`
	IsDraft     bool   `json:"isDraft"`
	URL         string `json:"url"`
}

// PullRequest reads one pull request's metadata.
func (g GitHub) PullRequest(ctx context.Context, number int) (PR, error) {
	if number <= 0 {
		return PR{}, errs.New("%d is not a pull request number", number)
	}

	args := []string{"pr", "view", strconv.Itoa(number), "--json", strings.Join(ghFields, ",")}
	if g.Repo != "" {
		// Naming the repository means pit works from a worktree, from
		// a subdirectory, and from a clone whose default remote is not
		// the one being reviewed.
		args = append(args, "--repo", g.Repo)
	}

	out, err := g.Runner.Output(ctx, proc.Command{Name: "gh", Args: args})
	if err != nil {
		return PR{}, describeFailure(err, number, g.Repo)
	}

	var raw ghPR
	if err := json.Unmarshal(out, &raw); err != nil {
		return PR{}, errs.Wrap(err, "cannot read what gh reported about #%d", number).
			WithHint("check that `gh` is up to date")
	}

	return PR{
		Number:     raw.Number,
		Title:      raw.Title,
		Author:     raw.Author.Login,
		Branch:     raw.HeadRefName,
		HeadSHA:    raw.HeadRefOid,
		BaseSHA:    raw.BaseRefOid,
		BaseBranch: raw.BaseRefName,
		State:      parseState(raw.State),
		Draft:      raw.IsDraft,
		URL:        raw.URL,
	}, nil
}

// describeFailure turns gh's exit into something a reviewer can act on.
// The three cases below are the ones that actually happen; anything
// else keeps gh's own words.
func describeFailure(err error, number int, repo string) error {
	text := err.Error()
	where := repo
	if where == "" {
		where = "this repository"
	}

	switch {
	case strings.Contains(text, "no pull requests found") ||
		strings.Contains(text, "Could not resolve to a PullRequest"):
		return errs.Wrap(err, "%s has no pull request #%d", where, number).
			WithHint("check the number, or that you are pointing at the right repository")

	case strings.Contains(text, "gh auth login") || strings.Contains(text, "authentication"):
		return errs.Wrap(err, "gh is not logged in").
			WithHint("run `gh auth login`")

	case strings.Contains(text, "not installed") || strings.Contains(text, "not on PATH"):
		return errs.Wrap(err, "pit reads pull requests through the GitHub CLI").
			WithHint("install it: https://cli.github.com")
	}

	return errs.Wrap(err, "cannot read pull request #%d from %s", number, where)
}
