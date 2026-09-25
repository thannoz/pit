package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/thannoz/pit/internal/ui"
)

// No test opens a browser on the machine it runs on; one that is about
// opening one says so with withOpened.
func init() {
	openInBrowser = func(context.Context, *ui.Printer, string) {}
}

// withOpened notes the URLs pit would have opened.
func withOpened(t *testing.T) *[]string {
	t.Helper()
	var opened []string
	previous := openInBrowser
	openInBrowser = func(_ context.Context, _ *ui.Printer, url string) { opened = append(opened, url) }
	t.Cleanup(func() { openInBrowser = previous })
	return &opened
}

// withBase records a pull request's sandbox and its base's.
func withBase(t *testing.T) {
	t.Helper()
	own := recorded(482, "github.com/acme/shop", "acme-shop-c56680", "refunds", time.Minute)
	base := recorded(482, "github.com/acme/shop", "acme-shop-c56680", "refunds", 2*time.Minute)
	base.Base, base.BaseBranch, base.Port, base.URL = true, "main", 40999, "http://localhost:40999/"
	base.Project += "-base"
	withManager(t, own, base)
}

// --base points a command at the base, and without it at the pull
// request: the same number names both.
func TestBaseIsChosenWithTheFlag(t *testing.T) {
	withBase(t)
	opened := withOpened(t)
	for _, args := range []string{"open 482", "open 482 --base"} {
		if _, _, err := run(t, strings.Fields(args)...); err != nil {
			t.Errorf("%s: %v", args, err)
		}
	}
	if strings.Join(*opened, " ") != "http://localhost:40482 http://localhost:40999/" {
		t.Errorf("opened %v", *opened)
	}
}

func TestNoBase(t *testing.T) {
	withManager(t, recorded(482, "github.com/acme/shop", "acme-shop-c56680", "refunds", time.Minute))
	_, _, err := run(t, "logs", "482", "--base")
	if err == nil || !strings.Contains(err.Error(), "there is no sandbox for the base of #482") {
		t.Errorf("err = %v", err)
	}
}

func TestLsShowsTheBase(t *testing.T) {
	withBase(t)
	out, _, err := run(t, "ls")
	if err != nil {
		t.Fatal(err)
	}
	var baseLine string
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "#482 base") {
			baseLine = l
		}
	}
	if baseLine == "" || !strings.Contains(baseLine, "main") || strings.Contains(baseLine, "refunds") {
		t.Errorf("ls:\n%s", out)
	}
	out, _, _ = run(t, "ls", "--json")
	if strings.Count(out, `"base": true`) != 1 {
		t.Errorf("ls --json:\n%s", out)
	}
}

// What is about the pull request itself refuses its base.
func TestCommandsAboutThePullRequestRefuseTheBase(t *testing.T) {
	withBase(t)
	for _, args := range [][]string{
		{"what", "482", "--base"},
		{"note", "482", "found", "--base"},
		{"report", "482", "--base"},
		{"replay", "482", "recipe.json", "--base"},
		{"open", "482", "--record", "--base"},
	} {
		_, _, err := run(t, args...)
		if err == nil || !strings.Contains(err.Error(), "is about the pull request itself") {
			t.Errorf("%v: err = %v", args, err)
		}
	}
}

func TestDownOfTheBaseOnly(t *testing.T) {
	withBase(t)
	if _, err := runCLIWithInput(t, "", "down", "482", "--base", "--yes"); err != nil {
		t.Fatal(err)
	}
	m, _ := manager()
	f, _ := m.Store.Load()
	if len(f.Sandboxes) != 1 || f.Sandboxes[0].Base {
		t.Errorf("left %+v", f.Sandboxes)
	}
}
