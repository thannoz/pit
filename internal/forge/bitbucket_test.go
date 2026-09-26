package forge

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/thannoz/pit/internal/errs"
)

// realBitbucketPull is the shape Bitbucket Cloud returns, cut from a
// live answer for a pull request opened from a fork.
const realBitbucketPull = `{
  "id": 8219,
  "title": "added a new quote",
  "state": "OPEN",
  "draft": false,
  "author": {"display_name": "Rafael Aguirre", "nickname": "Rafael Aguirre", "type": "user"},
  "source": {
    "branch": {"name": "test-1"},
    "commit": {"hash": "c41496c16acc", "type": "commit"},
    "repository": {"full_name": "wspace1/mytutorials.git.bitbucket.org"}
  },
  "destination": {
    "branch": {"name": "master"},
    "commit": {"hash": "59d3c2c4c3e2", "type": "commit"},
    "repository": {"full_name": "tutorials/tutorials.git.bitbucket.org"}
  },
  "links": {"html": {"href": "https://bitbucket.org/tutorials/tutorials.git.bitbucket.org/pull-requests/8219"}}
}`

func bitbucketAPI(t *testing.T, status int, answer string) (Bitbucket, *[]asked) {
	t.Helper()
	var calls []asked
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		calls = append(calls, asked{r.Method, r.URL.EscapedPath(), r.Header.Get("Authorization"), string(body)})
		w.WriteHeader(status)
		_, _ = io.WriteString(w, answer)
	}))
	t.Cleanup(srv.Close)
	return Bitbucket{Repo: "tutorials/tutorials.git.bitbucket.org", API: srv.URL + "/2.0"}, &calls
}

func TestBitbucketReadsAPullRequestFromAFork(t *testing.T) {
	b, calls := bitbucketAPI(t, http.StatusOK, realBitbucketPull)
	pr, err := b.PullRequest(t.Context(), 8219)
	if err != nil {
		t.Fatal(err)
	}
	want := PR{
		Number: 8219, Title: "added a new quote", Author: "Rafael Aguirre", Branch: "test-1",
		HeadSHA: "c41496c16acc", BaseSHA: "59d3c2c4c3e2", BaseBranch: "master", State: Open,
		URL:    "https://bitbucket.org/tutorials/tutorials.git.bitbucket.org/pull-requests/8219",
		Source: Source{Repo: "wspace1/mytutorials.git.bitbucket.org", Branch: "test-1"},
	}
	if pr != want {
		t.Errorf("pr =\n%+v\nwant\n%+v", pr, want)
	}
	if len(*calls) != 1 || (*calls)[0].path != "/2.0/repositories/tutorials/tutorials.git.bitbucket.org/pullrequests/8219" || (*calls)[0].token != "" {
		t.Errorf("asked %+v", *calls)
	}
}

func TestBitbucketSources(t *testing.T) {
	for body, want := range map[string]Source{
		// The repository itself, however it is written.
		`{"id": 7, "source": {"branch": {"name": "fix"}, "repository": {"full_name": "Tutorials/Tutorials.git.bitbucket.org"}}}`: {Branch: "fix"},
		// A fork that is gone.
		`{"id": 7, "source": {"branch": {"name": "fix"}, "repository": null}}`: {Branch: "fix", Lost: true},
	} {
		b, _ := bitbucketAPI(t, http.StatusOK, body)
		pr, err := b.PullRequest(t.Context(), 7)
		if err != nil || pr.Source != want {
			t.Errorf("%s: %+v, %v", body, pr.Source, err)
		}
	}
	for raw, want := range map[string]State{"OPEN": Open, "MERGED": Merged, "DECLINED": Closed, "SUPERSEDED": Closed} {
		b, _ := bitbucketAPI(t, http.StatusOK, `{"id": 7, "state": "`+raw+`", "draft": true, "author": {"display_name": "Lisa"}}`)
		pr, _ := b.PullRequest(t.Context(), 7)
		if pr.State != want || !pr.Draft || pr.Author != "Lisa" {
			t.Errorf("%s: %+v", raw, pr)
		}
	}
}

