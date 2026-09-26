package forge

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/thannoz/pit/internal/errs"
)

// realGitHubPull is the REST API's pull request, cut from a live answer
// of api.github.com.
const realGitHubPull = `{
  "number": 14517,
  "title": "Mention gh CLI in the README intro",
  "state": "closed",
  "draft": false,
  "merged": true,
  "html_url": "https://github.com/cli/cli/pull/14517",
  "user": {"login": "BagToad"},
  "head": {"label": "cli:bagtoad/readme-seo", "ref": "bagtoad/readme-seo", "sha": "362a5eb03dcc16af5e313ab8ae5a0cfdc4569e46", "repo": {"full_name": "cli/cli"}},
  "base": {"label": "cli:trunk", "ref": "trunk", "sha": "b6770c8bc54c72e74e785c307850446b8e10be9d", "repo": {"full_name": "cli/cli"}}
}`

type githubServer struct {
	mu      sync.Mutex
	calls   []asked
	headers []http.Header
}

// githubAPI answers like GitHub's REST API, by path, with status.
func githubAPI(t *testing.T, status int, answers map[string]string) (GitHubAPI, *githubServer) {
	t.Helper()
	g := &githubServer{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		g.mu.Lock()
		g.calls = append(g.calls, asked{r.Method, r.URL.EscapedPath(), r.Header.Get("Authorization"), string(body)})
		g.headers = append(g.headers, r.Header.Clone())
		g.mu.Unlock()
		answer, ok := answers[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"message": "Not Found", "status": "404"}`)
			return
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, answer)
	}))
	t.Cleanup(srv.Close)
	return GitHubAPI{Host: "github.com", Repo: "cli/cli", Token: "gho_secret", API: srv.URL}, g
}

func TestGitHubAPIReadsAPullRequest(t *testing.T) {
	g, s := githubAPI(t, http.StatusOK, map[string]string{"/repos/cli/cli/pulls/14517": realGitHubPull})
	pr, err := g.PullRequest(t.Context(), 14517)
	if err != nil {
		t.Fatal(err)
	}
	want := PR{
		Number: 14517, Title: "Mention gh CLI in the README intro", Author: "BagToad",
		Branch: "bagtoad/readme-seo", HeadSHA: "362a5eb03dcc16af5e313ab8ae5a0cfdc4569e46",
		BaseSHA: "b6770c8bc54c72e74e785c307850446b8e10be9d", BaseBranch: "trunk",
		State: Merged, URL: "https://github.com/cli/cli/pull/14517",
	}
	if pr != want {
		t.Errorf("pr =\n%+v\nwant\n%+v", pr, want)
	}
	h := s.headers[0]
	if s.calls[0].token != "Bearer gho_secret" || h.Get("Accept") != "application/vnd.github+json" || h.Get("X-GitHub-Api-Version") == "" {
		t.Errorf("asked %+v with %v", s.calls[0], h)
	}
}

func TestGitHubAPIStates(t *testing.T) {
	for _, tc := range []struct {
		edit  func(string) string
		state State
		draft bool
	}{
		{func(s string) string { return s }, Merged, false},
		{func(s string) string { return strings.Replace(s, `"merged": true`, `"merged": false`, 1) }, Closed, false},
		{func(s string) string {
			s = strings.Replace(s, `"merged": true`, `"merged": false`, 1)
			s = strings.Replace(s, `"draft": false`, `"draft": true`, 1)
			return strings.Replace(s, `"state": "closed"`, `"state": "open"`, 1)
		}, Open, true},
	} {
		g, _ := githubAPI(t, http.StatusOK, map[string]string{"/repos/cli/cli/pulls/14517": tc.edit(realGitHubPull)})
		pr, err := g.PullRequest(t.Context(), 14517)
		if err != nil || pr.State != tc.state || pr.Draft != tc.draft {
			t.Errorf("pr = %+v, %v; want %s, draft %v", pr, err, tc.state, tc.draft)
		}
	}
}

func TestGitHubAPIFailures(t *testing.T) {
	for _, tc := range []struct {
		status     int
		answer     string
		want, hint string
	}{
		{404, `{"message":"Not Found"}`, "cli/cli has no pull request #7", "private repository"},
		{401, `{"message":"Bad credentials"}`, "github.com did not accept pit's token", "pit auth login github.com"},
		{403, `{"message":"Resource not accessible by integration"}`, "the token may not do this on #7", "repo scope"},
		{403, `{"message":"API rate limit exceeded for user ID 1."}`, "the token may not do this on #7", "wait a while"},
		{502, `oops`, "github.com could not answer about #7", ""},
	} {
		g, _ := githubAPI(t, tc.status, map[string]string{"/repos/cli/cli/pulls/7": tc.answer})
		_, err := g.PullRequest(t.Context(), 7)
		if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(errs.Hint(err), tc.hint) {
			t.Errorf("%d: err = %v, hint %q", tc.status, err, errs.Hint(err))
		}
	}
	gone := GitHubAPI{Host: "github.invalid", Repo: "a/b", API: "http://127.0.0.1:1"}
	if _, err := gone.PullRequest(t.Context(), 7); err == nil || !strings.Contains(err.Error(), "cannot reach github.invalid") {
		t.Errorf("err = %v", err)
	}
	if _, err := gone.PullRequest(t.Context(), 0); err == nil || !strings.Contains(err.Error(), "not a pull request number") {
		t.Errorf("err = %v", err)
	}
}

func TestGitHubAPIComment(t *testing.T) {
	g, s := githubAPI(t, http.StatusCreated, map[string]string{
		"/repos/cli/cli/issues/14517/comments": `{"id": 3339876543, "html_url": "https://github.com/cli/cli/pull/14517#issuecomment-3339876543"}`,
	})
	url, err := g.Comment(t.Context(), 14517, "### Review notes\n")
	if err != nil || url != "https://github.com/cli/cli/pull/14517#issuecomment-3339876543" {
		t.Fatalf("url %q, %v", url, err)
	}
	var sent map[string]string
	c := s.calls[0]
	if c.method != http.MethodPost || c.token != "Bearer gho_secret" || json.Unmarshal([]byte(c.body), &sent) != nil || sent["body"] != "### Review notes\n" {
		t.Errorf("asked %+v", c)
	}

	// A locked conversation is said to be one.
	g, _ = githubAPI(t, http.StatusForbidden, map[string]string{
		"/repos/cli/cli/issues/7/comments": `{"message": "Unable to create comment because issue is locked."}`,
	})
	if _, err := g.Comment(t.Context(), 7, "hi"); err == nil || !strings.Contains(err.Error(), "the conversation on #7 is locked") ||
		!strings.Contains(errs.Hint(err), "write access") {
		t.Errorf("err = %v", err)
	}
	// What cannot be posted is not sent.
	g, s = githubAPI(t, http.StatusCreated, nil)
	for _, body := range []string{"  ", strings.Repeat("x", MaxComment+1)} {
		if _, err := g.Comment(t.Context(), 7, body); err == nil {
			t.Errorf("posted %d characters", len(body))
		}
	}
	if len(s.calls) != 0 {
		t.Errorf("asked %+v", s.calls)
	}
}

func TestGitHubAPIUserAndAddress(t *testing.T) {
	g, s := githubAPI(t, http.StatusOK, map[string]string{"/user": `{"login": "octocat", "id": 1}`})
	if user, err := g.User(t.Context()); err != nil || user != "octocat" {
		t.Errorf("user %q, %v", user, err)
	}
	if s.calls[0].token != "Bearer gho_secret" {
		t.Errorf("asked %+v", s.calls)
	}
	for host, want := range map[string]string{
		"github.com":         "https://api.github.com",
		"GitHub.com":         "https://api.github.com",
		"github.example.com": "https://github.example.com/api/v3",
	} {
		if got := (GitHubAPI{Host: host}).base(); got != want {
			t.Errorf("%s: %s, want %s", host, got, want)
		}
	}
}

func TestGitHubTokenFromEnv(t *testing.T) {
	for _, name := range []string{"GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN"} {
		t.Setenv(name, "")
	}
	t.Setenv("GITHUB_TOKEN", "b")
	t.Setenv("GITHUB_ENTERPRISE_TOKEN", "e")
	if got := GitHubTokenFromEnv("github.com"); got != "b" {
		t.Errorf("github.com: %q", got)
	}
	t.Setenv("GH_TOKEN", "a")
	if got := GitHubTokenFromEnv("github.com"); got != "a" {
		t.Errorf("github.com: %q", got)
	}
	// A company's server has tokens of its own.
	if got := GitHubTokenFromEnv("github.example.com"); got != "e" {
		t.Errorf("enterprise: %q", got)
	}
}
