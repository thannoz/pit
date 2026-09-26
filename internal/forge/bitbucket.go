package forge

import (
	"context"
	"encoding/base64"
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

// Bitbucket reads pull requests from Bitbucket Cloud. Its server, Data
// Center, speaks another API, which pit does not.
//
// Bitbucket keeps no ref for a pull request, so its commit is fetched
// from the branch it comes from, in the repository or in the fork it
// was opened from: PR.Source says which.
type Bitbucket struct {
	// Repo is workspace/slug.
	Repo string
	// Token is an access token, sent as a bearer token.
	Token string
	// Username and Password are an account's and its app password (or
	// API token), for Basic authentication, when there is no Token.
	Username, Password string
	// Client makes the requests; nil makes them with a timeout.
	Client *http.Client
	// API overrides https://api.bitbucket.org/2.0, for tests.
	API string
}

var (
	_ Forge     = Bitbucket{}
	_ Commenter = Bitbucket{}
)

// BitbucketFromEnv is a Bitbucket forge for repo, with the credentials
// of the environment.
func BitbucketFromEnv(repo string) Bitbucket {
	return Bitbucket{
		Repo:     repo,
		Token:    os.Getenv("BITBUCKET_TOKEN"),
		Username: os.Getenv("BITBUCKET_USERNAME"),
		Password: os.Getenv("BITBUCKET_APP_PASSWORD"),
	}
}

// BitbucketWith is BitbucketFromEnv, or, where the environment has no
// credentials, the stored ones: an access token, or "email:API token".
func BitbucketWith(repo, stored string) Bitbucket {
	b := BitbucketFromEnv(repo)
	if b.authorization() != "" || stored == "" {
		return b
	}
	return b.with(stored)
}

// with is b with credentials as pit keeps them: "email:API token" for
// Basic authentication, anything else a bearer's token.
func (b Bitbucket) with(stored string) Bitbucket {
	b.Token, b.Username, b.Password = "", "", ""
	if user, password, ok := strings.Cut(stored, ":"); ok {
		b.Username, b.Password = user, password
	} else {
		b.Token = stored
	}
	return b
}

// User is whose the credentials are. An access token of a repository or
// a workspace is nobody's, and is answered with "".
func (b Bitbucket) User(ctx context.Context) (string, error) {
	var u struct {
		Username string `json:"username"`
		Nickname string `json:"nickname"`
	}
	err := b.call(ctx, http.MethodGet, "/user", nil, &u)
	var s status
	if errors.As(err, &s) && s.code == http.StatusForbidden && b.Token != "" {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return orElse(u.Username, u.Nickname), nil
}

type bbRef struct {
	Branch struct {
		Name string `json:"name"`
	} `json:"branch"`
	Commit struct {
		Hash string `json:"hash"`
	} `json:"commit"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

type bbPR struct {
	ID     int    `json:"id"`
	Title  string `json:"title"`
	State  string `json:"state"`
	Draft  bool   `json:"draft"`
	Author struct {
		DisplayName string `json:"display_name"`
		Nickname    string `json:"nickname"`
	} `json:"author"`
	Source      bbRef `json:"source"`
	Destination bbRef `json:"destination"`
	Links       struct {
		HTML struct {
			Href string `json:"href"`
		} `json:"html"`
	} `json:"links"`
}

// PullRequest reads one pull request.
func (b Bitbucket) PullRequest(ctx context.Context, number int) (PR, error) {
	if number <= 0 {
		return PR{}, errs.New("%d is not a pull request number", number)
	}
	var raw bbPR
	if err := b.call(ctx, http.MethodGet, b.prPath(number), nil, &raw); err != nil {
		return PR{}, b.describe(err, number)
	}
	pr := PR{
		Number: raw.ID,
		Title:  raw.Title,
		Author: orElse(raw.Author.Nickname, raw.Author.DisplayName),
		Branch: raw.Source.Branch.Name,
		// Bitbucket gives commits shortened; the fetch finds the rest.
		HeadSHA:    raw.Source.Commit.Hash,
		BaseSHA:    raw.Destination.Commit.Hash,
		BaseBranch: raw.Destination.Branch.Name,
		State:      bbState(raw.State),
		Draft:      raw.Draft,
		URL:        raw.Links.HTML.Href,
		Source:     Source{Branch: raw.Source.Branch.Name},
	}
	switch fork := raw.Source.Repository.FullName; {
	case fork == "":
		pr.Source.Lost = true
	case !strings.EqualFold(fork, b.Repo):
		pr.Source.Repo = fork
	}
	return pr, nil
}

// MaxBitbucketComment is how long a comment pit posts on Bitbucket may
// be; Bitbucket does not say, and this is what it has been seen to take.
const MaxBitbucketComment = 32768

// Comment posts a comment on a pull request.
func (b Bitbucket) Comment(ctx context.Context, number int, body string) (string, error) {
	if number <= 0 {
		return "", errs.New("%d is not a pull request number", number)
	}
	if strings.TrimSpace(body) == "" {
		return "", errs.New("there is nothing to say in the comment")
	}
	if n := utf8.RuneCountInString(body); n > MaxBitbucketComment {
		return "", errs.New("the comment is %d characters long; pit posts at most %d on Bitbucket", n, MaxBitbucketComment)
	}
	if b.authorization() == "" {
		return "", errs.New("Bitbucket takes comments only from someone, and pit has no credentials for it").
			WithHint("run `pit auth login bitbucket.org`, or set BITBUCKET_TOKEN to an access token, or BITBUCKET_USERNAME and BITBUCKET_APP_PASSWORD")
	}
	payload, err := json.Marshal(map[string]any{"content": map[string]string{"raw": body}})
	if err != nil {
		return "", err
	}
	var c struct {
		ID    int `json:"id"`
		Links struct {
			HTML struct {
				Href string `json:"href"`
			} `json:"html"`
		} `json:"links"`
	}
	if err := b.call(ctx, http.MethodPost, b.prPath(number)+"/comments", payload, &c); err != nil {
		return "", b.describe(err, number)
	}
	if c.Links.HTML.Href != "" {
		return c.Links.HTML.Href, nil
	}
	return "https://bitbucket.org/" + b.Repo + "/pull-requests/" + strconv.Itoa(number) + "#comment-" + strconv.Itoa(c.ID), nil
}

func (b Bitbucket) prPath(number int) string {
	ws, slug, _ := strings.Cut(b.Repo, "/")
	return "/repositories/" + url.PathEscape(ws) + "/" + url.PathEscape(slug) + "/pullrequests/" + strconv.Itoa(number)
}

func (b Bitbucket) authorization() string {
	switch {
	case b.Token != "":
		return "Bearer " + b.Token
	case b.Username != "" && b.Password != "":
		return "Basic " + base64.StdEncoding.EncodeToString([]byte(b.Username+":"+b.Password))
	}
	return ""
}

func (b Bitbucket) call(ctx context.Context, method, path string, payload []byte, out any) error {
	base := b.API
	if base == "" {
		base = "https://api.bitbucket.org/2.0"
	}
	headers := map[string]string{}
	if a := b.authorization(); a != "" {
		headers["Authorization"] = a
	}
	return rest{Service: "Bitbucket", Host: "bitbucket.org", Client: b.Client}.call(ctx, method, base+path, headers, payload, out)
}

// describe turns what went wrong into what to do about it.
func (b Bitbucket) describe(err error, number int) error {
	var s status
	if errors.As(err, &s) {
		switch s.code {
		case http.StatusNotFound:
			hint := "check the number"
			if b.authorization() == "" {
				hint += "; a private repository answers only after `pit auth login bitbucket.org`, or with BITBUCKET_TOKEN, or BITBUCKET_USERNAME and BITBUCKET_APP_PASSWORD"
			}
			return errs.Wrap(err, "Bitbucket has no pull request #%d in %s", number, b.Repo).WithHint("%s", hint)
		case http.StatusUnauthorized:
			return errs.Wrap(err, "Bitbucket did not accept the credentials").
				WithHint("run `pit auth login bitbucket.org` again, or check BITBUCKET_TOKEN, or BITBUCKET_USERNAME and BITBUCKET_APP_PASSWORD")
		case http.StatusForbidden:
			return errs.Wrap(err, "the credentials may not do this on #%d", number).
				WithHint("they need access to %s, and to write pull requests for a comment", b.Repo)
		}
		return errs.Wrap(err, "Bitbucket could not answer about #%d", number)
	}
	return errs.Wrap(err, "cannot reach Bitbucket").
		WithHint("check the network; pit asks Bitbucket's API about pull requests")
}

// bbState maps Bitbucket's words onto pit's.
func bbState(raw string) State {
	switch strings.ToUpper(raw) {
	case "MERGED":
		return Merged
	case "DECLINED", "SUPERSEDED":
		return Closed
	default:
		return Open
	}
}

func isBitbucket(host string) bool { return strings.EqualFold(host, "bitbucket.org") }

func orElse(s, fallback string) string {
	if s != "" {
		return s
	}
	return fallback
}
