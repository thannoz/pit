package runtime

import (
	"strings"
	"testing"

	"github.com/thannoz/pit/internal/errs"
)

func TestProjectName(t *testing.T) {
	tests := []struct {
		name    string
		repoRef string
		pr      int
		want    string
	}{
		{"plain", "acme-shop-c56680", 482, "pit-acme-shop-c56680-482"},
		{"uppercase is folded", "Acme-Shop-C56680", 482, "pit-acme-shop-c56680-482"},
		{"underscores are allowed", "acme_shop-c56680", 1, "pit-acme_shop-c56680-1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ProjectName(tt.repoRef, tt.pr)
			if err != nil {
				t.Fatalf("ProjectName: %v", err)
			}
			if got != tt.want {
				t.Errorf("ProjectName() = %q, want %q", got, tt.want)
			}
			if !composeName.MatchString(got) {
				t.Errorf("%q is not a name Compose accepts", got)
			}
		})
	}
}

// TestProjectNameCarriesTheRepositoryHash guards the isolation itself:
// the project name is the only thing keeping two sandboxes apart, so
// two projects that share a slug must not share a name.
func TestProjectNameCarriesTheRepositoryHash(t *testing.T) {
	github, err := ProjectName("acme-shop-c56680", 1)
	if err != nil {
		t.Fatalf("ProjectName: %v", err)
	}
	internal, err := ProjectName("acme-shop-9f2b1a", 1)
	if err != nil {
		t.Fatalf("ProjectName: %v", err)
	}

	if github == internal {
		t.Errorf("two repositories with the same slug both got %q", github)
	}
}

func TestProjectNameRejectsUnusableInput(t *testing.T) {
	tests := []struct {
		name    string
		repoRef string
		pr      int
	}{
		{"no pull request", "acme-shop-c56680", 0},
		{"negative pull request", "acme-shop-c56680", -1},
		{"spaces", "acme shop", 1},
		{"a slash", "acme/shop", 1},
		{"a dot", "acme.shop", 1},
		{"empty", "", 1},
		{"far too long", strings.Repeat("a", 80), 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ProjectName(tt.repoRef, tt.pr)
			if err == nil {
				t.Fatal("want an error")
			}
			if errs.Hint(err) == "" && tt.pr > 0 {
				t.Error("the error carries no hint")
			}
		})
	}
}

// TestPrefixProtectsAgainstALeadingDash records why the check is on
// the finished name rather than on the input: Compose requires a name
// to start with a letter or digit, and the prefix guarantees that
// whatever the repository reference looks like.
func TestPrefixProtectsAgainstALeadingDash(t *testing.T) {
	got, err := ProjectName("-shop", 1)
	if err != nil {
		t.Fatalf("ProjectName: %v", err)
	}
	if !composeName.MatchString(got) {
		t.Errorf("%q is not a name Compose accepts", got)
	}
}

func TestPullRequestOf(t *testing.T) {
	tests := []struct {
		project string
		want    int
		ok      bool
	}{
		{"pit-acme-shop-c56680-482", 482, true},
		{"pit-a-1", 1, true},
		{"someone-elses-project", 0, false},
		{"pit-no-number-here", 0, false},
		{"pit-acme-shop-0", 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.project, func(t *testing.T) {
			got, ok := PullRequestOf(tt.project)
			if ok != tt.ok || got != tt.want {
				t.Errorf("PullRequestOf(%q) = %d, %v; want %d, %v", tt.project, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestIsPitProject(t *testing.T) {
	// Cleaning up after a crash starts from a list of names and nothing
	// else, so pit has to recognise its own.
	if !IsPitProject("pit-acme-shop-c56680-482") {
		t.Error("pit does not recognise its own project")
	}
	if IsPitProject("acme-shop") {
		t.Error("someone else's project was claimed as pit's")
	}
}

func TestTheBaseHasItsOwnProject(t *testing.T) {
	own, err := ProjectName("acme-shop-c56680", 482)
	if err != nil {
		t.Fatal(err)
	}
	base, err := BaseProjectName("acme-shop-c56680", 482)
	if err != nil || base != own+"-base" {
		t.Errorf("base = %q, %v", base, err)
	}
	if OverridePath("/s", 482) == BaseOverridePath("/s", 482) {
		t.Error("one override for both")
	}
}
