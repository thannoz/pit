package forgetest_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/thannoz/pit/internal/forge"
	"github.com/thannoz/pit/internal/forge/forgetest"
)

// review is what a command will do with a pull request: read it, and
// refuse politely when it is not worth reviewing.
func review(ctx context.Context, f forge.Forge, number int) (forge.PR, error) {
	pr, err := f.PullRequest(ctx, number)
	if err != nil {
		return forge.PR{}, err
	}
	if pr.State != forge.Open {
		return pr, errors.New("#" + pr.Describe() + " is not open")
	}
	return pr, nil
}

func TestFakeAnswersLikeAForge(t *testing.T) {
	f := forgetest.New()

	pr, err := review(t.Context(), f, 482)
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if pr.Author != "lisa" || pr.HeadSHA == "" {
		t.Errorf("PR = %+v, want a complete pull request", pr)
	}
	if got := f.Asked(); !slices.Equal(got, []int{482}) {
		t.Errorf("Asked() = %v, want [482]", got)
	}
}

func TestFakeCanBeMerged(t *testing.T) {
	// The Fake has to be able to be wrong in the ways that matter, or
	// the code above it is only ever tested on its happy path.
	f := forgetest.New(forge.PR{Number: 9, Title: "Done", Author: "sam", State: forge.Merged})

	if _, err := review(t.Context(), f, 9); err == nil {
		t.Error("a merged pull request was accepted for review")
	}
}

func TestFakeReportsUnknownNumbers(t *testing.T) {
	f := forgetest.New()

	if _, err := f.PullRequest(t.Context(), 404); err == nil {
		t.Error("an unknown pull request was answered")
	}
}

func TestFakeCanFailOutright(t *testing.T) {
	boom := errors.New("the service is unreachable")
	f := forgetest.New()
	f.Err = boom

	if _, err := f.PullRequest(t.Context(), 482); !errors.Is(err, boom) {
		t.Errorf("err = %v, want the scripted failure", err)
	}
}

func TestFakeTakesComments(t *testing.T) {
	f := forgetest.New()
	url, err := f.Comment(t.Context(), 482, "hello")
	if err != nil || url != "https://github.com/acme/shop/pull/482#issuecomment-1" {
		t.Errorf("url %q, err %v", url, err)
	}
	if _, err := f.Comment(t.Context(), 7, "hello"); err == nil {
		t.Error("a comment on a pull request the fake does not know")
	}
	if got := f.Comments(); len(got) != 1 || got[0] != (forgetest.Comment{Number: 482, Body: "hello"}) {
		t.Errorf("comments = %+v", got)
	}
}
