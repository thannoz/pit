package forge

import (
	"encoding/base64"
	"net/http"
	"testing"
)

// Each service is asked whose a token is at its own address, with the
// token as it takes one.
func TestUserOfEachService(t *testing.T) {
	gl, glCalls := gitlabAPI(t, http.StatusOK, `{"id": 1, "username": "gitlab-user"}`)
	gl.Token = "glpat"
	if user, err := gl.User(t.Context()); err != nil || user != "gitlab-user" {
		t.Errorf("gitlab: %q, %v", user, err)
	}
	if c := (*glCalls)[0]; c.path != "/api/v4/user" || c.token != "glpat" {
		t.Errorf("gitlab asked %+v", c)
	}

	g, s := giteaAPI(t, http.StatusOK, map[string]string{"/user": `{"id": 1, "login": "forgejo-user"}`})
	g.Token = "fj"
	if user, err := g.User(t.Context()); err != nil || user != "forgejo-user" {
		t.Errorf("gitea: %q, %v", user, err)
	}
	if c := s.calls[0]; c.path != "/api/v1/user" || c.token != "token fj" {
		t.Errorf("gitea asked %+v", c)
	}

	b, bbCalls := bitbucketAPI(t, http.StatusOK, `{"username": "bb-user", "nickname": "Bee"}`)
	b.Username, b.Password = "me@example.com", "api-token"
	if user, err := b.User(t.Context()); err != nil || user != "bb-user" {
		t.Errorf("bitbucket: %q, %v", user, err)
	}
	basic := "Basic " + base64.StdEncoding.EncodeToString([]byte("me@example.com:api-token"))
	if c := (*bbCalls)[0]; c.path != "/2.0/user" || c.token != basic {
		t.Errorf("bitbucket asked %+v", c)
	}

	// An access token of a repository is nobody's, which Bitbucket says
	// with 403; a wrong one it says with 401.
	b, _ = bitbucketAPI(t, http.StatusForbidden, `{"type": "error", "error": {"message": "Access denied"}}`)
	b.Token = "repo-token"
	if user, err := b.User(t.Context()); err != nil || user != "" {
		t.Errorf("repository token: %q, %v", user, err)
	}
	b, _ = bitbucketAPI(t, http.StatusUnauthorized, `{"type": "error", "error": {"message": "Unauthorized"}}`)
	b.Token = "wrong"
	if _, err := b.User(t.Context()); !Rejected(err) {
		t.Errorf("wrong token: %v", err)
	}
	// What pit keeps is split as the login was entered.
	b, bbCalls = bitbucketAPI(t, http.StatusOK, `{"username": "bb-user"}`)
	if _, err := b.with("me@example.com:api-token").User(t.Context()); err != nil || (*bbCalls)[0].token != basic {
		t.Errorf("email:token asked %+v, %v", *bbCalls, err)
	}
	if b := (Bitbucket{Username: "x", Password: "y"}).with("access"); b.Token != "access" || b.Username != "" || b.Password != "" {
		t.Errorf("access token: %+v", b)
	}

	// Without a token, a 403 is no answer either.
	b, _ = bitbucketAPI(t, http.StatusForbidden, `{}`)
	b.Username, b.Password = "me@example.com", "t"
	if _, err := b.User(t.Context()); err == nil {
		t.Error("a 403 to someone is not nobody's token")
	}
}
