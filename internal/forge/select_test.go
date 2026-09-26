package forge

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/proc"
)

// noTokens clears the environment's tokens, which would otherwise
// decide what a test on a developer's machine gets.
func noTokens(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN",
		"GITLAB_TOKEN", "GITLAB_ACCESS_TOKEN", "FORGEJO_TOKEN", "GITEA_TOKEN",
		"BITBUCKET_TOKEN", "BITBUCKET_USERNAME", "BITBUCKET_APP_PASSWORD",
	} {
		t.Setenv(name, "")
	}
}

func TestForPicksGitHub(t *testing.T) {
	noTokens(t)
	for _, host := range []string{"github.com", "GitHub.com", "github.acme-corp.net"} {
		t.Run(host, func(t *testing.T) {
			f, err := For(Options{Host: host, Repo: "acme/shop", Runner: &stubGH{}, Resolver: stubResolver{}})
			if err != nil {
				t.Fatalf("For(%q): %v", host, err)
			}
			gh, ok := f.(GitHub)
			if !ok {
				t.Fatalf("For(%q) returned %T, want GitHub", host, f)
			}
			if gh.Repo != "acme/shop" {
				t.Errorf("Repo = %q, want acme/shop", gh.Repo)
			}
		})
	}
}

// TestForFallsBackToGit records the change T-316 made: every host pit
// does not recognise used to be refused outright, which blocked the
// tool on most of the world's repositories and on testing pit against
// a local one. Reading the commit is less than a service can offer,
// but it is not nothing.
func TestForFallsBackToGit(t *testing.T) {
	for _, host := range []string{LocalHost, ""} {
		t.Run(host, func(t *testing.T) {
			f, err := For(Options{Host: host, Repo: "team/tool", Runner: &stubGH{}, Resolver: stubResolver{}})
			if err != nil {
				t.Fatalf("For(%q): %v", host, err)
			}
			if _, ok := f.(Git); !ok {
				t.Errorf("For(%q) returned %T, want Git", host, f)
			}
		})
	}
	// A server with a name of its own is asked what it runs first, and
	// read by its commit if it is nothing pit knows.
	f, err := For(Options{Host: "git.example.org", Repo: "team/tool", Runner: &stubGH{}, Resolver: stubResolver{}})
	p, ok := f.(Probing)
	if err != nil || !ok || p.Gitea.Host != "git.example.org" || p.Gitea.Repo != "team/tool" {
		t.Fatalf("%#v, %v", f, err)
	}
	if _, ok := p.Git.(Git); !ok {
		t.Errorf("git = %T", p.Git)
	}
}

func TestForPicksTheOtherForges(t *testing.T) {
	t.Setenv("FORGEJO_TOKEN", "fj")
	t.Setenv("BITBUCKET_TOKEN", "bb")
	for host, want := range map[string]string{
		"codeberg.org": "Gitea", "gitea.com": "Gitea", "gitea.acme.net": "Gitea", "forgejo.acme.net": "Gitea",
		"Codeberg.org": "Gitea", "bitbucket.org": "Bitbucket", "gitlab.com": "GitLab",
	} {
		f, err := For(Options{Host: host, Repo: "team/tool", Runner: &stubGH{}})
		if err != nil {
			t.Fatalf("%s: %v", host, err)
		}
		switch got := f.(type) {
		case Gitea:
			if want != "Gitea" || got.Host != host || got.Repo != "team/tool" || got.Token != "fj" {
				t.Errorf("%s: %#v", host, got)
			}
		case Bitbucket:
			if want != "Bitbucket" || got.Repo != "team/tool" || got.Token != "bb" {
				t.Errorf("%s: %#v", host, got)
			}
		case GitLab:
			if want != "GitLab" {
				t.Errorf("%s: %#v", host, got)
			}
		default:
			t.Errorf("%s: %T", host, f)
		}
	}
	// Without a resolver, a local repository cannot be read at all.
	if _, err := For(Options{Host: LocalHost, Runner: &stubGH{}}); err == nil {
		t.Error("a local repository without a resolver")
	}
}

