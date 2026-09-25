package forge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/thannoz/pit/internal/errs"
)

// GitLab reads merge requests through GitLab's API, of gitlab.com or of
// a company's own instance.
//
// The API rather than glab: GitLab answers a public project's questions
// without an account, so pit works on one with nothing installed; a
// private project needs a token, which is what glab would need too.
type GitLab struct {
	// Host is the instance: gitlab.com, gitlab.example.com.
	Host string
	// Project is the project's path, groups and all: group/sub/name.
	Project string
	// Token is a personal access token, for private projects and for
	// commenting; empty asks as nobody.
	Token string
	// Client makes the requests; nil makes them with a timeout.
	Client *http.Client
	// API overrides https://<Host>/api/v4, for tests.
	API string
}

// TokenFromEnv is the token GitLab's own tools read.
func TokenFromEnv() string {
	for _, name := range []string{"GITLAB_TOKEN", "GITLAB_ACCESS_TOKEN"} {
		if t := os.Getenv(name); t != "" {
			return t
		}
	}
	return ""
}

var (
	_ Forge     = GitLab{}
	_ Commenter = GitLab{}
)

// glMR mirrors GitLab's shape of a merge request.
type glMR struct {
	IID          int    `json:"iid"`
	Title        string `json:"title"`
	State        string `json:"state"`
	Draft        bool   `json:"draft"`
	WIP          bool   `json:"work_in_progress"`
	SourceBranch string `json:"source_branch"`
	TargetBranch string `json:"target_branch"`
	SHA          string `json:"sha"`
	WebURL       string `json:"web_url"`
	DiffRefs     *struct {
		BaseSHA string `json:"base_sha"`
	} `json:"diff_refs"`
	Author struct {
		Username string `json:"username"`
	} `json:"author"`
}

// PullRequest reads one merge request.
func (g GitLab) PullRequest(ctx context.Context, number int) (PR, error) {
	if number <= 0 {
		return PR{}, errs.New("%d is not a merge request number", number)
	}
	var raw glMR
	if err := g.call(ctx, http.MethodGet, g.mrPath(number), nil, &raw); err != nil {
		return PR{}, g.describe(err, number)
	}
	pr := PR{
		Number:     raw.IID,
		Title:      raw.Title,
		Author:     raw.Author.Username,
		Branch:     raw.SourceBranch,
		HeadSHA:    raw.SHA,
		BaseBranch: raw.TargetBranch,
		State:      glState(raw.State),
		Draft:      raw.Draft || raw.WIP,
		URL:        raw.WebURL,
	}
	if raw.DiffRefs != nil {
		pr.BaseSHA = raw.DiffRefs.BaseSHA
	}
	return pr, nil
}

// Comment posts a note on a merge request. GitLab takes one only from
// someone, so it needs the token.
func (g GitLab) Comment(ctx context.Context, number int, body string) (string, error) {
	if number <= 0 {
		return "", errs.New("%d is not a merge request number", number)
	}
	if strings.TrimSpace(body) == "" {
		return "", errs.New("there is nothing to say in the comment")
	}
	if n := utf8.RuneCountInString(body); n > MaxGitLabComment {
		return "", errs.New("the comment is %d characters long; GitLab takes at most %d", n, MaxGitLabComment)
	}
	if g.Token == "" {
		return "", errs.New("GitLab takes comments only from someone, and pit has no token for %s", g.Host).
			WithHint("set GITLAB_TOKEN to a personal access token with the api scope")
	}
	payload, err := json.Marshal(map[string]string{"body": body})
	if err != nil {
		return "", err
	}
	var note struct {
		ID int `json:"id"`
	}
	if err := g.call(ctx, http.MethodPost, g.mrPath(number)+"/notes", payload, &note); err != nil {
		return "", g.describe(err, number)
	}
	return fmt.Sprintf("https://%s/%s/-/merge_requests/%d#note_%d", g.Host, g.Project, number, note.ID), nil
}

// MaxGitLabComment is the longest note GitLab takes, in characters.
const MaxGitLabComment = 1000000

func (g GitLab) mrPath(number int) string {
	return "/projects/" + url.PathEscape(g.Project) + "/merge_requests/" + strconv.Itoa(number)
}

// status is an answer GitLab gave that was not a success.
type status struct {
	code    int
	message string
}

func (s status) Error() string {
	if s.message == "" {
		return fmt.Sprintf("GitLab answered %d", s.code)
	}
	return fmt.Sprintf("GitLab answered %d: %s", s.code, s.message)
}

// call asks the API and reads its answer into out.
func (g GitLab) call(ctx context.Context, method, path string, payload []byte, out any) error {
	base := g.API
	if base == "" {
		base = "https://" + g.Host + "/api/v4"
	}
	var body io.Reader
	if payload != nil {
		body = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, body)
	if err != nil {
		return err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if g.Token != "" {
		req.Header.Set("PRIVATE-TOKEN", g.Token)
	}
	client := g.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		var m struct {
			Message any `json:"message"`
			Error   any `json:"error"`
		}
		_ = json.Unmarshal(data, &m)
		msg := fmt.Sprint(firstOf(m.Message, m.Error))
		return status{code: resp.StatusCode, message: msg}
	}
	if err := json.Unmarshal(data, out); err != nil {
		return errs.Wrap(err, "cannot read what %s answered", g.Host)
	}
	return nil
}

func firstOf(values ...any) any {
	for _, v := range values {
		if v != nil {
			return v
		}
	}
	return ""
}

// describe turns what went wrong into what to do about it.
func (g GitLab) describe(err error, number int) error {
	var s status
	if errors.As(err, &s) {
		switch s.code {
		case http.StatusNotFound:
			hint := "check the number"
			if g.Token == "" {
				hint += "; a private project answers only with GITLAB_TOKEN set"
			}
			return errs.Wrap(err, "%s has no merge request !%d in %s", g.Host, number, g.Project).WithHint("%s", hint)
		case http.StatusUnauthorized:
			return errs.Wrap(err, "%s did not accept the token", g.Host).
				WithHint("GITLAB_TOKEN has to be a personal access token of %s that has not expired", g.Host)
		case http.StatusForbidden:
			return errs.Wrap(err, "the token may not do this on !%d", number).
				WithHint("it needs the api scope, and access to %s", g.Project)
		}
		return errs.Wrap(err, "%s could not answer about !%d", g.Host, number)
	}
	return errs.Wrap(err, "cannot reach %s", g.Host).
		WithHint("check the network; pit asks %s's API about merge requests", g.Host)
}

// glState maps GitLab's words onto pit's.
func glState(raw string) State {
	switch raw {
	case "merged":
		return Merged
	case "closed", "locked":
		return Closed
	default:
		return Open
	}
}

// isGitLab recognises gitlab.com and the naming self-hosted instances
// usually take.
func isGitLab(host string) bool {
	h := strings.ToLower(host)
	return h == "gitlab.com" || strings.HasPrefix(h, "gitlab.")
}
