package forge

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/proc"
)

// stubResolver stands in for the fetch.
type stubResolver struct {
	sha string
	err error
}

func (s stubResolver) FetchPullRequest(context.Context, int) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	if s.sha == "" {
		return "a3f91c2e4b7d8091a2b3c4d5e6f708192a3b4c5d", nil
	}
	return s.sha, nil
}

// stubGit answers `git show`.
type stubGit struct {
	out   string
	err   error
	calls []proc.Command
}

func (s *stubGit) Output(_ context.Context, c proc.Command) ([]byte, error) {
	s.calls = append(s.calls, c)
	if s.err != nil {
		return nil, s.err
	}
	return []byte(s.out), nil
}

func TestGitReadsTheCommit(t *testing.T) {
	g := Git{
		Runner:   &stubGit{out: "Rework the checkout flow\x1fLisa Meyer\n"},
		Resolver: stubResolver{},
		Dir:      "/repo",
	}

	pr, err := g.PullRequest(t.Context(), 482)
	if err != nil {
		t.Fatalf("PullRequest: %v", err)
	}

	if pr.Number != 482 {
		t.Errorf("Number = %d, want 482", pr.Number)
	}
	if pr.Title != "Rework the checkout flow" {
		t.Errorf("Title = %q", pr.Title)
	}
	if pr.Author != "Lisa Meyer" {
		t.Errorf("Author = %q", pr.Author)
	}
	if pr.HeadSHA == "" {
		t.Error("HeadSHA is empty")
	}
	// The caller has to be able to tell that this is less than a
	// service would have told it.
	if !pr.Limited {
		t.Error("Limited is false although the metadata came from a commit")
	}
}

func TestGitDescribesWithoutClaimingAnAccount(t *testing.T) {
	// A commit records a person's name. Printing it with an @ would
	// claim a login that may not exist.
	limited := PR{Number: 482, Title: "A change", Author: "Lisa Meyer", Limited: true}
	service := PR{Number: 482, Title: "A change", Author: "lisa", State: Open}

	if got := limited.Describe(); strings.Contains(got, "@") {
		t.Errorf("Describe() = %q, want no @ for a commit author", got)
	}
	if got := service.Describe(); !strings.Contains(got, "@lisa") {
		t.Errorf("Describe() = %q, want @lisa for a service login", got)
	}
}

func TestGitAsksForTheRightFormat(t *testing.T) {
	s := &stubGit{out: "subject\x1fauthor"}

	if _, err := (Git{Runner: s, Resolver: stubResolver{}, Dir: "/repo"}).PullRequest(t.Context(), 1); err != nil {
		t.Fatalf("PullRequest: %v", err)
	}

	args := strings.Join(s.calls[0].Args, " ")
	if !strings.Contains(args, "--no-patch") {
		t.Errorf("args %q fetch the whole diff", args)
	}
	if s.calls[0].Dir != "/repo" {
		t.Errorf("Dir = %q, want the repository", s.calls[0].Dir)
	}
}

func TestGitReportsAFetchFailure(t *testing.T) {
	boom := errors.New("couldn't find remote ref")
	g := Git{Runner: &stubGit{}, Resolver: stubResolver{err: boom}}

	if _, err := g.PullRequest(t.Context(), 482); !errors.Is(err, boom) {
		t.Errorf("err = %v, want the fetch failure", err)
	}
}

func TestGitRejectsNonsenseNumbers(t *testing.T) {
	g := Git{Runner: &stubGit{}, Resolver: stubResolver{}}

	for _, n := range []int{0, -1} {
		if _, err := g.PullRequest(t.Context(), n); err == nil {
			t.Errorf("PullRequest(%d) = nil error, want one", n)
		}
	}
}

func TestGitReportsAnUnreadableCommit(t *testing.T) {
	g := Git{Runner: &stubGit{err: errors.New("bad object")}, Resolver: stubResolver{}}

	err := errs.Hint(mustFail(t, g))
	_ = err // the message matters more than the hint here
}

func mustFail(t *testing.T, g Git) error {
	t.Helper()

	_, err := g.PullRequest(t.Context(), 1)
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "cannot read the commit") {
		t.Errorf("error = %q, want it to say what it could not read", err)
	}
	return err
}

func TestDescribeWithoutATitle(t *testing.T) {
	// A commit with an empty subject is unusual but possible.
	pr := PR{Number: 482}
	if got := pr.Describe(); got != "#482" {
		t.Errorf("Describe() = %q, want %q", got, "#482")
	}
}