// A token, of the environment or of pit's login, is asked with
// through the API; without one, gh is asked. The environment wins.
func TestForUsesATokenForGitHub(t *testing.T) {
	noTokens(t)
	asked := []string{}
	stored := map[string]string{"github.com": "gho_login", "github.acme.net": "ghe_login", "gitlab.com": "gl_login",
		"codeberg.org": "fj_login", "bitbucket.org": "me@example.com:bb_login", "git.example.org": "probe_login"}
	tokens := func(host string) string { asked = append(asked, host); return stored[host] }
	f, err := For(Options{Host: "github.com", Repo: "acme/shop", Runner: &stubGH{}, Tokens: tokens})
	if api, ok := f.(GitHubAPI); err != nil || !ok || api.Token != "gho_login" || api.Host != "github.com" || api.Repo != "acme/shop" {
		t.Fatalf("%#v, %v", f, err)
	}
	f, _ = For(Options{Host: "github.acme.net", Repo: "acme/shop", Runner: &stubGH{}, Tokens: tokens})
	if api, ok := f.(GitHubAPI); !ok || api.Token != "ghe_login" {
		t.Errorf("%#v", f)
	}
	f, _ = For(Options{Host: "gitlab.com", Repo: "a/b", Runner: &stubGH{}, Tokens: tokens})
	if gl, ok := f.(GitLab); !ok || gl.Token != "gl_login" {
		t.Errorf("%#v", f)
	}
	f, _ = For(Options{Host: "codeberg.org", Repo: "a/b", Runner: &stubGH{}, Tokens: tokens})
	if g, ok := f.(Gitea); !ok || g.Token != "fj_login" {
		t.Errorf("%#v", f)
	}
	f, _ = For(Options{Host: "git.example.org", Repo: "a/b", Runner: &stubGH{}, Tokens: tokens})
	if p, ok := f.(Probing); !ok || p.Gitea.Token != "probe_login" {
		t.Errorf("%#v", f)
	}
	// Bitbucket's "email:token" is Basic authentication, a token alone
	// a bearer's.
	f, _ = For(Options{Host: "bitbucket.org", Repo: "a/b", Runner: &stubGH{}, Tokens: tokens})
	if b, ok := f.(Bitbucket); !ok || b.Username != "me@example.com" || b.Password != "bb_login" || b.Token != "" {
		t.Errorf("%#v", f)
	}
	stored["bitbucket.org"] = "access"
	f, _ = For(Options{Host: "bitbucket.org", Repo: "a/b", Runner: &stubGH{}, Tokens: tokens})
	if b, ok := f.(Bitbucket); !ok || b.Token != "access" || b.Username != "" {
		t.Errorf("%#v", f)
	}

	// Nothing is looked up where there is nothing to look up for.
	asked = nil
	if _, err := For(Options{Host: LocalHost, Runner: &stubGH{}, Resolver: stubResolver{}, Tokens: tokens}); err != nil || len(asked) != 0 {
		t.Errorf("asked %q, %v", asked, err)
	}

	// The environment wins, and then the login is not even read.
	t.Setenv("GH_TOKEN", "env")
	t.Setenv("GITLAB_TOKEN", "glenv")
	t.Setenv("BITBUCKET_TOKEN", "bbenv")
	for host, want := range map[string]string{"github.com": "env", "gitlab.com": "glenv", "bitbucket.org": "bbenv"} {
		f, _ = For(Options{Host: host, Repo: "a/b", Runner: &stubGH{}, Tokens: tokens})
		var got string
		switch v := f.(type) {
		case GitHubAPI:
			got = v.Token
		case GitLab:
			got = v.Token
		case Bitbucket:
			got = v.Token
		}
		if got != want {
			t.Errorf("%s: %#v", host, f)
		}
	}
	if len(asked) != 0 {
		t.Errorf("asked %q", asked)
	}

	// No token at all is gh.
	noTokens(t)
	f, _ = For(Options{Host: "github.com", Repo: "acme/shop", Runner: &stubGH{}, Tokens: func(string) string { return "" }})
	if _, ok := f.(GitHub); !ok {
		t.Errorf("%#v", f)
	}
}

func TestServiceOf(t *testing.T) {
	for host, want := range map[string]string{
		"github.com": GitHubService, "github.acme.net": GitHubService, "gitlab.com": GitLabService,
		"codeberg.org": GiteaService, "bitbucket.org": BitbucketService, "git.example.org": "", LocalHost: "",
	} {
		if got := ServiceOf(host); got != want {
			t.Errorf("%s: %q, want %q", host, got, want)
		}
	}
	if _, err := User(t.Context(), "", "git.example.org", "t"); err == nil {
		t.Error("asked a host whose service is not known")
	}
	// Each is asked as the host it is, with the token as it takes it.
	for _, tc := range []struct {
		service, host, token string
		want                 account
	}{
		{GitHubService, "github.acme.net", "t", GitHubAPI{Host: "github.acme.net", Token: "t"}},
		{GitLabService, "gitlab.com", "t", GitLab{Host: "gitlab.com", Token: "t"}},
		{GiteaService, "codeberg.org", "t", Gitea{Host: "codeberg.org", Token: "t"}},
		{BitbucketService, "bitbucket.org", "me@example.com:t", Bitbucket{Username: "me@example.com", Password: "t"}},
		{BitbucketService, "bitbucket.org", "t", Bitbucket{Token: "t"}},
	} {
		if got, err := accountOn(tc.service, tc.host, tc.token); err != nil || got != tc.want {
			t.Errorf("%s %s: %#v, %v", tc.service, tc.token, got, err)
		}
	}
}

