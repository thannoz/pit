package forge

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/proc"
)

func TestForPicksGitHub(t *testing.T) {
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
	for _, host := range []string{"gitlab.com", "git.example.org", LocalHost, ""} {
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
			wantMsg:  "no account",
			wantHint: "gh auth login",
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
