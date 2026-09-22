package workspace

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/proc"
)

func TestLocalRefStaysOutOfTheUsersNamespace(t *testing.T) {
	ref := LocalRef(482)

	if !strings.HasPrefix(ref, LocalRefPrefix+"/") {
		t.Errorf("LocalRef(482) = %q, want it under %q", ref, LocalRefPrefix)
	}
	// refs/heads is what `git branch` shows; pit must never write there.
	if strings.HasPrefix(ref, "refs/heads/") {
		t.Errorf("LocalRef(482) = %q, which would create a branch", ref)
	}
}

func TestRemotePullRefPerHost(t *testing.T) {
	tests := []struct {
		host string
		want string
	}{
		{"github.com", "refs/pull/482/head"},
		{"gitlab.com", "refs/merge-requests/482/head"},
		{"gitlab.example.org", "refs/merge-requests/482/head"},
		{"github.example.org", "refs/pull/482/head"},
		{LocalHost, "refs/pull/482/head"},
	}

	for _, tt := range tests {
		t.Run(tt.host, func(t *testing.T) {
			if got := RemotePullRef(tt.host, 482); got != tt.want {
				t.Errorf("RemotePullRef(%q, 482) = %q, want %q", tt.host, got, tt.want)
			}
		})
	}
}

func TestFetchRejectsNonsenseNumbers(t *testing.T) {
	r := &stubRunner{}

	for _, pr := range []int{0, -1} {
		if _, err := Fetch(t.Context(), r, Repo{}, pr); err == nil {
			t.Errorf("Fetch(pr=%d) = nil error, want one", pr)
		}
	}
	if len(r.calls) != 0 {
		t.Errorf("made %d git calls for an invalid number, want 0", len(r.calls))
	}
}

func TestFetchUsesTheRightRefspec(t *testing.T) {
	r := &stubRunner{replies: map[string]string{
		"fetch --no-tags --force --quiet origin refs/pull/482/head:refs/pit/482": "",
		"rev-parse --verify refs/pit/482^{commit}":                               "a3f91c2\n",
	}}
	repo := Repo{Root: "/repo", Identity: Identity{Host: "github.com", Owner: "acme", Name: "shop"}}

	sha, err := Fetch(t.Context(), r, repo, 482)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if sha != "a3f91c2" {
		t.Errorf("Fetch returned %q, want the resolved commit", sha)
	}

	args := r.calls[0].Args
	for _, want := range []string{"--force", "--no-tags"} {
		if !slices.Contains(args, want) {
			t.Errorf("fetch args %v are missing %q", args, want)
		}
	}
}

func TestFetchFailureNamesThePullRequest(t *testing.T) {
	r := &stubRunner{fail: map[string]bool{
		"fetch --no-tags --force --quiet origin refs/pull/999/head:refs/pit/999": true,
	}}
	repo := Repo{Root: "/repo", Identity: Identity{Host: "github.com", Owner: "acme", Name: "shop"}}

	_, err := Fetch(t.Context(), r, repo, 999)
	if err == nil {
		t.Fatal("want an error for a pull request that cannot be fetched")
	}
	if !strings.Contains(err.Error(), "#999") {
		t.Errorf("error = %q, want it to name the pull request", err)
	}
	if errs.Hint(err) == "" {
		t.Error("the error carries no hint")
	}
}

// TestFetchLeavesTheRepositoryUntouched is the acceptance criterion for
// T-102: the reviewer's branches, HEAD and working tree must come out
// exactly as they went in.
func TestFetchLeavesTheRepositoryUntouched(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	repo := newTestRepo(t)
	want := repo.PublishPullRequest(7, "feature\n")

	branchesBefore := repo.Branches()
	headBefore := repo.Head()
	statusBefore := repo.Status()

	sha, err := Fetch(t.Context(), proc.Exec{}, repo.Repo, 7)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	if sha != want {
		t.Errorf("Fetch returned %q, want the pull request head %q", sha, want)
	}
	if got := repo.Branches(); !slices.Equal(got, branchesBefore) {
		t.Errorf("branches changed from %v to %v", branchesBefore, got)
	}
	if got := repo.Head(); got != headBefore {
		t.Errorf("HEAD moved from %q to %q", headBefore, got)
	}
	if got := repo.Status(); got != statusBefore {
		t.Errorf("the working tree changed:\nbefore %q\nafter  %q", statusBefore, got)
	}
}

func TestFetchIsRepeatableAfterAForcePush(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	repo := newTestRepo(t)

	repo.PublishPullRequest(7, "first version\n")
	if _, err := Fetch(t.Context(), proc.Exec{}, repo.Repo, 7); err != nil {
		t.Fatalf("first Fetch: %v", err)
	}

	// The author force-pushes: same pull request, unrelated commit.
	second := repo.PublishPullRequest(7, "second version\n")

	got, err := Fetch(t.Context(), proc.Exec{}, repo.Repo, 7)
	if err != nil {
		t.Fatalf("second Fetch: %v", err)
	}
	if got != second {
		t.Errorf("Fetch returned %q, want the new head %q", got, second)
	}
}

func TestDeleteRefRemovesWhatFetchCreated(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	repo := newTestRepo(t)
	repo.PublishPullRequest(7, "feature\n")

	if _, err := Fetch(t.Context(), proc.Exec{}, repo.Repo, 7); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if err := DeleteRef(t.Context(), proc.Exec{}, repo.Repo, 7); err != nil {
		t.Fatalf("DeleteRef: %v", err)
	}

	if _, err := ResolveRef(t.Context(), proc.Exec{}, repo.Repo.Root, LocalRef(7)); err == nil {
		t.Error("the ref still resolves after DeleteRef")
	}
}

func TestDeleteRefOnSomethingNeverFetched(t *testing.T) {
	// Cleanup runs on paths where the fetch may never have happened, so
	// this has to be harmless rather than an error to guard against.
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	repo := newTestRepo(t)

	if err := DeleteRef(t.Context(), proc.Exec{}, repo.Repo, 404); err != nil {
		t.Errorf("DeleteRef for a ref that was never fetched: %v", err)
	}
}

func TestResolveRefReportsUnknownRefs(t *testing.T) {
	r := &stubRunner{fail: map[string]bool{"rev-parse --verify refs/pit/1^{commit}": true}}

	_, err := ResolveRef(context.Background(), r, "/repo", LocalRef(1))
	if err == nil {
		t.Fatal("want an error for a ref that does not exist")
	}
	var target *errs.Error
	if !errors.As(err, &target) {
		t.Error("the error is not one of ours")
	}
}
