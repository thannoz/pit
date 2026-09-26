package workspace

import (
	"strings"
	"testing"
)

// TestSameRepositoryFromEveryURLForm is the acceptance criterion for
// T-101: however a clone was made, pit must arrive at one identity.
func TestSameRepositoryFromEveryURLForm(t *testing.T) {
	forms := []string{
		"git@github.com:acme/shop.git",
		"git@github.com:acme/shop",
		"https://github.com/acme/shop",
		"https://github.com/acme/shop.git",
		"https://github.com/acme/shop/",
		"ssh://git@github.com/acme/shop.git",
		"git://github.com/acme/shop.git",
		"https://GitHub.com/Acme/Shop.git",
		// A token in the URL must not change the identity, and must
		// not survive into anything pit writes down.
		"https://someone:ghp_secret@github.com/acme/shop.git",
	}

	want := Identity{Host: "github.com", Owner: "acme", Name: "shop"}

	for _, form := range forms {
		t.Run(form, func(t *testing.T) {
			got, err := ParseRemoteURL(form)
			if err != nil {
				t.Fatalf("ParseRemoteURL(%q): %v", form, err)
			}
			if got != want {
				t.Errorf("ParseRemoteURL(%q) = %+v, want %+v", form, got, want)
			}
		})
	}
}

func TestTokenNeverSurvivesParsing(t *testing.T) {
	id, err := ParseRemoteURL("https://someone:ghp_secret@github.com/acme/shop.git")
	if err != nil {
		t.Fatalf("ParseRemoteURL: %v", err)
	}
	for _, leaked := range []string{"ghp_secret", "someone"} {
		if strings.Contains(id.String()+id.Ref(), leaked) {
			t.Errorf("%q leaked into the identity %q", leaked, id)
		}
	}
}

func TestParseRemoteURL(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want Identity
	}{
		{
			name: "gitlab subgroups belong to the owner",
			raw:  "https://gitlab.com/group/subgroup/project.git",
			want: Identity{Host: "gitlab.com", Owner: "group/subgroup", Name: "project"},
		},
		{
			name: "scp form with a subgroup",
			raw:  "git@gitlab.com:group/subgroup/project.git",
			want: Identity{Host: "gitlab.com", Owner: "group/subgroup", Name: "project"},
		},
		{
			name: "self hosted with a port",
			raw:  "ssh://git@git.example.org:2222/team/tool.git",
			want: Identity{Host: "git.example.org", Owner: "team", Name: "tool"},
		},
		{
			name: "repeated slashes are harmless",
			raw:  "https://github.com//acme//shop.git",
			want: Identity{Host: "github.com", Owner: "acme", Name: "shop"},
		},
		{
			name: "a filesystem path is a local repository",
			raw:  "/Users/someone/code/shop",
			want: Identity{Host: LocalHost, Owner: "/users/someone/code", Name: "shop"},
		},
		{
			name: "file scheme is the same thing",
			raw:  "file:///tmp/shop.git",
			want: Identity{Host: LocalHost, Owner: "/tmp", Name: "shop"},
		},
		{
			name: "a Windows path is one too",
			raw:  `C:\Users\someone\code\shop.git`,
			want: Identity{Host: LocalHost, Owner: "c:/users/someone/code", Name: "shop"},
		},
		{
			name: "written with forward slashes",
			raw:  "D:/repos/shop/",
			want: Identity{Host: LocalHost, Owner: "d:/repos", Name: "shop"},
		},
		{
			name: "on a share",
			raw:  `\\server\repos\shop`,
			want: Identity{Host: LocalHost, Owner: "/server/repos", Name: "shop"},
		},
		{
			name: "file scheme with a drive",
			raw:  "file:///C:/repos/shop.git",
			want: Identity{Host: LocalHost, Owner: "/c:/repos", Name: "shop"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseRemoteURL(tt.raw)
			if err != nil {
				t.Fatalf("ParseRemoteURL(%q): %v", tt.raw, err)
			}
			if got != tt.want {
				t.Errorf("ParseRemoteURL(%q) = %+v, want %+v", tt.raw, got, tt.want)
			}
		})
	}
}

