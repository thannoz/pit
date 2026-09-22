package forge

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/proc"
)

// stubGH answers a gh invocation from a table.
type stubGH struct {
	out   string
	err   error
	calls []proc.Command
}

func (s *stubGH) Output(_ context.Context, c proc.Command) ([]byte, error) {
	s.calls = append(s.calls, c)
	if s.err != nil {
		return nil, s.err
	}
	return []byte(s.out), nil
}

// realResponse is the shape gh actually returns, taken from a live
// call so the parser is tested against the thing rather than against
// an idea of it.
const realResponse = `{
  "author": {"id": "MDQ6VXNlcjIwODk3NDM=", "is_bot": false, "login": "andyfeller", "name": "Andy Feller"},
  "baseRefName": "trunk",
  "baseRefOid": "28c4d3075b5916acdb6974a7e4f5c49d2c274a86",
  "headRefName": "andyfeller/flag-level-disableauth",
  "headRefOid": "cc36d32a212a2b8b6611fb73549fe6d04fb6ec38",
  "isDraft": false,
  "number": 9000,
  "state": "MERGED",
  "title": "proof of concept for flag-level disable auth check",
  "url": "https://github.com/cli/cli/pull/9000"
}`

func TestPullRequestParsesGitHubsShape(t *testing.T) {
	s := &stubGH{out: realResponse}

	pr, err := GitHub{Runner: s, Repo: "cli/cli"}.PullRequest(t.Context(), 9000)
	if err != nil {
		t.Fatalf("PullRequest: %v", err)
	}

	tests := []struct {
		field string
		got   any
		want  any
	}{
		{"Number", pr.Number, 9000},
		{"Title", pr.Title, "proof of concept for flag-level disable auth check"},
		// gh nests the author; pit does not.
		{"Author", pr.Author, "andyfeller"},
		{"Branch", pr.Branch, "andyfeller/flag-level-disableauth"},
		{"HeadSHA", pr.HeadSHA, "cc36d32a212a2b8b6611fb73549fe6d04fb6ec38"},
		{"BaseSHA", pr.BaseSHA, "28c4d3075b5916acdb6974a7e4f5c49d2c274a86"},
		{"BaseBranch", pr.BaseBranch, "trunk"},
		// GitHub shouts its states; pit does not.
		{"State", pr.State, Merged},
		{"Draft", pr.Draft, false},
	}

	for _, tt := range tests {
		t.Run(tt.field, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("%s = %v, want %v", tt.field, tt.got, tt.want)
			}
		})
	}
}

func TestPullRequestNamesTheRepository(t *testing.T) {
	// Without --repo, gh guesses from the working directory. pit may
	// be running in a worktree, or in a clone whose default remote is
	// not the one under review.
	s := &stubGH{out: realResponse}

	if _, err := (GitHub{Runner: s, Repo: "cli/cli"}).PullRequest(t.Context(), 9000); err != nil {
		t.Fatalf("PullRequest: %v", err)
	}

	args := strings.Join(s.calls[0].Args, " ")
	if !strings.Contains(args, "--repo cli/cli") {
		t.Errorf("args %q do not name the repository", args)
	}
	for _, want := range ghFields {
		if !strings.Contains(args, want) {
			t.Errorf("args %q do not ask for %q", args, want)
		}
	}
}

func TestPullRequestRejectsNonsenseNumbers(t *testing.T) {
	s := &stubGH{}

	for _, n := range []int{0, -1} {
		if _, err := (GitHub{Runner: s}).PullRequest(t.Context(), n); err == nil {
			t.Errorf("PullRequest(%d) = nil error, want one", n)
		}
	}
	if len(s.calls) != 0 {
		t.Errorf("gh was called %d times for an invalid number, want 0", len(s.calls))
	}
}

func TestFailuresAreTranslated(t *testing.T) {
	tests := []struct {
		name     string
		ghSays   string
		wantMsg  string
		wantHint string
	}{
		{
			name:     "no such pull request",
			ghSays:   `GraphQL: Could not resolve to a PullRequest with the number of 99999999.`,
			wantMsg:  "has no pull request #99999999",
			wantHint: "check the number",
		},
		{
			name:     "not logged in",
			ghSays:   `To get started with GitHub CLI, please run: gh auth login`,
			wantMsg:  "not logged in",
			wantHint: "gh auth login",
		},
		{
			name:     "gh is missing",
			ghSays:   `gh is not installed or not on PATH`,
			wantMsg:  "GitHub CLI",
			wantHint: "cli.github.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &stubGH{err: errors.New(tt.ghSays)}

			_, err := GitHub{Runner: s, Repo: "acme/shop"}.PullRequest(t.Context(), 99999999)
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

func TestUnexpectedFailureKeepsGitHubsOwnWords(t *testing.T) {
	// Translating only the cases that actually happen means anything
	// else still reaches the user intact rather than as "something
	// went wrong".
	s := &stubGH{err: errors.New("HTTP 503: the service is having a bad day")}

	_, err := GitHub{Runner: s, Repo: "acme/shop"}.PullRequest(t.Context(), 1)
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "having a bad day") {
		t.Errorf("message = %q, want gh's own words kept", err)
	}
}

func TestBrokenJSONIsReported(t *testing.T) {
	s := &stubGH{out: `{"number": `}

	if _, err := (GitHub{Runner: s}).PullRequest(t.Context(), 1); err == nil {
		t.Fatal("want an error for a truncated response")
	}
}

func TestDescribe(t *testing.T) {
	tests := []struct {
		name string
		pr   PR
		want string
	}{
		{"open", PR{Number: 482, Title: "Rework checkout", Author: "lisa", State: Open}, `#482 "Rework checkout" by @lisa`},
		{"draft", PR{Number: 1, Title: "WIP", Author: "sam", State: Open, Draft: true}, `#1 "WIP" by @sam (draft)`},
		{"merged", PR{Number: 9, Title: "Done", Author: "sam", State: Merged}, `#9 "Done" by @sam (merged)`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.pr.Describe(); got != tt.want {
				t.Errorf("Describe() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestAgainstRealGitHub is the acceptance criterion for T-305. It needs
// the network and a logged-in gh, so it is skipped when either is
// missing rather than failing the suite.
func TestAgainstRealGitHub(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: talks to github.com")
	}
	if _, err := exec.LookPath("gh"); err != nil {
		t.Skip("skipping: gh is not installed")
	}
	if _, err := (proc.Exec{}).Output(t.Context(), proc.Command{Name: "gh", Args: []string{"auth", "status"}}); err != nil {
		t.Skip("skipping: gh is not logged in")
	}

	pr, err := GitHub{Runner: proc.Exec{}, Repo: "cli/cli"}.PullRequest(t.Context(), 9000)
	if err != nil {
		t.Fatalf("PullRequest: %v", err)
	}

	// Pinned against a merged pull request, so the values cannot drift.
	if pr.Number != 9000 {
		t.Errorf("Number = %d, want 9000", pr.Number)
	}
	if pr.Author != "andyfeller" {
		t.Errorf("Author = %q, want andyfeller", pr.Author)
	}
	if pr.HeadSHA != "cc36d32a212a2b8b6611fb73549fe6d04fb6ec38" {
		t.Errorf("HeadSHA = %q", pr.HeadSHA)
	}
	if pr.State != Merged {
		t.Errorf("State = %q, want merged", pr.State)
	}
}
