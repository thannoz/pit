package auth

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/forge"
)

func TestLoginForEachService(t *testing.T) {
	defer func(gh, gl string) { GitHubClientID, GitLabClientID = gh, gl }(GitHubClientID, GitLabClientID)
	GitHubClientID, GitLabClientID = "Ov23pit", "glpit"

	l := LoginFor(forge.GitHubService, "github.com", "")
	if l.Flow == nil || l.Flow.ClientID != "Ov23pit" || l.Flow.CodeURL != "https://github.com/login/device/code" ||
		l.Flow.TokenURL != "https://github.com/login/oauth/access_token" || strings.Join(l.Flow.Scopes, " ") != "repo" {
		t.Errorf("github.com: %+v %+v", l, l.Flow)
	}
	// pit's application is github.com's; a company's server needs one
	// of its own.
	if l := LoginFor(forge.GitHubService, "github.acme.net", ""); l.Flow != nil || !strings.HasPrefix(l.TokenPage, "https://github.acme.net/settings/tokens/new") {
		t.Errorf("enterprise: %+v", l)
	}
	if l := LoginFor(forge.GitHubService, "github.acme.net", "Iv1.own"); l.Flow == nil || l.Flow.ClientID != "Iv1.own" || l.Flow.CodeURL != "https://github.acme.net/login/device/code" {
		t.Errorf("enterprise with an application: %+v", l.Flow)
	}

	l = LoginFor(forge.GitLabService, "gitlab.com", "")
	if l.Flow == nil || l.Flow.ClientID != "glpit" || l.Flow.CodeURL != "https://gitlab.com/oauth/authorize_device" ||
		l.Flow.TokenURL != "https://gitlab.com/oauth/token" || strings.Join(l.Flow.Scopes, " ") != "api" {
		t.Errorf("gitlab.com: %+v", l.Flow)
	}
	if l := LoginFor(forge.GitLabService, "gitlab.acme.net", ""); l.Flow != nil || !strings.Contains(l.TokenPage, "gitlab.acme.net/-/user_settings/personal_access_tokens") {
		t.Errorf("self-hosted gitlab: %+v", l)
	}

	// Gitea, Forgejo and Bitbucket have no device flow.
	for _, tc := range []struct{ service, host, page string }{
		{forge.GiteaService, "codeberg.org", "https://codeberg.org/user/settings/applications"},
		{forge.BitbucketService, "bitbucket.org", "https://id.atlassian.com/manage-profile/security/api-tokens"},
	} {
		if l := LoginFor(tc.service, tc.host, "x"); l.Flow != nil || l.TokenPage != tc.page || l.Needs == "" {
			t.Errorf("%s: %+v", tc.host, l)
		}
	}

	// Without pit's own application, github.com is logged in to by hand.
	GitHubClientID = ""
	if l := LoginFor(forge.GitHubService, "github.com", ""); l.Flow != nil {
		t.Errorf("no application: %+v", l.Flow)
	}
}

func TestACredentialKeepsWhatARefreshNeeds(t *testing.T) {
	l := LoginFor(forge.GitLabService, "gitlab.example.com", "own")
	exp := time.Date(2026, 9, 26, 14, 0, 0, 0, time.UTC)
	c := l.Credential(Token{Access: "a", Refresh: "r", Expires: exp}, "me")
	if c.Token != "a" || c.Refresh != "r" || c.TokenURL != "https://gitlab.example.com/oauth/token" || c.ClientID != "own" || c.User != "me" || !c.Expires.Equal(exp) {
		t.Errorf("%+v", c)
	}
	c = LoginFor(forge.GitHubService, "github.com", "gh").Credential(Token{Access: "a"}, "me")
	if c.Refresh != "" || c.TokenURL != "" || c.ClientID != "gh" {
		t.Errorf("%+v", c)
	}
}

// refreshServer is GitLab's token endpoint, renewing or not.
func refreshServer(t *testing.T, status int, body string) (string, *[]url.Values) {
	t.Helper()
	var forms []url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		form, _ := url.ParseQuery(string(raw))
		form.Set("path", r.URL.Path)
		forms = append(forms, form)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, &forms
}

