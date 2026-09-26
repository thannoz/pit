package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/thannoz/pit/internal/auth"
	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/forge"
	"github.com/thannoz/pit/internal/ui"
	"github.com/thannoz/pit/internal/workspace"
)

// withLogins gives a test a keychain of its own, and hosts that answer
// whose a token is from users: a token not in it is refused.
func withLogins(t *testing.T, users map[string]string) *auth.Memory {
	t.Helper()
	logins := &auth.Memory{}
	prevKeychain, prevWho, prevProbe, prevGh, prevRepo := keychain, whoami, probeGitea, ghLoggedIn, currentRepo
	keychain = func() auth.Store { return logins }
	// A 401 is what a host says to a token it does not take.
	refused := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"message":"401 Unauthorized"}`)
	}))
	t.Cleanup(refused.Close)
	whoami = func(ctx context.Context, service, host, token string) (string, error) {
		if user, ok := users[token]; ok {
			return user, nil
		}
		return forge.GitLab{Host: host, Token: token, API: refused.URL}.User(ctx)
	}
	probeGitea = func(context.Context, string) bool { return false }
	ghLoggedIn = func(context.Context) string { return "" }
	currentRepo = func(context.Context) (workspace.Repo, error) { return workspace.Repo{}, errors.New("not in one") }
	for _, name := range []string{"GH_TOKEN", "GITHUB_TOKEN", "GITLAB_TOKEN", "GITLAB_ACCESS_TOKEN", "FORGEJO_TOKEN", "GITEA_TOKEN", "BITBUCKET_TOKEN", "BITBUCKET_APP_PASSWORD"} {
		t.Setenv(name, "")
	}
	t.Cleanup(func() {
		keychain, whoami, probeGitea, ghLoggedIn, currentRepo = prevKeychain, prevWho, prevProbe, prevGh, prevRepo
	})
	return logins
}

// runWith runs pit with stdin.
func runWith(t *testing.T, stdin io.Reader, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	var out, errOut bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetIn(stdin)
	cmd.SetArgs(args)
	cmd.SetContext(t.Context())
	err = postProcess(cmd.Execute())
	return out.String(), errOut.String(), err
}

func TestLoginWithAToken(t *testing.T) {
	logins := withLogins(t, map[string]string{"ghp_good": "octocat"})
	out, _, err := runWith(t, strings.NewReader("ghp_good\n"), "auth", "login", "--with-token")
	if err != nil {
		t.Fatal(err)
	}
	// Outside a repository, the host is github.com.
	if !strings.Contains(out, "Logged in to github.com as octocat. The token is in the system keychain.") {
		t.Errorf("out = %q", out)
	}
	if c, err := logins.Get("github.com"); err != nil || c.Token != "ghp_good" || c.User != "octocat" || c.ClientID != "" {
		t.Errorf("stored %+v, %v", c, err)
	}
	// It was read, not asked for.
	if strings.Contains(out, "paste it here") {
		t.Errorf("asked: %q", out)
	}
}

func TestLoginRefusesATokenTheHostDoesNot(t *testing.T) {
	logins := withLogins(t, nil)
	_, _, err := runWith(t, strings.NewReader("ghp_wrong\n"), "auth", "login", "--with-token")
	if err == nil || !strings.Contains(err.Error(), "github.com did not accept the token") || !strings.Contains(errs.Hint(err), "nothing was stored") {
		t.Errorf("err = %v, hint %q", err, errs.Hint(err))
	}
	if hosts, _ := logins.Hosts(); len(hosts) != 0 {
		t.Errorf("stored %q", hosts)
	}
	if _, _, err := runWith(t, strings.NewReader("  \n"), "auth", "login", "--with-token"); err == nil || !strings.Contains(err.Error(), "no token was given") {
		t.Errorf("empty: %v", err)
	}
}

// Where there is no device flow, pit asks for a token, and says where
// one is made.
func TestLoginAsksForAToken(t *testing.T) {
	logins := withLogins(t, map[string]string{"fj_good": "forgejo-user"})
	out, _, err := runWith(t, strings.NewReader("fj_good\n"), "auth", "login", "https://Codeberg.org/some/repo")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Make a token at https://codeberg.org/user/settings/applications") || !strings.Contains(out, "Logged in to codeberg.org as forgejo-user") {
		t.Errorf("out = %q", out)
	}
	if c, _ := logins.Get("codeberg.org"); c.Token != "fj_good" {
		t.Errorf("stored %+v", c)
	}

	// Nobody to ask is said, with what to do instead.
	null, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = null.Close() }()
	_, _, err = runWith(t, null, "auth", "login", "codeberg.org")
	if err == nil || !strings.Contains(err.Error(), "nobody to ask") || !strings.Contains(errs.Hint(err), "--with-token") {
		t.Errorf("err = %v", err)
	}
}

// A host whose name does not say what it runs is asked whether it runs
// Gitea or Forgejo, and refused if it does not.
func TestLoginToAServerOfOnesOwn(t *testing.T) {
	logins := withLogins(t, map[string]string{"t": "me"})
	if _, _, err := runWith(t, strings.NewReader("t"), "auth", "login", "git.example.org", "--with-token"); err == nil || !strings.Contains(err.Error(), "cannot tell what git.example.org runs") {
		t.Errorf("err = %v", err)
	}
	probeGitea = func(_ context.Context, host string) bool { return host == "git.example.org" }
	if _, _, err := runWith(t, strings.NewReader("t"), "auth", "login", "git.example.org", "--with-token"); err != nil {
		t.Fatal(err)
	}
	if c, _ := logins.Get("git.example.org"); c.Token != "t" {
		t.Errorf("stored %+v", c)
	}
}

func TestLoginWithTheDeviceFlow(t *testing.T) {
	logins := withLogins(t, map[string]string{"gho_flow": "octocat"})
	polls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/code":
			_, _ = io.WriteString(w, `{"device_code":"dc","user_code":"WDJB-MJHT","verification_uri":"https://github.com/login/device","expires_in":900,"interval":5}`)
		default:
			polls++
			if polls == 1 {
				_, _ = io.WriteString(w, `{"error":"authorization_pending"}`)
				return
			}
			_, _ = io.WriteString(w, `{"access_token":"gho_flow","token_type":"bearer","scope":"repo"}`)
		}
	}))
	defer srv.Close()
	prev := loginFor
	loginFor = func(service, host, clientID string) auth.Login {
		l := auth.LoginFor(service, host, clientID)
		l.Flow = &auth.DeviceFlow{Host: host, CodeURL: srv.URL + "/code", TokenURL: srv.URL + "/token", ClientID: "Ov23pit",
			Sleep: func(context.Context, time.Duration) error { return nil }}
		return l
	}
	t.Cleanup(func() { loginFor = prev })
	// A token in the environment wins over the login, which is said.
	t.Setenv("GH_TOKEN", "env")

	out, stderr, err := runWith(t, strings.NewReader(""), "auth", "login")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Enter the code WDJB-MJHT at https://github.com/login/device") || !strings.Contains(out, "Logged in to github.com as octocat") {
		t.Errorf("out = %q", out)
	}
	if !strings.Contains(stderr, "GH_TOKEN is set, and pit uses it instead of this login") {
		t.Errorf("stderr = %q", stderr)
	}
	if c, _ := logins.Get("github.com"); c.Token != "gho_flow" || c.ClientID != "Ov23pit" || c.User != "octocat" || polls != 2 {
		t.Errorf("stored %+v after %d polls", c, polls)
	}
}

// GitLab hands out a link with the code filled in, which is the one
// shown.
func TestLoginShowsTheLinkWithTheCode(t *testing.T) {
	withLogins(t, map[string]string{"glo": "me"})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/code" {
			_, _ = io.WriteString(w, `{"device_code":"dc","user_code":"0A44L90H","verification_uri":"https://gitlab.com/oauth/device","verification_uri_complete":"https://gitlab.com/oauth/device?user_code=0A44L90H","interval":5}`)
			return
		}
		_, _ = io.WriteString(w, `{"access_token":"glo","expires_in":7200,"refresh_token":"r"}`)
	}))
	defer srv.Close()
	prev := loginFor
	loginFor = func(service, host, clientID string) auth.Login {
		l := auth.LoginFor(service, host, clientID)
		l.Flow = &auth.DeviceFlow{Host: host, CodeURL: srv.URL + "/code", TokenURL: srv.URL + "/token", ClientID: "c",
			Sleep: func(context.Context, time.Duration) error { return nil }}
		return l
	}
	t.Cleanup(func() { loginFor = prev })
	out, _, err := runWith(t, strings.NewReader(""), "auth", "login", "gitlab.com")
	if err != nil || !strings.Contains(out, "Enter the code 0A44L90H at https://gitlab.com/oauth/device?user_code=0A44L90H\n") {
		t.Errorf("out = %q, %v", out, err)
	}
	if c, _ := keychain().Get("gitlab.com"); c.Refresh != "r" || c.TokenURL != srv.URL+"/token" || c.Expires.IsZero() {
		t.Errorf("stored %+v", c)
	}
}

// Inside a repository, the host is the repository's.
func TestLoginToTheRepositorysHost(t *testing.T) {
	logins := withLogins(t, map[string]string{"gl": "me"})
	currentRepo = func(context.Context) (workspace.Repo, error) {
		return workspace.Repo{Identity: workspace.Identity{Host: "gitlab.com", Owner: "a", Name: "b"}}, nil
	}
	if _, _, err := runWith(t, strings.NewReader("gl"), "auth", "login", "--with-token"); err != nil {
		t.Fatal(err)
	}
	if c, _ := logins.Get("gitlab.com"); c.Token != "gl" {
		t.Errorf("stored %+v", c)
	}
}

func TestAuthStatus(t *testing.T) {
	logins := withLogins(t, map[string]string{"gho": "octocat", "bb": ""})
	_ = logins.Set("github.com", auth.Credential{Token: "gho", User: "octocat"})
	_ = logins.Set("bitbucket.org", auth.Credential{Token: "bb"})
	t.Setenv("GH_TOKEN", "env")
	out, _, err := runWith(t, strings.NewReader(""), "auth", "status")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"✓  github.com     as octocat; GH_TOKEN is set and is used instead", "✓  bitbucket.org  with a token of nobody's"} {
		if !strings.Contains(out, want) {
			t.Errorf("out = %q, want %q", out, want)
		}
	}

	// A token the host no longer takes fails the command.
	_ = logins.Set("gitlab.com", auth.Credential{Token: "revoked"})
	out, _, err = runWith(t, strings.NewReader(""), "auth", "status")
	if !strings.Contains(out, "✗  gitlab.com     the token is no longer taken") || err == nil || !strings.Contains(errs.Hint(err), "pit auth login gitlab.com") {
		t.Errorf("out = %q, err = %v", out, err)
	}

	out, _, _ = runWith(t, strings.NewReader(""), "auth", "status", "--json")
	var lines []loginStatus
	if err := json.Unmarshal([]byte(out), &lines); err != nil || len(lines) != 3 {
		t.Fatalf("%q: %v", out, err)
	}
	if l := lines[1]; l.Host != "github.com" || l.Service != "GitHub" || l.User != "octocat" || !l.OK || l.Env != "GH_TOKEN" {
		t.Errorf("%+v", l)
	}
	if l := lines[2]; l.OK || l.Problem == "" {
		t.Errorf("%+v", l)
	}
}

func TestAuthStatusWithoutLogins(t *testing.T) {
	withLogins(t, nil)
	out, _, err := runWith(t, strings.NewReader(""), "auth", "status")
	if err != nil || !strings.Contains(out, "pit is not logged in anywhere") || strings.Contains(out, "gh") {
		t.Errorf("%q, %v", out, err)
	}
	ghLoggedIn = func(context.Context) string { return "is logged in" }
	out, _, _ = runWith(t, strings.NewReader(""), "auth", "status")
	if !strings.Contains(out, "For GitHub, pit asks gh, which is logged in.") {
		t.Errorf("%q", out)
	}
}

func TestLogout(t *testing.T) {
	logins := withLogins(t, nil)
	_ = logins.Set("github.com", auth.Credential{Token: "gho", ClientID: "Ov23pit"})
	_ = logins.Set("codeberg.org", auth.Credential{Token: "fj"})
	out, _, err := runWith(t, strings.NewReader(""), "auth", "logout")
	if err != nil {
		t.Fatal(err)
	}
	// GitHub cannot be asked to revoke it; where it can be is said.
	if !strings.Contains(out, "Logged out of github.com.") || !strings.Contains(out, "revoke it at https://github.com/settings/applications") {
		t.Errorf("out = %q", out)
	}
	if _, err := logins.Get("github.com"); !errors.Is(err, auth.ErrNone) {
		t.Errorf("still there: %v", err)
	}
	out, _, _ = runWith(t, strings.NewReader(""), "auth", "logout", "codeberg.org")
	if !strings.Contains(out, "https://codeberg.org/user/settings/applications") {
		t.Errorf("out = %q", out)
	}
	if _, _, err := runWith(t, strings.NewReader(""), "auth", "logout", "github.com"); err == nil || !strings.Contains(err.Error(), "not logged in to github.com") {
		t.Errorf("err = %v", err)
	}
}

func TestRevokePages(t *testing.T) {
	for _, tc := range []struct {
		host string
		c    auth.Credential
		want string
	}{
		{"github.com", auth.Credential{}, "https://github.com/settings/tokens"},
		{"gitlab.com", auth.Credential{}, "https://gitlab.com/-/user_settings/personal_access_tokens"},
		{"bitbucket.org", auth.Credential{}, "https://id.atlassian.com/manage-profile/security/api-tokens"},
		{"git.example.org", auth.Credential{}, "https://git.example.org/user/settings/applications"},
	} {
		if got := revokePage(tc.host, tc.c); got != tc.want {
			t.Errorf("%s: %s", tc.host, got)
		}
	}
}

// A login that cannot be used is said, and pit goes on without it.
func TestStoredToken(t *testing.T) {
	logins := withLogins(t, nil)
	_ = logins.Set("github.com", auth.Credential{Token: "gho"})
	_ = logins.Set("gitlab.com", auth.Credential{Token: "old", Expires: time.Now().Add(-time.Hour)})
	var errOut bytes.Buffer
	tokens := storedToken(t.Context(), ui.New(io.Discard, &errOut))
	if got := tokens("github.com"); got != "gho" {
		t.Errorf("github.com: %q", got)
	}
	if got := tokens("codeberg.org"); got != "" || errOut.Len() != 0 {
		t.Errorf("no login: %q, said %q", got, errOut.String())
	}
	if got := tokens("gitlab.com"); got != "" || !strings.Contains(errOut.String(), "pit's login to gitlab.com cannot be used") ||
		!strings.Contains(errOut.String(), "pit auth login gitlab.com") {
		t.Errorf("expired: %q, said %q", got, errOut.String())
	}
}
