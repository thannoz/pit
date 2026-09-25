package forge

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/proc"
)

const commentURL = "https://github.com/acme/shop/pull/482#issuecomment-2210987654"

func TestCommentGoesThroughGH(t *testing.T) {
	s := &stubGH{out: commentURL + "\n"}
	body := "### Review notes\n\nOne thing I found.\n"
	url, err := GitHub{Runner: s, Repo: "acme/shop"}.Comment(t.Context(), 482, body)
	if err != nil {
		t.Fatal(err)
	}
	if url != commentURL {
		t.Errorf("url = %q", url)
	}
	if len(s.calls) != 1 {
		t.Fatalf("calls = %v", s.calls)
	}
	c := s.calls[0]
	if got := strings.Join(c.Args, " "); c.Name != "gh" || got != "pr comment 482 --body-file - --repo acme/shop" {
		t.Errorf("ran %s %s", c.Name, got)
	}
	if c.Stdin == nil {
		t.Fatal("the body was not given")
	}
	if got, _ := io.ReadAll(c.Stdin); string(got) != body {
		t.Errorf("gh read %q", got)
	}
}

// Without a repository gh finds it from the working directory.
func TestCommentWithoutARepository(t *testing.T) {
	s := &stubGH{out: "\nsome notice\n" + commentURL + "\n"}
	url, err := GitHub{Runner: s}.Comment(t.Context(), 482, "hello")
	if err != nil || url != commentURL || strings.Contains(strings.Join(s.calls[0].Args, " "), "--repo") {
		t.Errorf("url %q, err %v, args %v", url, err, s.calls[0].Args)
	}
}

// What cannot be posted is refused before gh is asked.
func TestCommentsThatAreNotSent(t *testing.T) {
	for _, tc := range []struct {
		name   string
		number int
		body   string
		want   string
	}{
		{"empty", 482, " \n", "nothing to say"},
		{"not a number", 0, "hello", "0 is not a pull request number"},
		{"too long", 482, strings.Repeat("ü", MaxComment+1), "65537 characters long; GitHub takes at most 65536"},
	} {
		s := &stubGH{}
		_, err := GitHub{Runner: s, Repo: "acme/shop"}.Comment(t.Context(), tc.number, tc.body)
		if err == nil || !strings.Contains(err.Error(), tc.want) || len(s.calls) != 0 {
			t.Errorf("%s: err = %v, calls %d", tc.name, err, len(s.calls))
		}
	}
	// As long as GitHub takes is not too long.
	s := &stubGH{out: commentURL}
	if _, err := (GitHub{Runner: s}).Comment(t.Context(), 482, strings.Repeat("ü", MaxComment)); err != nil {
		t.Errorf("a comment of the longest length: %v", err)
	}
}

func TestCommentFailures(t *testing.T) {
	for _, tc := range []struct {
		gh, want, hint string
	}{
		{"GraphQL: Could not resolve to a PullRequest with the number of 482. (repository.pullRequest)", "acme/shop has no pull request #482", "check the number"},
		{"To get started with GitHub CLI, please run:  gh auth login", "gh is not logged in", "gh auth login"},
		{"GraphQL: Unable to create comment because issue is locked. (addComment)", "the conversation on #482 is locked", "write access"},
		{"GraphQL: Resource not accessible by integration (addComment)", "gh may not comment on #482", "gh auth status"},
		{"something else entirely", "cannot comment on pull request #482", ""},
	} {
		s := &stubGH{err: errors.New(tc.gh)}
		_, err := GitHub{Runner: s, Repo: "acme/shop"}.Comment(t.Context(), 482, "hello")
		if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(errs.Hint(err), tc.hint) {
			t.Errorf("%q: err = %v, hint %q", tc.gh, err, errs.Hint(err))
		}
	}
}

// TestCommentDryRun runs Comment against a gh that only writes down
// what it was given: the real process, the real standard input, and no
// comment anywhere.
func TestCommentDryRun(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in gh is a shell script")
	}
	dir := t.TempDir()
	script := `#!/bin/sh
printf '%s\n' "$@" > "` + dir + `/args"
cat > "` + dir + `/body"
echo "` + commentURL + `"
`
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	body := "### Review notes\n\n- **Failed request:** `GET /api/cart` answered 500\n"
	url, err := GitHub{Runner: proc.Exec{}, Repo: "acme/shop"}.Comment(t.Context(), 482, body)
	if err != nil || url != commentURL {
		t.Fatalf("url %q, err %v", url, err)
	}
	args, _ := os.ReadFile(filepath.Join(dir, "args"))
	if string(args) != "pr\ncomment\n482\n--body-file\n-\n--repo\nacme/shop\n" {
		t.Errorf("gh was run with %q", args)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "body")); string(got) != body {
		t.Errorf("gh read %q", got)
	}
}
