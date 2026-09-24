package cli

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/sandbox"
	"github.com/thannoz/pit/internal/snapshot"
)

// snapAt saves a snapshot into the store of repository ref as if it
// had been saved age ago.
func snapAt(t *testing.T, m *sandbox.Manager, ref, repo, name string, pr int, age time.Duration) snapshot.Snapshot {
	t.Helper()
	at := time.Now().Add(-age)
	st := snapshot.Store{Dir: filepath.Join(m.StateDir, "snapshots", ref), Now: func() time.Time { return at }}
	snap, err := st.Save(t.Context(), snapshot.Snapshot{Name: name, Repo: repo, PR: pr, SHA: "a3f91c2e4b7d"},
		func(_ context.Context, w io.Writer) error {
			_, err := io.WriteString(w, "-- dump of "+name+"\n")
			return err
		})
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

func TestSnapLsWithoutSnapshots(t *testing.T) {
	withManager(t)
	out, _, err := run(t, "snap", "ls")
	if err != nil || !strings.Contains(out, "No snapshots") || !strings.Contains(out, "pit snap save") {
		t.Errorf("%v\n%s", err, out)
	}
}

// The acceptance criterion's other half: name, size, age and pull
// request, newest first.
func TestSnapLsListsNameSizeAgeAndPullRequest(t *testing.T) {
	m, _ := withManager(t)
	old := snapAt(t, m, "acme-shop-c56680", "github.com/acme/shop", "cart", 482, 45*24*time.Hour)
	fresh := snapAt(t, m, "acme-shop-c56680", "github.com/acme/shop", "", 519, time.Hour)

	out, _, err := run(t, "snap", "ls")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 || strings.Fields(lines[0])[0] != "ID" || strings.Contains(lines[0], "REPO") {
		t.Fatalf("listing:\n%s", out)
	}
	for i, want := range [][]string{
		{fresh.ID, "-", "#519", "B", "1h"},
		{old.ID, "cart", "#482", "B", "45d"},
	} {
		for _, w := range want {
			if !strings.Contains(lines[i+1], w) {
				t.Errorf("line %d lacks %q: %q", i+1, w, lines[i+1])
			}
		}
	}
}

func TestSnapLsNamesTheRepositoryWhenThereAreSeveral(t *testing.T) {
	m, _ := withManager(t)
	snapAt(t, m, "acme-shop-c56680", "github.com/acme/shop", "cart", 482, time.Hour)
	snapAt(t, m, "acme-blog-0a1b2c", "github.com/acme/blog", "draft", 7, time.Hour)

	out, _, err := run(t, "snap", "ls")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "REPO") || !strings.Contains(out, "shop") || !strings.Contains(out, "blog") {
		t.Errorf("listing:\n%s", out)
	}

	out, _, err = run(t, "--json", "snap", "ls")
	if err != nil || !strings.Contains(out, `"repo": "github.com/acme/blog"`) || !strings.Contains(out, `"name": "cart"`) {
		t.Errorf("--json: %v\n%s", err, out)
	}
}

func storedIDs(t *testing.T, m *sandbox.Manager) []string {
	t.Helper()
	snaps, err := allSnapshots(m)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, l := range snaps {
		ids = append(ids, l.ID)
	}
	return ids
}

// TestSnapRmOlderThanCleansUp is the acceptance criterion for T-704.
func TestSnapRmOlderThanCleansUp(t *testing.T) {
	m, _ := withManager(t)
	a := snapAt(t, m, "acme-shop-c56680", "github.com/acme/shop", "cart", 482, 45*24*time.Hour)
	b := snapAt(t, m, "acme-blog-0a1b2c", "github.com/acme/blog", "", 7, 31*24*time.Hour)
	keep := snapAt(t, m, "acme-shop-c56680", "github.com/acme/shop", "", 519, 29*24*time.Hour)

	// Without an answer, nothing goes.
	out, _, err := run(t, "snap", "rm", "--older-than=30d")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"removes 2 snapshots older than 30d", "cart (" + a.ID + ")", b.ID, "Nothing was removed"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if got := storedIDs(t, m); len(got) != 3 {
		t.Errorf("snapshots left = %v, want all three", got)
	}

	out, _, err = run(t, "snap", "rm", "--older-than=30d", "--yes")
	if err != nil {
		t.Fatal(err)
	}
	if got := storedIDs(t, m); len(got) != 1 || got[0] != keep.ID {
		t.Errorf("snapshots left = %v, want only %s", got, keep.ID)
	}
	if !strings.Contains(out, "freed") {
		t.Errorf("output:\n%s", out)
	}

	out, _, err = run(t, "snap", "rm", "--older-than=30d")
	if err != nil || !strings.Contains(out, "No snapshots older than 30d") {
		t.Errorf("nothing left to remove: %v\n%s", err, out)
	}
}

