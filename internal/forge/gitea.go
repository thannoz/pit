package forge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/thannoz/pit/internal/errs"
)

// Gitea reads pull requests through the API that Gitea and Forgejo --
// Forgejo began as Gitea, and Codeberg runs it -- still share.
type Gitea struct {
	// Host is the instance: codeberg.org, gitea.com, git.example.com.
	Host string
	// Repo is owner/name.
	Repo string
	// Token is an access token, for private repositories and for
	// commenting; empty asks as nobody.
	Token string
	// Client makes the requests; nil makes them with a timeout.
	Client *http.Client
	// API overrides https://<Host>/api/v1, for tests.
	API string
}

var (
	_ Forge     = Gitea{}
	_ Commenter = Gitea{}
)

// GiteaTokenFromEnv is the token for a Gitea or Forgejo instance.
func GiteaTokenFromEnv() string {
	for _, name := range []string{"FORGEJO_TOKEN", "GITEA_TOKEN"} {
		if t := os.Getenv(name); t != "" {
			return t
		}
	}
	return ""
}

// giteaPR mirrors the API's pull request.
type giteaPR struct {
	Number    int    `json:"number"`
	Title     string `json:"title"`
	State     string `json:"state"`
	Merged    bool   `json:"merged"`
	Draft     bool   `json:"draft"`
	HTMLURL   string `json:"html_url"`
	MergeBase string `json:"merge_base"`
	User      struct {
		Login string `json:"login"`
	} `json:"user"`
	Head struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

// PullRequest reads one pull request.
func (g Gitea) PullRequest(ctx context.Context, number int) (PR, error) {
	if number <= 0 {
		return PR{}, errs.New("%d is not a pull request number", number)
	}
	var raw giteaPR
	if err := g.call(ctx, http.MethodGet, "/repos/"+g.repoPath()+"/pulls/"+strconv.Itoa(number), nil, &raw); err != nil {
		return PR{}, g.describe(err, number)
	}
	pr := PR{
		Number:     raw.Number,
		Title:      raw.Title,
		Author:     raw.User.Login,
		Branch:     raw.Head.Ref,
		HeadSHA:    raw.Head.SHA,
		BaseSHA:    raw.MergeBase,
		BaseBranch: raw.Base.Ref,
		State:      Open,
		// Older instances have no draft; their authors mark one in
		// its title, which Gitea itself reads as work in progress.
		Draft: raw.Draft || workInProgress(raw.Title),
		URL:   raw.HTMLURL,
	}
	switch {
	case raw.Merged:
		pr.State = Merged
	case raw.State == "closed":
		pr.State = Closed
	}
	return pr, nil
}

// workInProgress reads the prefixes Gitea's default settings treat as
// a draft.
func workInProgress(title string) bool {
	t := strings.ToUpper(strings.TrimSpace(title))
	return strings.HasPrefix(t, "WIP:") || strings.HasPrefix(t, "[WIP]")
}

// Comment posts a comment on a pull request, which the API keeps with
// the issue of the same number.
func (g Gitea) Comment(ctx context.Context, number int, body string) (string, error) {
	if number <= 0 {
		return "", errs.New("%d is not a pull request number", number)
	}
	if strings.TrimSpace(body) == "" {
		return "", errs.New("there is nothing to say in the comment")
	}
	if n := utf8.RuneCountInString(body); n > MaxComment {
		return "", errs.New("the comment is %d characters long; pit posts at most %d", n, MaxComment)
	}
	if g.Token == "" {
		return "", errs.New("%s takes comments only from someone, and pit has no token for it", g.Host).
			WithHint("run `pit auth login %s`, or set FORGEJO_TOKEN (or GITEA_TOKEN) to an access token that may write issues", g.Host)
	}
	payload, err := json.Marshal(map[string]string{"body": body})
	if err != nil {
		return "", err
	}
	var c struct {
		ID      int    `json:"id"`
		HTMLURL string `json:"html_url"`
	}
	if err := g.call(ctx, http.MethodPost, "/repos/"+g.repoPath()+"/issues/"+strconv.Itoa(number)+"/comments", payload, &c); err != nil {
		return "", g.describe(err, number)
	}
	if c.HTMLURL != "" {
		return c.HTMLURL, nil
	}
	return fmt.Sprintf("https://%s/%s/pulls/%d#issuecomment-%d", g.Host, g.Repo, number, c.ID), nil
}

func (g Gitea) repoPath() string {
	owner, name, _ := strings.Cut(g.Repo, "/")
	return url.PathEscape(owner) + "/" + url.PathEscape(name)
}

func (g Gitea) base() string {
	if g.API != "" {
		return g.API
	}
	return "https://" + g.Host + "/api/v1"
}

func (g Gitea) call(ctx context.Context, method, path string, payload []byte, out any) error {
	headers := map[string]string{}
	if g.Token != "" {
		headers["Authorization"] = "token " + g.Token
	}
	return rest{Service: g.Host, Host: g.Host, Client: g.Client}.call(ctx, method, g.base()+path, headers, payload, out)
}

// User is whose the token is.
func (g Gitea) User(ctx context.Context) (string, error) {
	var u struct {
		Login string `json:"login"`
	}
	if err := g.call(ctx, http.MethodGet, "/user", nil, &u); err != nil {
		return "", err
	}
	return u.Login, nil
}

// IsGitea asks a host whether it runs Gitea or Forgejo: both answer
// /api/v1/version, which nothing else does in that shape.
func (g Gitea) IsGitea(ctx context.Context) bool {
	var v struct {
		Version string `json:"version"`
	}
	err := g.call(ctx, http.MethodGet, "/version", nil, &v)
	return err == nil && v.Version != ""
}

// describe turns what went wrong into what to do about it.
func (g Gitea) describe(err error, number int) error {
	var s status
	if errors.As(err, &s) {
		switch s.code {
		case http.StatusNotFound:
			hint := "check the number"
			if g.Token == "" {
				hint += "; a private repository answers only after `pit auth login " + g.Host + "`, or with FORGEJO_TOKEN or GITEA_TOKEN set"
			}
			return errs.Wrap(err, "%s has no pull request #%d in %s", g.Host, number, g.Repo).WithHint("%s", hint)
		case http.StatusUnauthorized:
			return errs.Wrap(err, "%s did not accept the token", g.Host).
				WithHint("the token has to be one of %s's, and not expired", g.Host)
		case http.StatusForbidden:
			return errs.Wrap(err, "the token may not do this on #%d", number).
				WithHint("it needs access to %s, and to write issues for a comment", g.Repo)
		}
		return errs.Wrap(err, "%s could not answer about #%d", g.Host, number)
	}
	return errs.Wrap(err, "cannot reach %s", g.Host).
		WithHint("check the network; pit asks %s's API about pull requests", g.Host)
}

// isGitea recognises the instances whose name says what they run.
func isGitea(host string) bool {
	h := strings.ToLower(host)
	return h == "gitea.com" || strings.HasPrefix(h, "codeberg.") ||
		strings.HasPrefix(h, "gitea.") || strings.HasPrefix(h, "forgejo.")
}
