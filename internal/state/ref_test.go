package state

import (
	"testing"
)

func TestParseRef(t *testing.T) {
	tests := []struct {
		in       string
		wantRepo string
		wantPR   int
	}{
		{"482", "", 482},
		{" 482 ", "", 482},
		{"acme/shop#482", "acme/shop", 482},
		{"github.com/acme/shop#482", "github.com/acme/shop", 482},
		{"shop-upstream#7", "shop-upstream", 7},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := ParseRef(tt.in)
			if err != nil {
				t.Fatalf("ParseRef(%q): %v", tt.in, err)
			}
			if got.Repo != tt.wantRepo || got.PR != tt.wantPR {
				t.Errorf("ParseRef(%q) = %+v, want {%q %d}", tt.in, got, tt.wantRepo, tt.wantPR)
			}
		})
	}
}

func TestParseRefRejectsNonsense(t *testing.T) {
	for _, in := range []string{"", "abc", "0", "-5", "acme/shop#", "acme/shop#abc", "#"} {
		t.Run(in, func(t *testing.T) {
			if _, err := ParseRef(in); err == nil {
				t.Errorf("ParseRef(%q) = nil error, want one", in)
			}
		})
	}
}

func TestRefRoundTrips(t *testing.T) {
	// The hint for an ambiguous reference prints these, so they have
	// to be typeable back in.
	for _, in := range []string{"482", "acme/shop#482"} {
		t.Run(in, func(t *testing.T) {
			ref, err := ParseRef(in)
			if err != nil {
				t.Fatalf("ParseRef: %v", err)
			}
			if got := ref.String(); got != in {
				t.Errorf("String() = %q, want %q", got, in)
			}
		})
	}
}

func TestRefMatches(t *testing.T) {
	box := Sandbox{PR: 482, Repo: "github.com/acme/shop"}

	tests := []struct {
		ref  string
		want bool
	}{
		{"482", true},
		{"483", false},
		{"acme/shop#482", true},
		{"github.com/acme/shop#482", true},
		// The trailing part is accepted: people type the name they
		// remember, and a name that fits two repositories is caught
		// as an ambiguity rather than guessed at.
		{"shop#482", true},
		{"acme/admin#482", false},
		{"acme/shop#483", false},
	}

	for _, tt := range tests {
		t.Run(tt.ref, func(t *testing.T) {
			ref, err := ParseRef(tt.ref)
			if err != nil {
				t.Fatalf("ParseRef: %v", err)
			}
			if got := ref.Matches(box); got != tt.want {
				t.Errorf("Matches(%q) = %v, want %v", tt.ref, got, tt.want)
			}
		})
	}
}

func TestRefMatchesOnlyAtASegmentBoundary(t *testing.T) {
	// "shop" must not match "workshop": the boundary is what keeps a
	// convenient short form from being a wrong one.
	box := Sandbox{PR: 1, Repo: "github.com/acme/workshop"}

	ref, err := ParseRef("shop#1")
	if err != nil {
		t.Fatalf("ParseRef: %v", err)
	}
	if ref.Matches(box) {
		t.Error("shop matched workshop")
	}
}

func TestShortRepo(t *testing.T) {
	tests := []struct {
		name string
		repo string
		want string
	}{
		{"hosted drops the host", "github.com/acme/shop", "acme/shop"},
		{"gitlab subgroups survive", "gitlab.com/group/sub/tool", "group/sub/tool"},
		{"a filesystem remote keeps only the directory", "local//tmp/pit-t317/shop-upstream", "shop-upstream"},
		{"already short", "acme/shop", "acme/shop"},
		{"no slash at all", "shop", "shop"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := (Sandbox{Repo: tt.repo}).ShortRepo(); got != tt.want {
				t.Errorf("ShortRepo(%q) = %q, want %q", tt.repo, got, tt.want)
			}
		})
	}
}

func TestDescribe(t *testing.T) {
	tests := []struct {
		name string
		box  Sandbox
		want string
	}{
		{
			name: "everything known",
			box:  Sandbox{PR: 482, Title: "Rework checkout", Branch: "feat/checkout", Repo: "github.com/acme/shop"},
			want: `#482 "Rework checkout" (feat/checkout) in acme/shop`,
		},
		{
			name: "no branch, as a git-read pull request has none",
			box:  Sandbox{PR: 7, Title: "A change", Repo: "local//tmp/demo"},
			want: `#7 "A change" in demo`,
		},
		{
			name: "no title either",
			box:  Sandbox{PR: 7, Repo: "github.com/acme/shop"},
			want: "#7 in acme/shop",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.box.Describe(); got != tt.want {
				t.Errorf("Describe() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestQualifiedRefIsTypeable(t *testing.T) {
	box := Sandbox{PR: 482, Repo: "github.com/acme/shop"}

	ref, err := ParseRef(box.QualifiedRef())
	if err != nil {
		t.Fatalf("the qualified reference cannot be parsed back: %v", err)
	}
	if !ref.Matches(box) {
		t.Errorf("%q does not match the sandbox it names", box.QualifiedRef())
	}
}
