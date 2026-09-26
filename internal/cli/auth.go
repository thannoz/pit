package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/thannoz/pit/internal/auth"
	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/forge"
	"github.com/thannoz/pit/internal/proc"
	"github.com/thannoz/pit/internal/ui"
)

// keychain is where pit keeps its logins; a variable so that no test
// reaches the keychain of the machine it runs on.
var keychain = func() auth.Store { return auth.Keychain{Service: "pit"} }

// The ways pit talks to a host while logging in; variables so that a
// test talks to one of its own.
var (
	loginFor   = auth.LoginFor
	whoami     = forge.User
	probeGitea = func(ctx context.Context, host string) bool { return forge.Gitea{Host: host}.IsGitea(ctx) }
	ghLoggedIn = ghStatus
)

// storedToken is pit's login to a host, renewed when it has expired: for
// forge.Options.Tokens. A login that cannot be used is said once, and
// pit goes on without it.
func storedToken(ctx context.Context, out *ui.Printer) func(string) string {
	return func(host string) string {
		c, err := auth.Current(ctx, keychain(), nil, host, time.Now())
		if err != nil {
			if !errors.Is(err, auth.ErrNone) {
				out.Warnf("pit's login to %s cannot be used: %v", host, err)
				if hint := errs.Hint(err); hint != "" {
					out.Warnf("%s", hint)
				}
			}
			return ""
		}
		return c.Token
	}
}

// githubLogin is pit's login to github.com, for pit doctor.
func githubLogin() (string, bool) {
	c, err := keychain().Get("github.com")
	return c.User, err == nil
}

func newAuthCmd(opts *globalOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Log pit in to GitHub, GitLab, Gitea, Forgejo or Bitbucket",
		Long: `Log pit in to the service a repository lives on, so that it can read
private pull requests and post comments without gh or a token in the
environment.

The token is kept in the system keychain, never in a file. A token in the
environment (GH_TOKEN, GITLAB_TOKEN, FORGEJO_TOKEN, BITBUCKET_TOKEN, ...)
still wins over it.`,
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(newAuthLoginCmd(opts), newAuthStatusCmd(opts), newAuthLogoutCmd(opts))
	return cmd
}

func newAuthLoginCmd(_ *globalOptions) *cobra.Command {
	var withToken bool
	var clientID string
	cmd := &cobra.Command{
		Use:   "login [host]",
		Short: "Log in to a hosting service",
		Long: `Log in to a hosting service: the one the repository here lives on,
github.com outside one, or the host named.

On github.com and gitlab.com pit shows a code to enter on the service's
page, in any browser where you are logged in. Elsewhere, and with
--with-token, it asks for a token you make on the service's page.`,
		Example: `  pit auth login
  pit auth login codeberg.org
  pit auth login github.example.com --client-id Iv1.0123456789abcdef
  echo "$TOKEN" | pit auth login gitlab.example.com --with-token`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return runAuthLogin(c, args, withToken, clientID)
		},
	}
	cmd.Flags().BoolVar(&withToken, "with-token", false, "read a token from standard input instead of logging in on the service's page")
	cmd.Flags().StringVar(&clientID, "client-id", "", "the ID of an OAuth application of your own on the host, for its device flow")
	return cmd
}

func runAuthLogin(c *cobra.Command, args []string, withToken bool, clientID string) error {
	ctx := c.Context()
	out := ui.New(c.OutOrStdout(), c.ErrOrStderr())
	host := hostOf(ctx, args)
	service, err := serviceOf(ctx, host)
	if err != nil {
		return err
	}
	l := loginFor(service, host, clientID)

	var cred auth.Credential
	if withToken || l.Flow == nil {
		token, err := readToken(c, out, l, withToken)
		if err != nil {
			return err
		}
		user, err := check(ctx, l, token)
		if err != nil {
			return err
		}
		cred = auth.Credential{Token: token, User: user}
	} else {
		code, err := l.Flow.Start(ctx)
		if err != nil {
			return err
		}
		page := code.VerificationURI
		if code.VerificationURIComplete != "" {
			page = code.VerificationURIComplete
		}
		out.Printf("Enter the code %s at %s\n", code.UserCode, page)
		out.Printf("Waiting for it to be entered (Ctrl+C stops) ...\n")
		t, err := l.Flow.Wait(ctx, code)
		if err != nil {
			return err
		}
		user, err := check(ctx, l, t.Access)
		if err != nil {
			return err
		}
		cred = l.Credential(t, user)
	}

	if err := keychain().Set(host, cred); err != nil {
		return err
	}
	who := ""
	if cred.User != "" {
		who = " as " + cred.User
	}
	out.Printf("Logged in to %s%s. The token is in the system keychain.\n", host, who)
	if name := envToken(service, host); name != "" {
		out.Warnf("%s is set, and pit uses it instead of this login while it is", name)
	}
	return nil
}

