package analysis

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thannoz/pit/internal/diff"
)

// golden reads a frozen classification: kind, a tab, a path.
func golden(t *testing.T, name string) (diff.Diff, map[string]Kind) {
	t.Helper()

	f, err := os.Open(filepath.Join("..", "..", "testdata", "classify", name+".golden"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = f.Close() }()

	var d diff.Diff
	want := map[string]Kind{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		kind, path, ok := strings.Cut(line, "\t")
		if !ok {
			t.Fatalf("malformed golden line %q", line)
		}
		d.Files = append(d.Files, diff.File{Path: path})
		want[path] = Kind(kind)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read: %v", err)
	}
	return d, want
}

// TestRealPullRequestsAreClassified is the acceptance criterion for
// T-602: pull requests of more than twenty files, from real projects,
// each file where a reviewer would put it.
func TestRealPullRequestsAreClassified(t *testing.T) {
	for _, name := range []string{"documenso-3362", "dub-4534", "lipgloss-672"} {
		t.Run(name, func(t *testing.T) {
			d, want := golden(t, name)
			if len(d.Files) <= 20 {
				t.Fatalf("%s has %d files; the criterion is about more than twenty", name, len(d.Files))
			}

			for _, f := range Classify(d, nil) {
				if f.Kind != want[f.Path] {
					t.Errorf("%s is %q (%s), want %q", f.Path, f.Kind, f.Reason, want[f.Path])
				}
			}
		})
	}
}

func TestTestsAndDocsAreNotOnTheChecklist(t *testing.T) {
	// The other half of the criterion.
	for _, name := range []string{"documenso-3362", "dub-4534", "lipgloss-672"} {
		d, _ := golden(t, name)
		for _, f := range Classify(d, nil) {
			if (f.Kind == Test || f.Kind == Docs) && f.OnChecklist() {
				t.Errorf("%s: %s is %s and still on the checklist", name, f.Path, f.Kind)
			}
		}
	}
}

func TestConventions(t *testing.T) {
	// Rules for frameworks the real pull requests above do not use.
	// They come from each framework's own naming, and are checked here
	// so that they cannot misfire unnoticed.
	tests := []struct {
		path string
		want Kind
	}{
		// Next.js, both routers, at the root, in src/ and in a monorepo.
		{"app/page.tsx", Page},
		{"src/app/blog/[slug]/page.tsx", Page},
		{"apps/web/app/(marketing)/pricing/page.mdx", Page},
		{"app/dashboard/layout.tsx", Page},
		{"app/api/users/route.ts", Endpoint},
		{"pages/index.tsx", Page},
		{"src/pages/settings/profile.tsx", Page},
		{"pages/_app.tsx", Page},
		{"pages/api/users.ts", Endpoint},

		// SvelteKit.
		{"src/routes/+page.svelte", Page},
		{"src/routes/blog/+layout.svelte", Page},
		{"src/routes/api/items/+server.ts", Endpoint},

		// Remix.
		{"app/routes/_index.tsx", Page},
		{"app/routes/api.users.ts", Endpoint},

		// Nuxt.
		{"pages/about.vue", Page},
		{"server/api/items.get.ts", Endpoint},

		// What a page looks like and shows.
		{"styles/globals.css", Style},
		{"src/components/Button.module.scss", Style},
		{"tailwind.config.ts", Style},
		{"public/logo.svg", Asset},
		{"assets/fonts/Inter.woff2", Asset},
		{"locales/de.json", Asset},
		{"src/components/Button.tsx", Component},
		{"src/lib/Card.svelte", Component},
		{"templates/order.gohtml", Component},

		// The database.
		{"db/migrate/20240101_add_refunds.rb", Migration},
		{"migrations/0042_add_vat_id.up.sql", Migration},
		{"alembic/versions/a1b2_add_table.py", Migration},

		// How it runs, and how it is built.
		{"Dockerfile", Config},
		{".env.example", Config},
		{"next.config.mjs", Config},
		{"docker-compose.yml", Config},
		{"package.json", Dependency},
		{"requirements.txt", Dependency},
		{"Makefile", Tooling},
		{".github/workflows/ci.yml", Tooling},
		{"tsconfig.json", Tooling},
		{"eslint.config.js", Tooling},
		{"api/v1/service.pb.go", Generated},
		{"dist/app.js", Generated},

		// Behaviour without a screen.
		{"internal/handlers/orders.go", Code},
		{"src/lib/format.ts", Code},

		// Not recognised is not hidden.
		{"data/export.bin", Other},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got, reason := kindOf(tt.path); got != tt.want {
				t.Errorf("kindOf(%q) = %q (%s), want %q", tt.path, got, reason, tt.want)
			}
		})
	}
}

func TestPrecedence(t *testing.T) {
	// Where two rules could both claim a file, the one that says what
	// the file is for wins.
	tests := []struct {
		path string
		want Kind
		why  string
	}{
		{"app/checkout/page.test.tsx", Test, "a test of a page is a test"},
		{"src/pages/__tests__/index.tsx", Test, "under pages/, but in a test directory"},
		{"app/blog/page.mdx", Page, "an MDX page is a page, not documentation"},
		{"docs/guide.mdx", Docs, "MDX under docs/ is documentation"},
		{"requirements.txt", Dependency, "a list of packages, not prose"},
		{"src/components/pages/HomePage.tsx", Component, "a pages/ folder inside components/ is not a router"},
		{"lib/app/page.tsx", Component, "an app/ folder that is not at an application's root"},
		{"fixtures/standard.sql", Code, "fixtures may be what a reviewer sees; they stay visible"},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got, reason := kindOf(tt.path); got != tt.want {
				t.Errorf("kindOf(%q) = %q (%s), want %q: %s", tt.path, got, reason, tt.want, tt.why)
			}
		})
	}
}

func TestIgnorePatternsTakeAFileOffTheChecklist(t *testing.T) {
	d := diff.Diff{Files: []diff.File{
		{Path: "apps/web/lib/jobs/registry.ts"},
		{Path: "apps/web/app/(ee)/api/cron/rewards/process/route.ts"},
	}}

	files := Classify(d, []string{"**/cron/**"})

	if files[0].Ignored != "" || !files[0].OnChecklist() {
		t.Errorf("%s was ignored by a pattern that does not match it", files[0].Path)
	}
	if files[1].Ignored != "**/cron/**" || files[1].OnChecklist() {
		t.Errorf("%s: ignored = %q, on checklist = %v", files[1].Path, files[1].Ignored, files[1].OnChecklist())
	}
	// Ignoring does not change what a file is, only whether to look.
	if files[1].Kind != Endpoint {
		t.Errorf("ignoring changed the kind to %q", files[1].Kind)
	}
}

func TestADeletedFileIsWhatItWas(t *testing.T) {
	// A deleted endpoint is still an endpoint -- a more interesting one
	// than most, since something may still be calling it.
	d := diff.Diff{Files: []diff.File{
		{Path: "apps/web/app/(ee)/api/groups/remap-discount-codes/route.ts", Change: diff.Deleted},
	}}
	if got := Classify(d, nil)[0].Kind; got != Endpoint {
		t.Errorf("a deleted route handler is %q", got)
	}
}

func TestEveryFileHasAReason(t *testing.T) {
	// A classification nobody can question is one nobody can trust.
	for _, name := range []string{"documenso-3362", "dub-4534", "lipgloss-672"} {
		d, _ := golden(t, name)
		for _, f := range Classify(d, nil) {
			if f.Reason == "" {
				t.Errorf("%s has no reason", f.Path)
			}
		}
	}
}
