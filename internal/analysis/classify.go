package analysis

import (
	"path"
	"slices"
	"strings"

	"github.com/thannoz/pit/internal/diff"
	"github.com/thannoz/pit/internal/glob"
)

// Kind is what a changed file is, from a reviewer's point of view.
type Kind string

// The kinds a file can be. The distinction that matters is between
// the ones that lead to something a reviewer can open and look at, and
// the ones that do not.
const (
	// Page is something a reviewer opens in a browser, or a file that
	// wraps such things (a layout).
	Page Kind = "page"
	// Endpoint is something a page calls: an API route.
	Endpoint Kind = "endpoint"
	// Component is part of a page: a React component, a template.
	Component Kind = "component"
	// Style changes how pages look without being one.
	Style Kind = "style"
	// Asset is what a page shows: an image, a font, translated text.
	Asset Kind = "asset"
	// Migration changes the data a page shows: a migration or the
	// data model it migrates.
	Migration Kind = "migration"
	// Config changes how the application runs.
	Config Kind = "config"
	// Code is behaviour without a screen of its own. Which screens it
	// reaches is a question for later.
	Code Kind = "code"

	// Test, Docs, Dependency, Tooling and Generated are real changes
	// that no page shows.
	Test       Kind = "test"
	Docs       Kind = "docs"
	Dependency Kind = "dependency"
	Tooling    Kind = "tooling"
	Generated  Kind = "generated"

	// Other is a file none of the rules recognised. It stays on the
	// checklist: not knowing what something is is no reason to hide it.
	Other Kind = "other"
)

// File is a changed file and what it is.
type File struct {
	diff.File
	// Kind is what the file is.
	Kind Kind
	// Reason says which rule decided that, in words a reader can check.
	// A classification nobody can question is one nobody can trust.
	Reason string
	// Ignored is the review.ignore pattern that matched, if any.
	Ignored string
}

// OnChecklist reports whether the file belongs on a reviewer's list of
// things to look at.
func (f File) OnChecklist() bool {
	if f.Ignored != "" {
		return false
	}
	switch f.Kind {
	case Test, Docs, Dependency, Tooling, Generated:
		return false
	default:
		return true
	}
}

// Classify decides what each changed file is.
//
// By path alone, on purpose: the rules are the conventions projects
// already follow -- where tests live, what a page is called in each
// framework -- and a rule that has to open the file is a rule that can
// be slow, and wrong in ways that are hard to see. Reading files is for
// the analysis that follows, which knows what it is looking for.
//
// A deleted file is classified by where it was. A deleted endpoint is
// still an endpoint, and a more interesting one than most.
func Classify(d diff.Diff, ignore []string) []File {
	out := make([]File, 0, len(d.Files))
	for _, f := range d.Files {
		kind, reason := kindOf(f.Path)
		c := File{File: f, Kind: kind, Reason: reason}
		for _, pattern := range ignore {
			if glob.Match(pattern, f.Path) {
				c.Ignored = pattern
				break
			}
		}
		out = append(out, c)
	}
	return out
}

// facts is what the rules look at: a path taken apart once.
type facts struct {
	dirs []string // lower-cased directory names, outermost first
	base string   // lower-cased file name
	ext  string   // lower-cased extension, without the dot
	stem string   // the file name without its extension
}

func factsOf(p string) facts {
	lower := strings.ToLower(p)
	dir, base := path.Split(lower)
	ext := strings.TrimPrefix(path.Ext(base), ".")

	var dirs []string
	if dir != "" {
		dirs = strings.Split(strings.Trim(dir, "/"), "/")
	}
	return facts{dirs: dirs, base: base, ext: ext, stem: strings.TrimSuffix(base, "."+ext)}
}

// in reports whether any directory on the path has one of the names.
func (f facts) in(names ...string) bool {
	for _, d := range f.dirs {
		if slices.Contains(names, d) {
			return true
		}
	}
	return false
}

// rooted reports whether a directory of that name sits at the root of
// an application -- the repository's root, src/, or the same inside one
// app of a monorepo -- and returns what lies below it.
//
// Frameworks mean their directories only there. A pages/ inside
// components/ is a folder of components that happen to be pages in
// someone's head, and treating it as a Next.js router would be wrong
// about every file in it.
func (f facts) rooted(name string) ([]string, bool) {
	for i, d := range f.dirs {
		if d == name && appRoot(f.dirs[:i]) {
			return f.dirs[i+1:], true
		}
	}
	return nil, false
}