// Only a 401 says the token is not taken; anything else is another
// failure.
func TestRejected(t *testing.T) {
	for code, want := range map[int]bool{401: true, 403: false, 404: false, 500: false} {
		if got := Rejected(status{service: "GitHub", code: code}); got != want {
			t.Errorf("%d: %v", code, got)
		}
	}
	if Rejected(errors.New("connection refused")) || Rejected(nil) {
		t.Error("not an answer at all")
	}
}

func TestForNeedsAWayToRunCommands(t *testing.T) {
	if _, err := For(Options{Host: "github.com"}); err == nil {
		t.Error("want an error without a runner")
	}
}

// failOn returns a Runner that fails for one gh subcommand.
type failOn struct {
	subcommand string
	err        error
}

func (f failOn) Output(_ context.Context, c proc.Command) ([]byte, error) {
	if len(c.Args) > 0 && c.Args[0] == f.subcommand {
		return nil, f.err
	}
	return []byte("ok"), nil
}

// TestCheckFindsTheTwoUsualProblems covers the other two cases: gh is
// missing, and gh is present but has no account.
func TestCheckFindsTheTwoUsualProblems(t *testing.T) {
	tests := []struct {
		name       string
		runner     Runner
		wantMsg    string
		wantHint   string
		wantNoDupe string
	}{
		{
			name:     "gh is not installed",
			runner:   failOn{subcommand: "--version", err: errors.New(`exec: "gh": executable file not found in $PATH`)},
			wantMsg:  "GitHub CLI (gh)",
			wantHint: "cli.github.com",
		},
		{
			name:     "gh has no account",
			runner:   failOn{subcommand: "auth", err: errors.New("You are not logged into any GitHub hosts")},
			wantMsg:  "neither pit nor the GitHub CLI is logged in",
			wantHint: "pit auth login",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := GitHub{Runner: tt.runner}.Check(t.Context())
			if err == nil {
				t.Fatal("want an error")
			}
			if !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("message = %q, want it to contain %q", err, tt.wantMsg)
			}
			if !strings.Contains(errs.Hint(err), tt.wantHint) {
				t.Errorf("hint = %q, want it to contain %q", errs.Hint(err), tt.wantHint)
			}
		})
	}
}

func TestCheckPassesWhenGhIsUsable(t *testing.T) {
	if err := (GitHub{Runner: &stubGH{out: "gh version 2.98.0"}}).Check(t.Context()); err != nil {
		t.Errorf("Check: %v", err)
	}
}

// TestCheckHappensBeforeAnythingIsCreated records why Check exists at
// all: without it the first failure would arrive after a worktree had
// been made and a build started.
func TestCheckHappensBeforeAnythingIsCreated(t *testing.T) {
	s := &stubGH{}

	if err := (GitHub{Runner: s}).Check(t.Context()); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(s.calls) != 2 {
		t.Fatalf("Check made %d calls, want 2", len(s.calls))
	}
	for i, want := range []string{"--version", "auth"} {
		if s.calls[i].Args[0] != want {
			t.Errorf("call %d was %v, want it to start with %q", i+1, s.calls[i].Args, want)
		}
	}
}

// GitLab is asked where the repository is on GitLab: gitlab.com, or an
// instance named the way they usually are.
func TestForPicksGitLab(t *testing.T) {
	t.Setenv("GITLAB_TOKEN", "glpat-secret")
	for _, host := range []string{"gitlab.com", "gitlab.example.com", "GitLab.com"} {
		f, err := For(Options{Host: host, Repo: "group/sub/tool", Runner: &stubGH{}, Resolver: stubResolver{}})
		if err != nil {
			t.Fatal(err)
		}
		g, ok := f.(GitLab)
		if !ok || g.Host != host || g.Project != "group/sub/tool" || g.Token != "glpat-secret" {
			t.Errorf("For(%q) = %#v", host, f)
		}
	}
}
