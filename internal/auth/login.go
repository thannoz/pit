package auth

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/forge"
)

// The IDs of pit's applications on github.com and gitlab.com, which the
// device flow names to say who is asking. They are public by design: a
// device-flow application has no secret. Empty until the applications
// are registered; a release sets them with -ldflags -X.
var (
	GitHubClientID = ""
	GitLabClientID = ""
)

// Login is how pit logs in to a host.
type Login struct {
	// Service is what the host runs: forge.GitHubService, ...
	Service string
	Host    string
	// Flow is the device flow, or nil where there is none: Gitea,
	// Forgejo and Bitbucket have none, and a server of a company's own
	// has none of pit's unless given an application's ID.
	Flow *DeviceFlow
	// TokenPage is where a token is made by hand, and Needs what it has
	// to be allowed.
	TokenPage string
	Needs     string
}

// LoginFor is how pit logs in to a host running service. clientID is
// an application of the reviewer's own, for a server pit has none on.
func LoginFor(service, host, clientID string) Login {
	l := Login{Service: service, Host: host}
	base := "https://" + host
	switch service {
	case forge.GitHubService:
		if clientID == "" && strings.EqualFold(host, "github.com") {
			clientID = GitHubClientID
		}
		if clientID != "" {
			l.Flow = &DeviceFlow{
				Host: host, ClientID: clientID, Scopes: []string{"repo"},
				CodeURL: base + "/login/device/code", TokenURL: base + "/login/oauth/access_token",
			}
		}
		l.TokenPage = base + "/settings/tokens/new?description=pit&scopes=repo"
		l.Needs = "the repo scope"
	case forge.GitLabService:
		if clientID == "" && strings.EqualFold(host, "gitlab.com") {
			clientID = GitLabClientID
		}
		if clientID != "" {
			l.Flow = &DeviceFlow{
				Host: host, ClientID: clientID, Scopes: []string{"api"},
				CodeURL: base + "/oauth/authorize_device", TokenURL: base + "/oauth/token",
			}
		}
		l.TokenPage = base + "/-/user_settings/personal_access_tokens?name=pit&scopes=api"
		l.Needs = "the api scope"
	case forge.GiteaService:
		l.TokenPage = base + "/user/settings/applications"
		l.Needs = "read access to repositories and write access to issues"
	case forge.BitbucketService:
		l.TokenPage = "https://id.atlassian.com/manage-profile/security/api-tokens"
		l.Needs = "read and write access to pull requests; enter it as email:token, or paste an access token of a repository"
	}
	return l
}

// Credential is what is kept of a token the flow handed over.
func (l Login) Credential(t Token, user string) Credential {
	c := Credential{Token: t.Access, Expires: t.Expires, User: user}
	if l.Flow != nil {
		c.ClientID = l.Flow.ClientID
		if t.Refresh != "" {
			c.Refresh, c.TokenURL = t.Refresh, l.Flow.TokenURL
		}
	}
	return c
}

// Current is a host's login, renewed first when it has expired.
func Current(ctx context.Context, s Store, client *http.Client, host string, now time.Time) (Credential, error) {
	c, err := s.Get(host)
	if err != nil || !c.Expired(now) {
		return c, err
	}
	if c.Refresh == "" || c.TokenURL == "" {
		return Credential{}, errs.New("the login to %s has expired", host).
			WithHint("run `pit auth login %s` again", host)
	}
	t, err := Refresh(ctx, client, host, c, now)
	if err != nil {
		return Credential{}, err
	}
	c.Token, c.Expires = t.Access, t.Expires
	if t.Refresh != "" {
		c.Refresh = t.Refresh
	}
	if err := s.Set(host, c); err != nil {
		return Credential{}, err
	}
	return c, nil
}

// Revoke asks the host to forget a token, where it lets pit: GitLab
// does, for the application it gave the token to. GitHub wants the
// application's secret, which pit does not have, so false says the
// reviewer has to do it on the host's page.
func Revoke(ctx context.Context, client *http.Client, c Credential) (bool, error) {
	if c.ClientID == "" || !strings.HasSuffix(c.TokenURL, "/oauth/token") {
		return false, nil
	}
	to := strings.TrimSuffix(c.TokenURL, "/token") + "/revoke"
	form := url.Values{"client_id": {c.ClientID}, "token": {c.Token}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, to, strings.NewReader(form.Encode()))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	_ = resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return false, errs.New("%s answered %d", to, resp.StatusCode)
	}
	return true, nil
}
