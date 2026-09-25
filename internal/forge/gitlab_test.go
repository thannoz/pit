package forge

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/thannoz/pit/internal/errs"
)

// realMergeRequest is the shape GitLab returns, cut from a live answer
// of gitlab.com so the parser is tested against the thing.
const realMergeRequest = `{
  "iid": 3966,
  "title": "Draft: feat(skills): add testing-df-corporate-proxy harness and skill",
  "state": "opened",
  "draft": true,
  "source_branch": "df-corporate-proxy-harness",
  "target_branch": "feat/dependency-firewall",
  "sha": "b510fd652aa4db28ee7cc42f94074dede2ad0638",
  "web_url": "https://gitlab.com/gitlab-org/cli/-/merge_requests/3966",
  "diff_refs": {
    "base_sha": "e8dee3669ef677f28aee5ed44f32ef54708f04ec",
    "head_sha": "b510fd652aa4db28ee7cc42f94074dede2ad0638",
    "start_sha": "e8dee3669ef677f28aee5ed44f32ef54708f04ec"
  },
  "author": {"username": "arpitgogia", "name": "Arpit Gogia"}
}`

type asked struct {
	method, path, token, body string
}

// gitlabAPI answers like GitLab's API, and notes what it was asked.
func gitlabAPI(t *testing.T, status int, answer string) (GitLab, *[]asked) {
	t.Helper()
	var calls []asked
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		calls = append(calls, asked{r.Method, r.URL.EscapedPath(), r.Header.Get("PRIVATE-TOKEN"), string(body)})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, answer)
	}))
	t.Cleanup(srv.Close)
	return GitLab{Host: "gitlab.com", Project: "gitlab-org/cli", API: srv.URL + "/api/v4"}, &calls
}

func TestMergeRequestParsesGitLabsShape(t *testing.T) {
	g, calls := gitlabAPI(t, http.StatusOK, realMergeRequest)
	pr, err := g.PullRequest(t.Context(), 3966)
	if err != nil {
		t.Fatal(err)
	}
	want := PR{
		Number: 3966, Title: "Draft: feat(skills): add testing-df-corporate-proxy harness and skill",
		Author: "arpitgogia", Branch: "df-corporate-proxy-harness", BaseBranch: "feat/dependency-firewall",
		HeadSHA: "b510fd652aa4db28ee7cc42f94074dede2ad0638", BaseSHA: "e8dee3669ef677f28aee5ed44f32ef54708f04ec",
		State: Open, Draft: true, URL: "https://gitlab.com/gitlab-org/cli/-/merge_requests/3966",
	}
	if pr != want {
		t.Errorf("pr =\n%+v\nwant\n%+v", pr, want)
	}
	// The project's path is one path segment, its slashes escaped, the
	// way the API wants it; no token was sent, since none was given.
	if len(*calls) != 1 || (*calls)[0].path != "/api/v4/projects/gitlab-org%2Fcli/merge_requests/3966" || (*calls)[0].token != "" {
		t.Errorf("asked %+v", *calls)
	}
}

func TestMergeRequestStates(t *testing.T) {
	for raw, want := range map[string]State{"opened": Open, "merged": Merged, "closed": Closed, "locked": Closed} {
		g, _ := gitlabAPI(t, http.StatusOK, `{"iid": 7, "state": "`+raw+`", "work_in_progress": true}`)
		pr, err := g.PullRequest(t.Context(), 7)
		if err != nil || pr.State != want || !pr.Draft || pr.BaseSHA != "" {
			t.Errorf("%s: %+v, %v", raw, pr, err)
		}
	}
}

func TestMergeRequestWithAToken(t *testing.T) {
	g, calls := gitlabAPI(t, http.StatusOK, realMergeRequest)
	g.Token = "glpat-secret"
	if _, err := g.PullRequest(t.Context(), 3966); err != nil {
		t.Fatal(err)
	}
	if (*calls)[0].token != "glpat-secret" {
		t.Errorf("asked %+v", *calls)
	}
}

