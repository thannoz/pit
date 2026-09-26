package forge

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/thannoz/pit/internal/errs"
)

// GitHubAPI reads pull requests through GitHub's REST API, with a token
// of pit's own login or of the environment. Without one, pit asks gh,
// which has a login of its own: GitHub.
type GitHubAPI struct {
	// Host is github.com, or a GitHub Enterprise server.
	Host string
	// Repo is owner/name.
	Repo string
	// Token is what the API is asked with.
	Token string
	// Client makes the requests; nil makes them with a timeout.
	Client *http.Client
	// API overrides the API's address, for tests.
	API string
}

var (
	_ Forge     = GitHubAPI{}
	_ Commenter = GitHubAPI{}
)

// GitHubTokenFromEnv is the token for a GitHub host that gh itself
// would read: GH_TOKEN or GITHUB_TOKEN for github.com, the ENTERPRISE
// ones for a server of a company's own.
func GitHubTokenFromEnv(host string) string {
	names := []string{"GH_TOKEN", "GITHUB_TOKEN"}
	if !strings.EqualFold(host, "github.com") {
		names = []string{"GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN"}
	}
	for _, name := range names {
		if t := os.Getenv(name); t != "" {
			return t
		}
	}
	return ""
}

// ghRESTPR mirrors the REST API's pull request.
type ghRESTPR struct {
	Number  int    `json:"number"`
	Title   string `json:"title"`
	State   string `json:"state"`
	Merged  bool   `json:"merged"`
	Draft   bool   `json:"draft"`
	HTMLURL string `json:"html_url"`
	User    struct {
		Login string `json:"login"`
	} `json:"user"`
	Head struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"base"`
}

// PullRequest reads one pull request.
func (g GitHubAPI) PullRequest(ctx context.Context, number int) (PR, error) {
	if number <= 0 {
		return PR{}, errs.New("%d is not a pull request number", number)
	}
	var raw ghRESTPR
	if err := g.call(ctx, http.MethodGet, "/repos/"+g.repoPath()+"/pulls/"+strconv.Itoa(number), nil, &raw); err != nil {
		return PR{}, g.describe(err, number)
	}
	pr := PR{
		Number:     raw.Number,
		Title:      raw.Title,
		Author:     raw.User.Login,
		Branch:     raw.Head.Ref,
		HeadSHA:    raw.Head.SHA,
		BaseSHA:    raw.Base.SHA,
		BaseBranch: raw.Base.Ref,
		State:      Open,
		Draft:      raw.Draft,
		URL:        raw.HTMLURL,
	}
	switch {
	case raw.Merged:
		pr.State = Merged
	case raw.State == "closed":
		pr.State = Closed
	}
	return pr, nil
}

// Comment posts a comment on a pull request, which the API keeps with
// the issue of the same number.
func (g GitHubAPI) Comment(ctx context.Context, number int, body string) (string, error) {
	if number <= 0 {
		return "", errs.New("%d is not a pull request number", number)
	}
	if strings.TrimSpace(body) == "" {
		return "", errs.New("there is nothing to say in the comment")
	}
	if n := utf8.RuneCountInString(body); n > MaxComment {
		return "", errs.New("the comment is %d characters long; GitHub takes at most %d", n, MaxComment)
	}
	payload, err := json.Marshal(map[string]string{"body": body})
	if err != nil {
		return "", err
	}
	var c struct {
		HTMLURL string `json:"html_url"`
	}
	if err := g.call(ctx, http.MethodPost, "/repos/"+g.repoPath()+"/issues/"+strconv.Itoa(number)+"/comments", payload, &c); err != nil {
		var s status
		if errors.As(err, &s) && s.code == http.StatusForbidden && strings.Contains(strings.ToLower(s.message), "locked") {
			return "", errs.Wrap(err, "the conversation on #%d is locked", number).
				WithHint("only people with write access can comment on it now")
		}
		return "", g.describe(err, number)
	}
	return c.HTMLURL, nil
}

// User is whose the token is.
func (g GitHubAPI) User(ctx context.Context) (string, error) {
	var u struct {
		Login string `json:"login"`
	}
	if err := g.call(ctx, http.MethodGet, "/user", nil, &u); err != nil {
		return "", err
	}
	return u.Login, nil
}

func (g GitHubAPI) repoPath() string {
	owner, name, _ := strings.Cut(g.Repo, "/")
	return url.PathEscape(owner) + "/" + url.PathEscape(name)
}

// base is the API: api.github.com for github.com, /api/v3 on a server
// of a company's own.
func (g GitHubAPI) base() string {
	switch {
	case g.API != "":
		return g.API
	case strings.EqualFold(g.Host, "github.com"):
		return "https://api.github.com"
	}
	return "https://" + g.Host + "/api/v3"
}

func (g GitHubAPI) call(ctx context.Context, method, path string, payload []byte, out any) error {
	headers := map[string]string{
		"Accept":               "application/vnd.github+json",
		"X-GitHub-Api-Version": "2022-11-28",
	}
	if g.Token != "" {
		headers["Authorization"] = "Bearer " + g.Token
	}
	return rest{Service: "GitHub", Host: g.Host, Client: g.Client}.call(ctx, method, g.base()+path, headers, payload, out)
}

// describe turns what went wrong into what to do about it.
func (g GitHubAPI) describe(err error, number int) error {
	var s status
	if errors.As(err, &s) {
		switch s.code {
		case http.StatusNotFound:
			return errs.Wrap(err, "%s has no pull request #%d", g.Repo, number).
				WithHint("check the number; a private repository answers only to a login that may see it (`pit auth status` says whose pit uses)")
		case http.StatusUnauthorized:
			return errs.Wrap(err, "%s did not accept pit's token", g.Host).
				WithHint("run `pit auth login %s` again, or check GH_TOKEN if it is set", g.Host)
		case http.StatusForbidden:
			hint := "the token needs the repo scope, and access to " + g.Repo
			if strings.Contains(strings.ToLower(s.message), "rate limit") {
				hint = "wait a while; GitHub limits how often one token may ask"
			}
			return errs.Wrap(err, "the token may not do this on #%d", number).WithHint("%s", hint)
		}
		return errs.Wrap(err, "%s could not answer about #%d", g.Host, number)
	}
	return errs.Wrap(err, "cannot reach %s", g.Host).
		WithHint("check the network; pit asks %s's API about pull requests", g.Host)
}