func TestCurrentRenewsAnExpiredLogin(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	base, forms := refreshServer(t, 200, `{"access_token":"new","refresh_token":"r2","expires_in":7200}`)
	s := &Memory{}
	old := Credential{Token: "old", Refresh: "r1", Expires: now.Add(-time.Minute), TokenURL: base + "/oauth/token", ClientID: "c", User: "me"}
	_ = s.Set("gitlab.com", old)

	c, err := Current(t.Context(), s, nil, "gitlab.com", now)
	if err != nil || c.Token != "new" || c.Refresh != "r2" || !c.Expires.Equal(now.Add(2*time.Hour)) || c.User != "me" {
		t.Fatalf("%+v, %v", c, err)
	}
	if f := (*forms)[0]; f.Get("grant_type") != "refresh_token" || f.Get("refresh_token") != "r1" || f.Get("client_id") != "c" {
		t.Errorf("asked with %v", f)
	}
	// Kept, so the next command does not renew it again.
	if kept, _ := s.Get("gitlab.com"); kept.Token != "new" {
		t.Errorf("kept %+v", kept)
	}
	if _, err := Current(t.Context(), s, nil, "gitlab.com", now); err != nil || len(*forms) != 1 {
		t.Errorf("renewed twice: %v", err)
	}
}

func TestCurrentCannotRenew(t *testing.T) {
	now := time.Now()
	s := &Memory{}
	if _, err := Current(t.Context(), s, nil, "github.com", now); !errors.Is(err, ErrNone) {
		t.Errorf("no login: %v", err)
	}
	// An expired token without a refresh token is the end of it.
	_ = s.Set("gitlab.com", Credential{Token: "t", Expires: now.Add(-time.Hour)})
	if _, err := Current(t.Context(), s, nil, "gitlab.com", now); err == nil || !strings.Contains(errs.Hint(err), "pit auth login gitlab.com") {
		t.Errorf("err = %v", err)
	}
	// Nor with a token address but nothing to renew with: the host is
	// not even asked.
	base, forms := refreshServer(t, 200, `{"access_token":"new"}`)
	_ = s.Set("gitlab.com", Credential{Token: "t", Expires: now.Add(-time.Hour), TokenURL: base + "/oauth/token", ClientID: "c"})
	if _, err := Current(t.Context(), s, nil, "gitlab.com", now); err == nil || !strings.Contains(err.Error(), "has expired") || len(*forms) != 0 {
		t.Errorf("err = %v, asked %d times", err, len(*forms))
	}
	// A refresh token the host no longer takes says to log in again.
	base, _ = refreshServer(t, 400, `{"error":"invalid_grant","error_description":"The provided authorization grant is invalid, expired, revoked"}`)
	_ = s.Set("gitlab.com", Credential{Token: "t", Refresh: "r", Expires: now.Add(-time.Hour), TokenURL: base + "/oauth/token", ClientID: "c"})
	_, err := Current(t.Context(), s, nil, "gitlab.com", now)
	if err == nil || !strings.Contains(err.Error(), "would not renew") || !strings.Contains(err.Error(), "grant is invalid") ||
		!strings.Contains(errs.Hint(err), "pit auth login gitlab.com") {
		t.Errorf("err = %v", err)
	}
}

func TestRevoke(t *testing.T) {
	base, forms := refreshServer(t, 200, `{}`)
	ok, err := Revoke(t.Context(), nil, Credential{Token: "glo", TokenURL: base + "/oauth/token", ClientID: "c"})
	if !ok || err != nil {
		t.Fatalf("%v, %v", ok, err)
	}
	if f := (*forms)[0]; f.Get("path") != "/oauth/revoke" || f.Get("token") != "glo" || f.Get("client_id") != "c" {
		t.Errorf("asked with %v", f)
	}
	// GitHub's, and a token made by hand, cannot be.
	for _, c := range []Credential{
		{Token: "gho", ClientID: "Ov23", TokenURL: "https://github.com/login/oauth/access_token"},
		{Token: "gho", ClientID: "Ov23"},
		{Token: "glpat", TokenURL: base + "/oauth/token"},
	} {
		if ok, err := Revoke(t.Context(), nil, c); ok || err != nil {
			t.Errorf("%+v: %v, %v", c, ok, err)
		}
	}
	if len(*forms) != 1 {
		t.Errorf("asked %d times", len(*forms))
	}
	base, _ = refreshServer(t, 500, `oops`)
	if ok, err := Revoke(t.Context(), nil, Credential{Token: "glo", TokenURL: base + "/oauth/token", ClientID: "c"}); ok || err == nil {
		t.Errorf("%v, %v", ok, err)
	}
}