// hostOf is the host named, or the one the repository here lives on,
// or github.com.
func hostOf(ctx context.Context, args []string) string {
	if len(args) == 1 {
		h := strings.ToLower(strings.TrimSpace(args[0]))
		h = strings.TrimPrefix(strings.TrimPrefix(h, "https://"), "http://")
		h, _, _ = strings.Cut(h, "/")
		return h
	}
	if repo, err := currentRepo(ctx); err == nil && repo.Identity.Host != "" && repo.Identity.Host != forge.LocalHost {
		return repo.Identity.Host
	}
	return "github.com"
}

// serviceOf is what a host runs: by its name, or, for a name that does
// not say, by asking whether it runs Gitea or Forgejo.
func serviceOf(ctx context.Context, host string) (string, error) {
	if s := forge.ServiceOf(host); s != "" {
		return s, nil
	}
	if probeGitea(ctx, host) {
		return forge.GiteaService, nil
	}
	return "", errs.New("cannot tell what %s runs", host).
		WithHint("pit logs in to GitHub, GitLab, Gitea, Forgejo and Bitbucket; it knows a GitHub or GitLab server of a company's own by a name that starts with github. or gitlab.")
}

// readToken reads a token from standard input, or asks for one.
func readToken(c *cobra.Command, out *ui.Printer, l auth.Login, withToken bool) (string, error) {
	in := c.InOrStdin()
	if !withToken {
		if !interactive(c) {
			return "", errs.New("logging in to %s takes a token, and there is nobody to ask for one", l.Host).
				WithHint("pass one on standard input with --with-token")
		}
		out.Printf("Make a token at %s\nwith %s, and paste it here: ", l.TokenPage, l.Needs)
	}
	var token string
	if f, ok := in.(*os.File); ok && !withToken && term.IsTerminal(int(f.Fd())) {
		// Not echoed: it is a password.
		raw, err := term.ReadPassword(int(f.Fd()))
		out.Printf("\n")
		if err != nil {
			return "", errs.Wrap(err, "cannot read the token")
		}
		token = string(raw)
	} else {
		raw, err := io.ReadAll(io.LimitReader(in, 64<<10))
		if err != nil {
			return "", errs.Wrap(err, "cannot read the token")
		}
		token = string(raw)
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return "", errs.New("no token was given").
			WithHint("make one at %s with %s", l.TokenPage, l.Needs)
	}
	return token, nil
}

// check asks the host whose the token is, which is also whether it
// takes it at all.
func check(ctx context.Context, l auth.Login, token string) (string, error) {
	user, err := whoami(ctx, l.Service, l.Host, token)
	switch {
	case forge.Rejected(err):
		return "", errs.Wrap(err, "%s did not accept the token", l.Host).
			WithHint("nothing was stored; make one at %s with %s", l.TokenPage, l.Needs)
	case err != nil:
		return "", errs.Wrap(err, "cannot ask %s whose the token is", l.Host).
			WithHint("nothing was stored; check the network and try again")
	}
	return user, nil
}

// envToken is the variable of the environment that wins over pit's
// login to host, if one is set.
func envToken(service, host string) string {
	var names []string
	switch service {
	case forge.GitHubService:
		names = []string{"GH_TOKEN", "GITHUB_TOKEN"}
		if !strings.EqualFold(host, "github.com") {
			names = []string{"GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN"}
		}
	case forge.GitLabService:
		names = []string{"GITLAB_TOKEN", "GITLAB_ACCESS_TOKEN"}
	case forge.GiteaService:
		names = []string{"FORGEJO_TOKEN", "GITEA_TOKEN"}
	case forge.BitbucketService:
		names = []string{"BITBUCKET_TOKEN", "BITBUCKET_APP_PASSWORD"}
	}
	for _, n := range names {
		if os.Getenv(n) != "" {
			return n
		}
	}
	return ""
}

// loginStatus is a line of pit auth status.
type loginStatus struct {
	Host    string `json:"host"`
	Service string `json:"service"`
	User    string `json:"user,omitempty"`
	OK      bool   `json:"ok"`
	Problem string `json:"problem,omitempty"`
	// Env names the variable that wins over the login.
	Env string `json:"overridden_by,omitempty"`
}

func newAuthStatusCmd(opts *globalOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show which services pit is logged in to, and as whom",
		Long: `Show pit's logins, and ask each service whether it still takes the token.
Exits non-zero when one no longer works.`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return runAuthStatus(c, opts)
		},
	}
}