func TestBitbucketSaysWhatWentWrong(t *testing.T) {
	b, _ := bitbucketAPI(t, http.StatusNotFound, `{"type": "error", "error": {"message": "Repository not found"}}`)
	_, err := b.PullRequest(t.Context(), 99)
	if err == nil || !strings.Contains(err.Error(), "Bitbucket has no pull request #99") ||
		!strings.Contains(err.Error(), "Bitbucket answered 404: Repository not found") || !strings.Contains(errs.Hint(err), "BITBUCKET_TOKEN") {
		t.Errorf("err = %v, hint %q", err, errs.Hint(err))
	}
	for status, want := range map[int]string{401: "did not accept the credentials", 403: "may not do this on #7", 502: "could not answer about #7"} {
		b, _ := bitbucketAPI(t, status, `{}`)
		if _, err := b.PullRequest(t.Context(), 7); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%d: err = %v", status, err)
		}
	}
	b.API = "http://127.0.0.1:1/2.0"
	if _, err := b.PullRequest(t.Context(), 7); err == nil || !strings.Contains(err.Error(), "cannot reach Bitbucket") {
		t.Errorf("err = %v", err)
	}
	if _, err := b.PullRequest(t.Context(), -1); err == nil {
		t.Error("read #-1")
	}
}

func TestBitbucketCredentials(t *testing.T) {
	t.Setenv("BITBUCKET_TOKEN", "")
	t.Setenv("BITBUCKET_USERNAME", "lisa")
	t.Setenv("BITBUCKET_APP_PASSWORD", "app-pass")
	b := BitbucketFromEnv("acme/shop")
	if got := b.authorization(); got != "Basic "+base64.StdEncoding.EncodeToString([]byte("lisa:app-pass")) {
		t.Errorf("basic = %q", got)
	}
	t.Setenv("BITBUCKET_TOKEN", "tok")
	if got := BitbucketFromEnv("acme/shop").authorization(); got != "Bearer tok" {
		t.Errorf("bearer = %q", got)
	}
	if got := (Bitbucket{Username: "lisa"}).authorization(); got != "" {
		t.Errorf("a user without a password = %q", got)
	}
}

func TestBitbucketComments(t *testing.T) {
	b, calls := bitbucketAPI(t, http.StatusCreated, `{"id": 5, "links": {"html": {"href": "https://bitbucket.org/acme/shop/pull-requests/7#comment-5"}}}`)
	if _, err := b.Comment(t.Context(), 7, "hi"); err == nil || !strings.Contains(errs.Hint(err), "BITBUCKET_TOKEN") {
		t.Errorf("without credentials: %v", err)
	}
	b.Token = "tok"
	url, err := b.Comment(t.Context(), 7, "### Review notes")
	if err != nil || url != "https://bitbucket.org/acme/shop/pull-requests/7#comment-5" {
		t.Fatalf("%q, %v", url, err)
	}
	c := (*calls)[len(*calls)-1]
	var body struct {
		Content struct {
			Raw string `json:"raw"`
		} `json:"content"`
	}
	if json.Unmarshal([]byte(c.body), &body) != nil || body.Content.Raw != "### Review notes" || c.token != "Bearer tok" ||
		c.path != "/2.0/repositories/tutorials/tutorials.git.bitbucket.org/pullrequests/7/comments" {
		t.Errorf("asked %+v", c)
	}
	b2, _ := bitbucketAPI(t, http.StatusCreated, `{"id": 6}`)
	b2.Token = "tok"
	if url, _ := b2.Comment(t.Context(), 7, "hi"); url != "https://bitbucket.org/tutorials/tutorials.git.bitbucket.org/pull-requests/7#comment-6" {
		t.Errorf("url = %q", url)
	}
	for _, body := range []string{"", strings.Repeat("x", MaxBitbucketComment+1)} {
		if _, err := b.Comment(t.Context(), 7, body); err == nil {
			t.Errorf("posted %d characters", len(body))
		}
	}
	if _, err := b.Comment(t.Context(), 0, "hi"); err == nil {
		t.Error("commented on #0")
	}
}