func TestParseRemoteURLRejectsNonsense(t *testing.T) {
	tests := []struct{ name, raw string }{
		{"empty", ""},
		{"whitespace", "   "},
		{"host without a path", "https://github.com"},
		{"no host and no path", "not a url at all!"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ParseRemoteURL(tt.raw); err == nil {
				t.Errorf("ParseRemoteURL(%q) = nil error, want one", tt.raw)
			}
		})
	}
}

func TestDifferentRepositoriesGetDifferentHashes(t *testing.T) {
	a := Identity{Host: "github.com", Owner: "acme", Name: "shop"}
	b := Identity{Host: "github.com", Owner: "other", Name: "shop"}

	if a.Hash() == b.Hash() {
		t.Errorf("%q and %q share the hash %q", a, b, a.Hash())
	}
	// Same slug, different hash: that is exactly what the hash is for.
	if a.Slug() != b.Slug() {
		t.Logf("slugs differ (%q, %q), which is fine but not the case under test", a.Slug(), b.Slug())
	}
}

func TestHashIsStable(t *testing.T) {
	id := Identity{Host: "github.com", Owner: "acme", Name: "shop"}

	// Pinned: the hash names directories and Compose projects, so if it
	// ever changes, every existing sandbox becomes unreachable. Changing
	// this value is a decision, not an accident.
	const want = "c56680"
	if got := id.Hash(); got != want {
		t.Errorf("Hash() = %q, want %q -- changing it orphans existing sandboxes", got, want)
	}
}

func TestSlugIsSafeForDockerAndDirectories(t *testing.T) {
	tests := []struct {
		name string
		id   Identity
		want string
	}{
		{"plain", Identity{Host: "github.com", Owner: "acme", Name: "shop"}, "acme-shop"},
		{"uppercase is folded", Identity{Host: "github.com", Owner: "ACME", Name: "Shop"}, "acme-shop"},
		{"dots and spaces are replaced", Identity{Host: "github.com", Owner: "a.c me", Name: "sh.op"}, "a-c-me-sh-op"},
		{"only the innermost group is kept", Identity{Host: "gitlab.com", Owner: "group/sub", Name: "tool"}, "sub-tool"},
		{"no owner", Identity{Host: LocalHost, Name: "shop"}, "shop"},
		{"a Windows directory", Identity{Host: LocalHost, Owner: `C:\Users\someone\code`, Name: "shop"}, "code-shop"},
		{"leading digit is kept", Identity{Host: "github.com", Owner: "9lives", Name: "cat"}, "9lives-cat"},
		{"unusable name falls back", Identity{Host: "github.com", Owner: "...", Name: "..."}, "repo"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.id.Slug()
			if got != tt.want {
				t.Errorf("Slug() = %q, want %q", got, tt.want)
			}
			assertComposeSafe(t, got)
		})
	}
}

// assertComposeSafe pins the rule Docker Compose applies to project
// names: lowercase letters, digits, dashes and underscores, starting
// with a letter or digit.
func assertComposeSafe(t *testing.T, s string) {
	t.Helper()

	if s == "" {
		t.Fatal("empty name")
	}
	if c := rune(s[0]); !composeCanStartWith(c) {
		t.Errorf("%q starts with %q, which Compose rejects", s, string(c))
	}
	for _, c := range s {
		if !composeAllows(c) {
			t.Errorf("%q contains %q, which Compose rejects", s, string(c))
		}
	}
}

func composeCanStartWith(c rune) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		return true
	}
	return false
}

func composeAllows(c rune) bool {
	switch {
	case composeCanStartWith(c), c == '-', c == '_':
		return true
	}
	return false
}

func TestRefCombinesSlugAndHash(t *testing.T) {
	id := Identity{Host: "github.com", Owner: "acme", Name: "shop"}

	if got, want := id.Ref(), id.Slug()+"-"+id.Hash(); got != want {
		t.Errorf("Ref() = %q, want %q", got, want)
	}
}