func TestSnapRmByIDOrName(t *testing.T) {
	m, _ := withManager(t)
	cart := snapAt(t, m, "acme-shop-c56680", "github.com/acme/shop", "cart", 482, time.Hour)
	plain := snapAt(t, m, "acme-shop-c56680", "github.com/acme/shop", "", 482, time.Hour)
	keep := snapAt(t, m, "acme-shop-c56680", "github.com/acme/shop", "keep", 482, time.Hour)

	// One name that does not exist, and nothing is removed: the
	// others are not taken away by a command that failed.
	_, _, err := run(t, "snap", "rm", "cart", "nope")
	if err == nil || !strings.Contains(err.Error(), `"nope"`) {
		t.Errorf("err = %v", err)
	}
	if got := storedIDs(t, m); len(got) != 3 {
		t.Errorf("snapshots left = %v after a failed rm", got)
	}

	if _, _, err := run(t, "snap", "rm", "cart", plain.ID, "cart"); err != nil {
		t.Fatal(err)
	}
	if got := storedIDs(t, m); len(got) != 1 || got[0] != keep.ID {
		t.Errorf("snapshots left = %v, want only %s (removed %s and %s)", got, keep.ID, cart.ID, plain.ID)
	}
	if _, err := os.Stat(filepath.Join(m.StateDir, "snapshots", "acme-shop-c56680", cart.ID+".gz")); err == nil {
		t.Error("the data of a removed snapshot is still there")
	}
}

// Two repositories may both have a snapshot called cart; which one is
// meant is not guessed.
func TestSnapRmDoesNotGuessBetweenRepositories(t *testing.T) {
	m, _ := withManager(t)
	a := snapAt(t, m, "acme-shop-c56680", "github.com/acme/shop", "cart", 482, time.Hour)
	b := snapAt(t, m, "acme-blog-0a1b2c", "github.com/acme/blog", "cart", 7, time.Hour)

	_, _, err := run(t, "snap", "rm", "cart")
	if err == nil || !strings.Contains(errs.Hint(err), a.ID) || !strings.Contains(errs.Hint(err), b.ID) {
		t.Errorf("err = %v, hint %q", err, errs.Hint(err))
	}
	if got := storedIDs(t, m); len(got) != 2 {
		t.Errorf("snapshots left = %v", got)
	}
	if _, _, err := run(t, "snap", "rm", b.ID); err != nil {
		t.Errorf("by ID: %v", err)
	}
}

func TestSnapRmNeedsToKnowWhat(t *testing.T) {
	withManager(t)
	for _, args := range [][]string{{"snap", "rm"}, {"snap", "rm", "cart", "--older-than=30d"}, {"snap", "rm", "--older-than=soon"}} {
		if _, _, err := run(t, args...); err == nil || errs.Hint(err) == "" {
			t.Errorf("%v: %v, want an error with a hint", args, err)
		}
	}
}

func TestParseAge(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"30d": 30 * 24 * time.Hour,
		"2w":  14 * 24 * time.Hour,
		"12h": 12 * time.Hour,
		"90m": 90 * time.Minute,
	} {
		if got, err := parseAge(in); err != nil || got != want {
			t.Errorf("parseAge(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "d", "0d", "-3d", "30", "soon", "1.5d"} {
		if _, err := parseAge(bad); err == nil {
			t.Errorf("parseAge(%q) accepted", bad)
		}
	}
}

// Two clones of the same repository keep their snapshots apart and show
// the same name; the column is there because the stores differ.
func TestSnapLsShowsTheRepositoryForTwoClonesOfOne(t *testing.T) {
	m, _ := withManager(t)
	snapAt(t, m, "acme-shop-c56680", "github.com/acme/shop", "cart", 482, time.Hour)
	snapAt(t, m, "acme-shop-9f8e7d", "github.com/acme/shop", "cart", 482, time.Hour)

	out, _, err := run(t, "snap", "ls")
	if err != nil || !strings.HasPrefix(out, "REPO") {
		t.Errorf("%v\n%s", err, out)
	}
}

// An empty name is no name: it does not pick the unnamed snapshots.
func TestSnapRmOfAnEmptyName(t *testing.T) {
	m, _ := withManager(t)
	snapAt(t, m, "acme-shop-c56680", "github.com/acme/shop", "", 482, time.Hour)

	if _, _, err := run(t, "snap", "rm", ""); err == nil {
		t.Error(`pit snap rm "" succeeded`)
	}
	if got := storedIDs(t, m); len(got) != 1 {
		t.Errorf("snapshots left = %v", got)
	}
}
