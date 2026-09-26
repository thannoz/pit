package forge

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/thannoz/pit/internal/errs"
)

// realGiteaPull is the shape Forgejo returns, cut from a live answer of
// codeberg.org; gitea.com answers the same.
const realGiteaPull = `{
  "id": 7501,
  "number": 14553,
  "title": "fix: prevent off by one by excluding size == 1024",
  "state": "open",
  "draft": false,
  "merged": false,
  "html_url": "https://codeberg.org/forgejo/forgejo/pulls/14553",
  "merge_base": "a74759dc39987e02ee6f85260e47c0f29b1a325e",
  "user": {"id": 1, "login": "kbruen"},
  "head": {"label": "forgejo", "ref": "forgejo", "sha": "755bff0771ffd7785c0f5ad2f5fa6bae8e8c57f8", "repo": {"full_name": "kbruen/forgejo"}},
  "base": {"label": "forgejo", "ref": "forgejo", "sha": "a74759dc39987e02ee6f85260e47c0f29b1a325e", "repo": {"full_name": "forgejo/forgejo"}}
}`

type giteaServer struct {
	mu      sync.Mutex
	calls   []asked
	status  int
	answers map[string]string
}

// giteaAPI answers like Gitea's API, by path, and notes what it was
// asked.
func giteaAPI(t *testing.T, status int, answers map[string]string) (Gitea, *giteaServer) {
	t.Helper()
	g := &giteaServer{status: status, answers: answers}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		g.mu.Lock()
		g.calls = append(g.calls, asked{r.Method, r.URL.EscapedPath(), r.Header.Get("Authorization"), string(body)})
		g.mu.Unlock()
		answer, ok := answers[strings.TrimPrefix(r.URL.Path, "/api/v1")]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"message": "The target couldn't be found."}`)
			return
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, answer)
	}))
	t.Cleanup(srv.Close)
	return Gitea{Host: "codeberg.org", Repo: "forgejo/forgejo", API: srv.URL + "/api/v1"}, g
}

func TestGiteaReadsAPullRequest(t *testing.T) {
	g, s := giteaAPI(t, http.StatusOK, map[string]string{"/repos/forgejo/forgejo/pulls/14553": realGiteaPull})
	pr, err := g.PullRequest(t.Context(), 14553)
	if err != nil {
		t.Fatal(err)
	}
	want := PR{
		Number: 14553, Title: "fix: prevent off by one by excluding size == 1024", Author: "kbruen",
		Branch: "forgejo", HeadSHA: "755bff0771ffd7785c0f5ad2f5fa6bae8e8c57f8", BaseSHA: "a74759dc39987e02ee6f85260e47c0f29b1a325e",
		BaseBranch: "forgejo", State: Open, URL: "https://codeberg.org/forgejo/forgejo/pulls/14553",
	}
	if pr != want {
		t.Errorf("pr =\n%+v\nwant\n%+v", pr, want)
	}
	if len(s.calls) != 1 || s.calls[0].token != "" {
		t.Errorf("asked %+v", s.calls)
	}
}

func TestGiteaStatesAndDrafts(t *testing.T) {
	for body, want := range map[string]PR{
		`{"number": 7, "state": "closed", "merged": true}`:             {State: Merged},
		`{"number": 7, "state": "closed", "merged": false}`:            {State: Closed},
		`{"number": 7, "state": "open", "draft": true}`:                {State: Open, Draft: true},
		`{"number": 7, "state": "open", "title": "WIP: half done"}`:    {State: Open, Draft: true},
		`{"number": 7, "state": "open", "title": "[wip] half done"}`:   {State: Open, Draft: true},
		`{"number": 7, "state": "open", "title": "Fix WIP: counting"}`: {State: Open},
	} {
		g, _ := giteaAPI(t, http.StatusOK, map[string]string{"/repos/forgejo/forgejo/pulls/7": body})
		pr, err := g.PullRequest(t.Context(), 7)
		if err != nil || pr.State != want.State || pr.Draft != want.Draft {
			t.Errorf("%s: %+v, %v", body, pr, err)
		}
	}
}

func TestGiteaSaysWhatWentWrong(t *testing.T) {
	g, _ := giteaAPI(t, http.StatusOK, nil)
	_, err := g.PullRequest(t.Context(), 99)
	if err == nil || !strings.Contains(err.Error(), "codeberg.org has no pull request #99 in forgejo/forgejo") ||
		!strings.Contains(errs.Hint(err), "FORGEJO_TOKEN") || !strings.Contains(err.Error(), "codeberg.org answered 404: The target couldn't be found.") {
		t.Errorf("err = %v, hint %q", err, errs.Hint(err))
	}
	g.Token = "t"
	if _, err := g.PullRequest(t.Context(), 99); strings.Contains(errs.Hint(err), "FORGEJO_TOKEN") {
		t.Errorf("hint = %q", errs.Hint(err))
	}
	for status, want := range map[int]string{401: "did not accept the token", 403: "may not do this on #7", 500: "could not answer about #7"} {
		g, _ := giteaAPI(t, status, map[string]string{"/repos/forgejo/forgejo/pulls/7": `{"message": "no"}`})
		if _, err := g.PullRequest(t.Context(), 7); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%d: err = %v", status, err)
		}
	}
	g.API = "http://127.0.0.1:1/api/v1"
	if _, err := g.PullRequest(t.Context(), 7); err == nil || !strings.Contains(err.Error(), "cannot reach codeberg.org") {
		t.Errorf("err = %v", err)
	}
	if _, err := g.PullRequest(t.Context(), 0); err == nil {
		t.Error("read #0")
	}
}

func TestGiteaComments(t *testing.T) {
	g, s := giteaAPI(t, http.StatusCreated, map[string]string{
		"/repos/forgejo/forgejo/issues/7/comments": `{"id": 42, "html_url": "https://codeberg.org/forgejo/forgejo/pulls/7#issuecomment-42"}`,
	})
	if _, err := g.Comment(t.Context(), 7, "hello"); err == nil || !strings.Contains(errs.Hint(err), "FORGEJO_TOKEN") {
		t.Errorf("without a token: %v", err)
	}
	g.Token = "secret"
	url, err := g.Comment(t.Context(), 7, "### Review notes")
	if err != nil || url != "https://codeberg.org/forgejo/forgejo/pulls/7#issuecomment-42" {
		t.Fatalf("%q, %v", url, err)
	}
	var body map[string]string
	c := s.calls[len(s.calls)-1]
	if json.Unmarshal([]byte(c.body), &body) != nil || body["body"] != "### Review notes" || c.token != "token secret" || c.method != http.MethodPost {
		t.Errorf("asked %+v", c)
	}
	// Without a link in the answer, pit makes one.
	g2, _ := giteaAPI(t, http.StatusCreated, map[string]string{"/repos/forgejo/forgejo/issues/7/comments": `{"id": 43}`})
	g2.Token = "secret"
	if url, _ := g2.Comment(t.Context(), 7, "hi"); url != "https://codeberg.org/forgejo/forgejo/pulls/7#issuecomment-43" {
		t.Errorf("url = %q", url)
	}
	for _, body := range []string{" ", strings.Repeat("x", MaxComment+1)} {
		if _, err := g.Comment(t.Context(), 7, body); err == nil {
			t.Errorf("posted %d characters", len(body))
		}
	}
	if _, err := g.Comment(t.Context(), 0, "hi"); err == nil {
		t.Error("commented on #0")
	}
}

func TestProbingAsksWhatAServerRuns(t *testing.T) {
	g, _ := giteaAPI(t, http.StatusOK, map[string]string{
		"/version":                                     `{"version": "1.22.0"}`,
		"/repos/forgejo/forgejo/pulls/14553":           realGiteaPull,
		"/repos/forgejo/forgejo/issues/14553/comments": `{"id": 1, "html_url": "u"}`,
	})
	g.Host, g.Token = "git.acme.net", "t"
	p := Probing{Gitea: g, Git: stubForge{PR{Title: "from the commit"}}}
	if pr, err := p.PullRequest(t.Context(), 14553); err != nil || pr.Author != "kbruen" {
		t.Errorf("%+v, %v", pr, err)
	}
	if url, err := p.Comment(t.Context(), 14553, "hi"); err != nil || url != "u" {
		t.Errorf("%q, %v", url, err)
	}

	// Something else that answers in JSON is not one either.
	json200, _ := giteaAPI(t, http.StatusOK, map[string]string{"/version": `{}`})
	if json200.IsGitea(t.Context()) {
		t.Error("{} is not a Gitea's version")
	}
	// Something else: the commit, and no comment.
	other, _ := giteaAPI(t, http.StatusOK, map[string]string{"/version": `<html>`})
	other.Host = "git.acme.net"
	p = Probing{Gitea: other, Git: stubForge{PR{Title: "from the commit"}}}
	if pr, err := p.PullRequest(t.Context(), 7); err != nil || pr.Title != "from the commit" {
		t.Errorf("%+v, %v", pr, err)
	}
	if _, err := p.Comment(t.Context(), 7, "hi"); err == nil || !strings.Contains(err.Error(), "git.acme.net is none it recognises") {
		t.Errorf("err = %v", err)
	}
	if _, err := (Probing{Gitea: other}).PullRequest(t.Context(), 7); err == nil {
		t.Error("read a pull request with nothing to read it with")
	}
}

type stubForge struct{ pr PR }

func (s stubForge) PullRequest(_ context.Context, n int) (PR, error) {
	s.pr.Number = n
	return s.pr, nil
}