func runAuthStatus(c *cobra.Command, opts *globalOptions) error {
	ctx := c.Context()
	out := ui.New(c.OutOrStdout(), c.ErrOrStderr())
	store := keychain()
	hosts, err := store.Hosts()
	if err != nil {
		return err
	}
	lines := make([]loginStatus, 0, len(hosts))
	for _, host := range hosts {
		lines = append(lines, statusOf(ctx, store, host))
	}

	if opts.jsonOutput {
		enc := json.NewEncoder(out.Out())
		enc.SetIndent("", "  ")
		if err := enc.Encode(lines); err != nil {
			return err
		}
	} else {
		if len(lines) == 0 {
			out.Printf("pit is not logged in anywhere; `pit auth login` logs it in.\n")
			if gh := ghLoggedIn(ctx); gh != "" {
				out.Printf("For GitHub, pit asks gh, which %s.\n", gh)
			}
			return nil
		}
		w := tabwriter.NewWriter(out.Out(), 0, 0, 2, ' ', 0)
		for _, l := range lines {
			detail := "as " + l.User
			if l.User == "" {
				detail = "with a token of nobody's"
			}
			if !l.OK {
				detail = l.Problem
			}
			if l.Env != "" {
				detail += "; " + l.Env + " is set and is used instead"
			}
			mark := "✓"
			if !l.OK {
				mark = "✗"
			}
			_, _ = fmt.Fprintf(w, "%s\t%s\t%s\n", mark, l.Host, detail)
		}
		if err := w.Flush(); err != nil {
			return err
		}
	}
	for _, l := range lines {
		if !l.OK {
			return errs.New("the login to %s no longer works", l.Host).
				WithHint("run `pit auth login %s` again", l.Host)
		}
	}
	return nil
}

// statusOf asks a host about pit's login to it.
func statusOf(ctx context.Context, store auth.Store, host string) loginStatus {
	s := loginStatus{Host: host, Service: forge.ServiceOf(host)}
	if s.Service == "" {
		// A server of Gitea's or Forgejo's that its name does not
		// give away: pit logs in to no other kind.
		s.Service = forge.GiteaService
	}
	s.Env = envToken(s.Service, host)
	c, err := auth.Current(ctx, store, nil, host, time.Now())
	if err != nil {
		s.Problem = err.Error()
		return s
	}
	user, err := whoami(ctx, s.Service, host, c.Token)
	switch {
	case forge.Rejected(err):
		s.Problem = "the token is no longer taken"
	case err != nil:
		s.Problem = "cannot ask: " + err.Error()
	default:
		s.OK, s.User = true, user
	}
	return s
}

// ghStatus says whether gh is logged in, or "" when there is no gh.
func ghStatus(ctx context.Context) string {
	x := proc.Exec{}
	if _, err := x.Output(ctx, proc.Command{Name: "gh", Args: []string{"--version"}}); err != nil {
		return ""
	}
	if _, err := x.Output(ctx, proc.Command{Name: "gh", Args: []string{"auth", "status"}}); err != nil {
		return "is not logged in either"
	}
	return "is logged in"
}

func newAuthLogoutCmd(_ *globalOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "logout [host]",
		Short: "Remove pit's login to a hosting service from the keychain",
		Long: `Remove pit's login to a hosting service from the keychain: the one the
repository here lives on, github.com outside one, or the host named.

GitLab is also asked to revoke the token. Elsewhere pit cannot, and says
where you can.`,
		Args: cobra.MaximumNArgs(1),
		RunE: runAuthLogout,
	}
}

func runAuthLogout(c *cobra.Command, args []string) error {
	ctx := c.Context()
	out := ui.New(c.OutOrStdout(), c.ErrOrStderr())
	host := hostOf(ctx, args)
	store := keychain()
	cred, err := store.Get(host)
	if errors.Is(err, auth.ErrNone) {
		return errs.New("not logged in to %s", host).
			WithHint("`pit auth status` shows where it is")
	}
	if err != nil {
		return err
	}
	revoked, rerr := auth.Revoke(ctx, nil, cred)
	if err := store.Delete(host); err != nil && !errors.Is(err, auth.ErrNone) {
		return err
	}
	out.Printf("Logged out of %s.\n", host)
	switch {
	case revoked:
		out.Printf("%s has revoked the token.\n", host)
	case rerr != nil:
		out.Warnf("%s would not revoke the token (%v); it works until you revoke it on the service's page", host, rerr)
	default:
		// Not "revoke it": a token pasted in may be gh's too.
		out.Printf("pit no longer has the token; revoke it at %s if nothing else uses it.\n", revokePage(host, cred))
	}
	return nil
}

// revokePage is where a token pit cannot revoke itself is revoked.
func revokePage(host string, c auth.Credential) string {
	base := "https://" + host
	switch forge.ServiceOf(host) {
	case forge.GitHubService:
		if c.ClientID != "" {
			return base + "/settings/applications"
		}
		return base + "/settings/tokens"
	case forge.GitLabService:
		return base + "/-/user_settings/personal_access_tokens"
	case forge.BitbucketService:
		return "https://id.atlassian.com/manage-profile/security/api-tokens"
	}
	return base + "/user/settings/applications"
}