// appRoot reports whether the directories above a framework directory
// are the ones an application's root is found at.
func appRoot(above []string) bool {
	monorepo := func(d string) bool {
		return slices.Contains([]string{"apps", "packages", "services", "sites", "web"}, d)
	}
	switch len(above) {
	case 0:
		return true
	case 1:
		return above[0] == "src"
	case 2:
		return monorepo(above[0])
	case 3:
		return monorepo(above[0]) && above[2] == "src"
	default:
		return false
	}
}

// rootedUnder reports whether the path runs through a rooted dir and,
// somewhere below it, through sub.
func (f facts) rootedUnder(dir, sub string) bool {
	below, ok := f.rooted(dir)
	return ok && slices.Contains(below, sub)
}

// under reports whether the path runs through dir and then, somewhere
// below it, through sub -- at any depth, for conventions that are not
// tied to an application's root.
func (f facts) under(dir, sub string) bool {
	for i, d := range f.dirs {
		if d == dir && slices.Contains(f.dirs[i+1:], sub) {
			return true
		}
	}
	return false
}

// has reports whether a rooted directory of that name is on the path.
func (f facts) has(name string) bool {
	_, ok := f.rooted(name)
	return ok
}

func (f facts) extIs(exts ...string) bool { return slices.Contains(exts, f.ext) }

// rule decides a kind when it matches. The order of the rules is the
// order of precedence: a test is a test even when it lives next to a
// page and is named like one.
type rule struct {
	kind   Kind
	reason string
	match  func(facts) bool
}

var (
	script   = []string{"ts", "tsx", "js", "jsx", "mjs", "cjs"}
	markup   = []string{"tsx", "jsx", "vue", "svelte", "astro"}
	template = []string{"html", "tmpl", "gohtml", "hbs", "ejs", "erb", "jinja", "j2", "twig", "liquid"}
	source   = []string{"go", "ts", "js", "mjs", "cjs", "py", "rb", "php", "java", "kt", "rs", "cs", "swift", "ex", "exs", "sql", "sh"}
	images   = []string{"png", "jpg", "jpeg", "gif", "webp", "avif", "ico", "svg", "bmp"}
	fonts    = []string{"woff", "woff2", "ttf", "otf", "eot"}
	media    = []string{"mp4", "webm", "mp3", "wav", "ogg"}
)

