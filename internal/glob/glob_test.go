package glob

import "testing"

func TestMatch(t *testing.T) {
	tests := []struct {
		pattern, name string
		want          bool
	}{
		// ** at the end: everything below.
		{"docs/**", "docs/guide.md", true},
		{"docs/**", "docs/a/b/c.md", true},
		{"docs/**", "docs", true},
		{"docs/**", "src/docs/guide.md", false},

		// ** at the start: at any depth.
		{"**/*.test.ts", "app.test.ts", true},
		{"**/*.test.ts", "src/deep/app.test.ts", true},
		{"**/*.test.ts", "src/app.ts", false},

		// ** in the middle includes zero directories.
		{"src/**/page.tsx", "src/page.tsx", true},
		{"src/**/page.tsx", "src/app/blog/page.tsx", true},
		{"src/**/page.tsx", "lib/app/page.tsx", false},

		// Within a segment, path.Match's own rules.
		{"*.md", "README.md", true},
		{"*.md", "docs/README.md", false},
		{"file?.go", "file1.go", true},
		{"[ab].go", "c.go", false},

		// A single * never crosses a directory.
		{"src/*.go", "src/a/b.go", false},

		// Repeated ** is one **.
		{"**/**/x", "a/x", true},

		// Exact.
		{"go.sum", "go.sum", true},
		{"go.sum", "sub/go.sum", false},
	}

	for _, tt := range tests {
		t.Run(tt.pattern+" "+tt.name, func(t *testing.T) {
			if got := Match(tt.pattern, tt.name); got != tt.want {
				t.Errorf("Match(%q, %q) = %v, want %v", tt.pattern, tt.name, got, tt.want)
			}
		})
	}
}

func TestValid(t *testing.T) {
	for _, ok := range []string{"docs/**", "**/*.test.ts", "[abc]/*.go"} {
		if err := Valid(ok); err != nil {
			t.Errorf("Valid(%q) = %v, want nil", ok, err)
		}
	}
	// An unclosed class is the typo that otherwise ignores nothing and
	// says nothing.
	if err := Valid("src/[abc.go"); err == nil {
		t.Error("Valid accepted an unclosed character class")
	}
}

func TestAMalformedPatternMatchesNothing(t *testing.T) {
	if Match("src/[abc.go", "src/a.go") {
		t.Error("a malformed pattern matched")
	}
}