func TestMergeRequestFailures(t *testing.T) {
	for _, tc := range []struct {
		status        int
		answer, token string
		want, hint    string
	}{
		{404, `{"message":"404 Not found"}`, "", "gitlab.com has no merge request !7 in gitlab-org/cli", "a private project answers only with GITLAB_TOKEN set"},
		{404, `{"message":"404 Not found"}`, "t", "gitlab.com has no merge request !7", "check the number"},
		{401, `{"message":"401 Unauthorized"}`, "t", "gitlab.com did not accept the token", "has not expired"},
		{403, `{"message":"403 Forbidden"}`, "t", "the token may not do this on !7", "api scope"},
		{500, `oops`, "", "gitlab.com could not answer about !7", ""},
	} {
		g, _ := gitlabAPI(t, tc.status, tc.answer)
		g.Token = tc.token
		_, err := g.PullRequest(t.Context(), 7)
		if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(errs.Hint(err), tc.hint) {
			t.Errorf("%d: err = %v, hint %q", tc.status, err, errs.Hint(err))
		}
		// With a token given, it is not the missing token.
		if tc.token != "" && strings.Contains(errs.Hint(err), "GITLAB_TOKEN set") {
			t.Errorf("%d: hint %q", tc.status, errs.Hint(err))
		}
	}
	gone := GitLab{Host: "gitlab.invalid", Project: "a/b", API: "http://127.0.0.1:1/api/v4"}
	if _, err := gone.PullRequest(t.Context(), 7); err == nil || !strings.Contains(err.Error(), "cannot reach gitlab.invalid") {
		t.Errorf("err = %v", err)
	}
}

func TestGitLabComment(t *testing.T) {
	g, calls := gitlabAPI(t, http.StatusCreated, `{"id": 2210987654, "body": "hello"}`)
	g.Token = "glpat-secret"
	url, err := g.Comment(t.Context(), 3966, "### Review notes\n")
	if err != nil || url != "https://gitlab.com/gitlab-org/cli/-/merge_requests/3966#note_2210987654" {
		t.Fatalf("url %q, %v", url, err)
	}
	c := (*calls)[0]
	var sent map[string]string
	if c.method != http.MethodPost || c.path != "/api/v4/projects/gitlab-org%2Fcli/merge_requests/3966/notes" || c.token != "glpat-secret" ||
		json.Unmarshal([]byte(c.body), &sent) != nil || sent["body"] != "### Review notes\n" {
		t.Errorf("asked %+v", c)
	}
}

// What cannot be posted is refused before GitLab is asked.
func TestGitLabCommentsThatAreNotSent(t *testing.T) {
	for _, tc := range []struct {
		token, body, want string
	}{
		{"", "hello", "GitLab takes comments only from someone"},
		{"t", "  ", "nothing to say"},
		{"t", strings.Repeat("x", MaxGitLabComment+1), "GitLab takes at most"},
	} {
		g, calls := gitlabAPI(t, http.StatusCreated, `{"id": 1}`)
		g.Token = tc.token
		if _, err := g.Comment(t.Context(), 7, tc.body); err == nil || !strings.Contains(err.Error(), tc.want) || len(*calls) != 0 {
			t.Errorf("%q: err = %v, asked %d", tc.want, err, len(*calls))
		}
	}
}

func TestTokenFromEnv(t *testing.T) {
	t.Setenv("GITLAB_TOKEN", "")
	t.Setenv("GITLAB_ACCESS_TOKEN", "from-access")
	if got := TokenFromEnv(); got != "from-access" {
		t.Errorf("got %q", got)
	}
	t.Setenv("GITLAB_TOKEN", "from-token")
	if got := TokenFromEnv(); got != "from-token" {
		t.Errorf("got %q", got)
	}
}