var rules = []rule{
	// Tests first, because they are named after what they test and
	// live next to it.
	{Test, "a test file", func(f facts) bool {
		return strings.HasSuffix(f.base, "_test.go") ||
			strings.Contains(f.base, ".test.") || strings.Contains(f.base, ".spec.") ||
			f.ext == "snap" || f.ext == "golden"
	}},
	{Test, "lives in a test directory", func(f facts) bool {
		// Not fixtures/: in pit's own data concept that is where the
		// data a reviewer sees lives. Keeping a file on the checklist by
		// mistake costs a glance; hiding one costs the review.
		return f.in("test", "tests", "__tests__", "e2e", "testdata", "__snapshots__",
			"cypress", "playwright", "__mocks__")
	}},

	// Pages and endpoints, by each framework's own naming.
	{Endpoint, "a Next.js route handler", func(f facts) bool {
		return f.has("app") && f.stem == "route" && f.extIs(script...)
	}},
	{Page, "a Next.js page", func(f facts) bool {
		return f.has("app") && f.stem == "page" && (f.extIs(script...) || f.ext == "mdx")
	}},
	{Page, "a Next.js layout, which wraps every page below it", func(f facts) bool {
		return f.has("app") && f.extIs(script...) &&
			slices.Contains([]string{"layout", "template", "loading", "error", "not-found", "default", "global-error"}, f.stem)
	}},
	{Endpoint, "a SvelteKit server route", func(f facts) bool {
		return f.has("routes") && strings.HasPrefix(f.base, "+server.")
	}},
	{Page, "a SvelteKit page or layout", func(f facts) bool {
		return f.has("routes") && (strings.HasPrefix(f.base, "+page") || strings.HasPrefix(f.base, "+layout"))
	}},
	{Endpoint, "an API route under pages/api", func(f facts) bool {
		return f.rootedUnder("pages", "api") || f.rootedUnder("server", "api")
	}},
	{Page, "wraps every page, as pages/_app and pages/_document do", func(f facts) bool {
		return f.has("pages") && strings.HasPrefix(f.base, "_") && f.extIs(script...)
	}},
	{Page, "a file under pages/, which is a page by convention", func(f facts) bool {
		return f.has("pages") && (f.extIs(markup...) || f.extIs(script...) || f.ext == "mdx")
	}},
	{Page, "a file under app/routes, which Remix serves as a page", func(f facts) bool {
		return f.rootedUnder("app", "routes") && f.extIs(markup...)
	}},
	{Endpoint, "a script under app/routes, most likely a Remix resource route", func(f facts) bool {
		return f.rootedUnder("app", "routes") && f.extIs(script...)
	}},

	// What the project is built from, before documentation: a
	// requirements.txt is a list of packages, not prose.
	{Dependency, "pins the project's dependencies", func(f facts) bool {
		return slices.Contains([]string{
			"go.mod", "go.sum", "package.json", "package-lock.json", "yarn.lock", "pnpm-lock.yaml",
			"bun.lockb", "bun.lock", "cargo.toml", "cargo.lock", "gemfile", "gemfile.lock",
			"poetry.lock", "pyproject.toml", "requirements.txt", "composer.json", "composer.lock",
			"pnpm-workspace.yaml",
		}, f.base)
	}},

	// Things that are not code at all.
	{Docs, "documentation", func(f facts) bool {
		return f.extIs("md", "rst", "adoc") ||
			(f.ext == "mdx" && f.in("docs", "doc", "documentation", "content")) ||
			f.in("docs", "doc", "documentation") ||
			slices.Contains([]string{"license", "licence", "changelog", "authors", "contributors", "notice"}, f.stem)
	}},

	// The database.
	{Migration, "a migration", func(f facts) bool {
		return f.in("migrations", "migrate", "migration") || f.under("alembic", "versions") ||
			strings.HasSuffix(f.base, ".up.sql") || strings.HasSuffix(f.base, ".down.sql")
	}},
	{Migration, "the data model that migrations are made from", func(f facts) bool {
		return f.ext == "prisma"
	}},

	{Tooling, "how the project is checked or built, not how it runs", func(f facts) bool {
		return f.in(".github", ".gitlab", ".circleci", ".husky", ".vscode", ".idea", ".devcontainer") ||
			slices.Contains([]string{
				".gitignore", ".gitattributes", ".editorconfig", "codeowners", ".golangci.yml",
				".golangci.yaml", ".prettierrc", ".prettierignore", ".eslintrc", ".eslintignore",
				".gitlab-ci.yml", "renovate.json", "turbo.json", "nx.json", "biome.json",
				"makefile", ".npmrc", ".nvmrc", ".tool-versions", "tsconfig.json", "lefthook.yml",
			}, f.base) ||
			strings.HasPrefix(f.base, ".eslintrc") || strings.HasPrefix(f.base, ".prettierrc") ||
			strings.HasPrefix(f.base, "eslint.config.") || strings.HasPrefix(f.base, "prettier.config.") ||
			strings.HasPrefix(f.base, "vitest.config.") || strings.HasPrefix(f.base, "jest.config.") ||
			strings.HasPrefix(f.base, "playwright.config.")
	}},
	{Generated, "generated from something else, which is where the change is", func(f facts) bool {
		return f.in("dist", "vendor", "node_modules", "__generated__", "generated") ||
			strings.HasSuffix(f.base, ".pb.go") || strings.HasSuffix(f.stem, "_gen") ||
			strings.Contains(f.base, ".gen.") || strings.Contains(f.base, ".generated.") ||
			strings.Contains(f.base, ".min.")
	}},

	// What a page looks like and shows.
	{Style, "a stylesheet", func(f facts) bool {
		return f.extIs("css", "scss", "sass", "less", "styl", "pcss") ||
			strings.HasPrefix(f.base, "tailwind.config.") || strings.HasPrefix(f.base, "postcss.config.")
	}},
	{Asset, "an image, a font or other media", func(f facts) bool {
		return f.extIs(images...) || f.extIs(fonts...) || f.extIs(media...)
	}},
	{Asset, "text a user reads, in a translation file", func(f facts) bool {
		return f.in("locales", "locale", "i18n", "messages", "translations", "lang") &&
			f.extIs("json", "yaml", "yml", "po", "properties", "ts")
	}},
	{Component, "a component", func(f facts) bool {
		return f.extIs(markup...)
	}},
	{Component, "a template, which renders part of a page", func(f facts) bool {
		return f.extIs(template...)
	}},

	// How it runs.
	{Config, "configures how the application runs", func(f facts) bool {
		return strings.HasPrefix(f.base, ".env") || strings.HasPrefix(f.base, "dockerfile") ||
			strings.HasPrefix(f.base, "docker-compose") || strings.HasPrefix(f.base, "compose.") ||
			f.base == ".pit.yaml" || strings.Contains(f.base, ".config.") ||
			f.extIs("json", "yaml", "yml", "toml", "ini", "conf")
	}},

	{Code, "source code", func(f facts) bool {
		return f.extIs(source...)
	}},
}

// kindOf applies the rules in order.
func kindOf(p string) (Kind, string) {
	f := factsOf(p)
	for _, r := range rules {
		if r.match(f) {
			return r.kind, r.reason
		}
	}
	return Other, "no rule recognised it"
}
