package routes

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/thannoz/pit/internal/analysis"
	"github.com/thannoz/pit/internal/diff"
)

// SvelteKit reads the routes of SvelteKit applications from their
// src/routes.
//
// The rules follow SvelteKit itself (2.x and the 3.0 prereleases): a
// folder is a segment of the address, except a (group); a file whose
// name starts with + is a route file, the rest are the application's
// own modules kept next to them; [[optional]] matches nothing as well,
// [...rest] does not need to, and [x+2e] is how a character that
// cannot be in a file name is written.
//
// A +server file answers for each HTTP method it exports, and a
// +page.server file's actions answer POST: a change to one of them is
// looked at with that method, not by opening the page.
//
// What the files alone cannot say is left alone, as for Next.js: a
// reroute hook in the application's code, and a base path that is not
// a literal. The handle hook is not among them. SvelteKit picks the
// route before handle runs; handle can answer differently, the way any
// load function can, but not send the address to another route.
type SvelteKit struct{}

// Name implements analysis.Analyzer.
func (SvelteKit) Name() string { return "SvelteKit" }

// Routes implements analysis.Analyzer. Every directory with a
// package.json that depends on @sveltejs/kit is an application.
func (SvelteKit) Routes(ctx context.Context, fsys fs.FS) ([]analysis.Route, error) {
	apps, err := appsUsing(ctx, fsys, "@sveltejs/kit")
	if err != nil {
		return nil, err
	}
	var out []analysis.Route
	for _, root := range apps {
		cfg := readKitConfig(fsys, root)
		routes, err := kitRoutes(ctx, fsys, cfg)
		if err != nil {
			return nil, err
		}
		for i, r := range routes {
			routes[i].Doubts = append(slices.Clip(r.Doubts), cfg.unread...)
			if cfg.base != "" {
				routes[i].Path = strings.TrimSuffix(cfg.base+r.Path, "/")
			}
		}
		out = append(out, routes...)
	}
	return out, nil
}

// kitConfig is what an application's configuration says about where
// its routes are and where they answer, as far as its text can be read.
type kitConfig struct {
	root string
	// routes and params are the directories of the route files and of
	// the parameter matchers.
	routes, params string
	// components are the extensions of route files that are
	// components, modules those of the ones that are code.
	components, modules []string
	// base comes before every address, when it is a literal.
	base string
	// unread are what pit cannot read, each a doubt on every address.
	unread []analysis.Doubt
	// aliases are the import aliases SvelteKit sets up: $lib and the
	// ones in the alias option, as tsconfig paths relative to the
	// tree's root.
	aliases map[string][]string
}

// The configuration lives in svelte.config.js up to SvelteKit 2, and
// in the options of the sveltekit() Vite plugin from 3.0 on. Both are
// read; an application has one or the other.
var kitConfigFiles = []string{
	"svelte.config.js", "svelte.config.mjs", "svelte.config.ts", "svelte.config.cjs",
	"vite.config.ts", "vite.config.js", "vite.config.mts", "vite.config.mjs", "vite.config.cjs",
}

var (
	extensionsKey       = regexp.MustCompile(`\bextensions\s*:\s*\[([^\]]*)\]`)
	moduleExtensionsKey = regexp.MustCompile(`\bmoduleExtensions\s*:\s*\[([^\]]*)\]`)
	aliasEntry          = regexp.MustCompile(`(?:["'` + "`" + `]([^"'` + "`" + `]+)["'` + "`" + `]|([\w$@/*-]+))\s*:\s*["'` + "`" + `]([^"'` + "`" + `]+)["'` + "`" + `]`)
	reroute             = regexp.MustCompile(`\bexport\s+(?:(?:async\s+)?function|const|let|var)\s+reroute\b|\bexport\s*\{[^}]*\breroute\b`)
)

func readKitConfig(fsys fs.FS, root string) kitConfig {
	c := kitConfig{
		root:       root,
		routes:     path.Join(root, "src/routes"),
		params:     path.Join(root, "src/params"),
		components: []string{".svelte"},
		modules:    []string{".js", ".ts"},
		aliases:    map[string][]string{},
	}
	lib, hooks := "src/lib", "src/hooks"
	var aliases map[string]string
	for _, name := range kitConfigFiles {
		data, err := fs.ReadFile(fsys, path.Join(root, name))
		if err != nil {
			continue
		}
		src := stripComments(string(data))
		if files := objectAt(src, "files"); files != "" {
			if v, ok := literalAt(files, "routes"); ok {
				c.routes = path.Join(root, v)
			}
			if v, ok := literalAt(files, "params"); ok {
				c.params = path.Join(root, v)
			}
			if v, ok := literalAt(files, "lib"); ok {
				lib = v
			}
			if v, ok := literalAt(objectAt(files, "hooks"), "universal"); ok {
				hooks = v
			}
		}
		if paths := objectAt(src, "paths"); paths != "" {
			if v, ok := literalAt(paths, "base"); ok {
				c.base = strings.TrimSuffix(v, "/")
			} else if regexp.MustCompile(`\bbase\s*:`).MatchString(paths) {
				c.unread = append(c.unread, analysis.Doubt{Confidence: analysis.Uncertain,
					Reason: name + " sets paths.base in a way pit cannot read; every address starts with it"})
			}
		}
		// A preprocessor's own extensions, mdsvex's for one, are the
		// ones SvelteKit is given too; taking every list there is
		// takes those.
		for _, m := range extensionsKey.FindAllStringSubmatch(src, -1) {
			for _, q := range quoted.FindAllStringSubmatch(m[1], -1) {
				if !slices.Contains(c.components, q[1]) {
					c.components = append(c.components, q[1])
				}
			}
		}
		if m := moduleExtensionsKey.FindStringSubmatch(src); m != nil {
			for _, q := range quoted.FindAllStringSubmatch(m[1], -1) {
				if !slices.Contains(c.modules, q[1]) {
					c.modules = append(c.modules, q[1])
				}
			}
		}
		if obj := objectAt(src, "alias"); obj != "" {
			aliases = map[string]string{}
			for _, m := range aliasEntry.FindAllStringSubmatch(obj[1:len(obj)-1], -1) {
				aliases[m[1]+m[2]] = m[3]
			}
		}
	}

	// $lib, and every alias as SvelteKit writes it into the tsconfig it
	// generates: a name for a directory stands for what is in it too.
	if aliases == nil {
		aliases = map[string]string{}
	}
	if _, ok := aliases["$lib"]; !ok {
		aliases["$lib"] = lib
	}
	for key, target := range aliases {
		c.aliases[key] = []string{path.Join(root, target)}
		if !strings.HasSuffix(key, "/*") && !strings.Contains(key, "*") {
			c.aliases[key+"/*"] = []string{path.Join(root, target) + "/*"}
		}
	}

	for _, f := range entryFiles(path.Join(root, hooks), c.modules) {
		if data, err := fs.ReadFile(fsys, f); err == nil && reroute.MatchString(stripComments(string(data))) {
			c.unread = append(c.unread, analysis.Doubt{Confidence: analysis.Uncertain,
				Reason: f + " reroutes requests in code: which route answers an address is decided there"})
			break
		}
	}
	return c
}

// entryFiles are the files a module path without an extension can
// mean, as SvelteKit resolves its hooks: the path with an extension, or
// its index.
func entryFiles(base string, extensions []string) []string {
	var out []string
	for _, ext := range extensions {
		out = append(out, base+ext)
	}
	for _, ext := range extensions {
		out = append(out, path.Join(base, "index"+ext))
	}
	return out
}

// objectAt is the { ... } a key of src has as its value, or "".
func objectAt(src, key string) string {
	if src == "" {
		return ""
	}
	loc := regexp.MustCompile(`\b` + regexp.QuoteMeta(key) + `\s*:\s*\{`).FindStringIndex(src)
	if loc == nil {
		return ""
	}
	start := loc[1] - 1
	end := closing(src[start:])
	if end < 0 {
		return src[start:]
	}
	return src[start : start+end+1]
}

// literalAt is the string a key of src has as its value, when it is a
// literal.
func literalAt(src, key string) (string, bool) {
	m := regexp.MustCompile(`\b` + regexp.QuoteMeta(key) + `\s*:\s*["'` + "`" + `]([^"'` + "`" + `$]*)["'` + "`" + `]`).FindStringSubmatch(src)
	if m == nil {
		return "", false
	}
	return m[1], true
}

var (
	kitComponent = regexp.MustCompile(`^\+(page|layout|error)(@[^.]*)?$`)
	kitModule    = regexp.MustCompile(`^\+(server|page|layout)(\.server)?$`)
	httpMethods  = []string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"}
)

// kitFile is a route file and what it is.
type kitFile struct {
	path    string
	dir     string // the folders between src/routes and it
	kind    string // page, layout, error or server
	server  bool   // +page.server, +layout.server
	address string
	doubts  []analysis.Doubt
	// matchers are the parameter matchers its address uses.
	matchers []string
}

func kitRoutes(ctx context.Context, fsys fs.FS, c kitConfig) ([]analysis.Route, error) {
	var files []kitFile
	err := fs.WalkDir(fsys, c.routes, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == c.routes && errors.Is(err, fs.ErrNotExist) {
				return fs.SkipDir
			}
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.IsDir() || !strings.HasPrefix(d.Name(), "+") {
			return nil
		}
		f, ok := c.routeFile(d.Name())
		if !ok {
			return nil
		}
		f.path, f.dir = p, strings.Trim(strings.TrimPrefix(path.Dir(p), c.routes), "/")
		f.address, f.matchers, f.doubts = kitAddress(f.dir, false)
		files = append(files, f)
		return nil
	})
	if err != nil {
		return nil, err
	}

	var routes []analysis.Route
	var wrapped []string
	for _, f := range files {
		switch {
		case f.kind == "layout":
			wrapped = append(wrapped, f.path)
		case f.kind == "error":
			// It shows when a page fails, which opening the page is
			// exactly what does not show; unplaced, as in Next.js.
		case f.kind == "page" && f.server, f.kind == "server":
			found, err := kitScript(fsys, f)
			if err != nil {
				return nil, err
			}
			routes = append(routes, found...)
		default:
			routes = append(routes, analysis.Route{Path: f.address, File: f.path, Kind: analysis.Page, Method: "GET", Doubts: f.doubts})
		}
	}
	routes = append(routes, nearest(routes, wrapped)...)
	return append(routes, matched(fsys, c, files)...), nil
}

// routeFile tells what a file whose name starts with + is.
func (c kitConfig) routeFile(name string) (kitFile, bool) {
	// +page.test.ts and +page.stories.svelte are next to a route, not
	// part of it: the patterns take a name with nothing between + and
	// the extension, so they pass them over, as SvelteKit does.
	for _, ext := range c.components {
		if stem, ok := strings.CutSuffix(name, ext); ok {
			if m := kitComponent.FindStringSubmatch(stem); m != nil {
				return kitFile{kind: m[1]}, true
			}
		}
	}
	for _, ext := range c.modules {
		if stem, ok := strings.CutSuffix(name, ext); ok {
			if m := kitModule.FindStringSubmatch(stem); m != nil {
				return kitFile{kind: m[1], server: m[2] != ""}, true
			}
		}
	}
	return kitFile{}, false
}

var (
	kitGroup    = regexp.MustCompile(`^\(.+\)$`)
	kitOptional = regexp.MustCompile(`^\[\[([\w-]+)(?:=([\w-]+))?\]\]$`)
	kitRest     = regexp.MustCompile(`^\[\.\.\.([\w-]+)(?:=([\w-]+))?\]$`)
	kitParam    = regexp.MustCompile(`^\[([\w-]+)(?:=([\w-]+))?\]$`)
	kitBracket  = regexp.MustCompile(`\[\[[^\]]*\]\]|\[[^\]]*\]`)
	kitMatcher  = regexp.MustCompile(`=([\w-]+)\]`)
)

// kitAddress turns the folders between src/routes and a route file into
// the address it answers at. An [[optional]] segment is left out, unless
// withOptional asks for it as a placeholder.
func kitAddress(dir string, withOptional bool) (address string, matchers []string, doubts []analysis.Doubt) {
	var segments []string
	for _, f := range strings.Split(dir, "/") {
		if f == "" || kitGroup.MatchString(f) {
			continue
		}
		for _, m := range kitMatcher.FindAllStringSubmatch(f, -1) {
			matchers = append(matchers, m[1])
		}
		switch {
		case kitOptional.MatchString(f):
			// It matches nothing as well, so the address without it is
			// the one that needs no value.
			if withOptional {
				segments = append(segments, "{"+kitOptional.FindStringSubmatch(f)[1]+"}")
			}
		case kitRest.MatchString(f):
			// A rest matches nothing as well, but the address without
			// it is often another route's: src/routes/[...path] would
			// be /, which the root page answers.
			segments = append(segments, "{"+kitRest.FindStringSubmatch(f)[1]+"...}")
		case kitParam.MatchString(f):
			segments = append(segments, "{"+kitParam.FindStringSubmatch(f)[1]+"}")
		default:
			segment, partial := kitSegment(f)
			if partial {
				doubts = append(doubts, analysis.Doubt{Confidence: analysis.Likely, Reason: fmt.Sprintf(
					"%s puts a value inside a segment; pit fills in only whole segments, so it makes no link here", f)})
			}
			segments = append(segments, segment)
		}
	}
	return "/" + strings.Join(segments, "/"), matchers, doubts
}

// kitSegment reads a folder name with brackets inside it: [x+2e] is a
// character, anything else a value, which becomes "…".
func kitSegment(f string) (segment string, partial bool) {
	segment = kitBracket.ReplaceAllStringFunc(f, func(b string) string {
		inner := strings.Trim(b, "[]")
		if code, ok := strings.CutPrefix(inner, "x+"); ok {
			return escaped(code)
		}
		if code, ok := strings.CutPrefix(inner, "u+"); ok {
			return escaped(code)
		}
		partial = true
		return "…"
	})
	return segment, partial
}

// escaped decodes [x+2e] and [u+0041-0042] the way SvelteKit does, and
// writes what cannot stand in an address unescaped as it has to be
// sent.
func escaped(code string) string {
	var b strings.Builder
	for _, part := range strings.Split(code, "-") {
		n, err := strconv.ParseInt(part, 16, 32)
		if err != nil {
			return "[" + code + "]"
		}
		switch r := rune(n); r {
		case '%', '/', '?', '#':
			fmt.Fprintf(&b, "%%%02X", r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// kitScript reads the routes of a +server or +page.server file: one
// for each thing it exports, with its method, so that a change to the
// POST handler is looked at with POST.
func kitScript(fsys fs.FS, f kitFile) ([]analysis.Route, error) {
	kind, whole := analysis.Endpoint, ""
	if f.kind == "page" {
		kind, whole = analysis.Page, "GET"
	}
	data, err := fs.ReadFile(fsys, f.path)
	if err != nil {
		return nil, err
	}
	m := parseModule(string(data), true)
	route := func(method string, lines diff.Range) analysis.Route {
		return analysis.Route{Path: f.address, File: f.path, Kind: kind, Method: method, Lines: lines, Doubts: f.doubts}
	}
	methodOf := func(name string) string {
		if f.kind == "page" {
			if name == "actions" {
				return "POST"
			}
			return "GET"
		}
		if slices.Contains(httpMethods, name) {
			return name
		}
		return "" // fallback, and page options such as prerender
	}

	var out []analysis.Route
	for _, name := range sortedNames(m.exports) {
		d := m.exports[name]
		if !d.isType {
			out = append(out, route(methodOf(name), d.lines))
		}
	}
	// What it exports from elsewhere has no lines here; a change to
	// the file is a change to it.
	for _, name := range sortedNames(m.reexports) {
		out = append(out, route(methodOf(name), diff.Range{}))
	}
	if len(out) == 0 {
		out = append(out, route(whole, diff.Range{}))
	}
	return out, nil
}

// matched are the routes a parameter matcher decides: src/params/id.ts
// is what makes /orders/[id=id] match /orders/42 and not /orders/new.
//
// The address is the one the matcher is asked about. Where it guards an
// optional segment, that is the address with the segment in it:
// /photos/[[assetId=id]] is /photos without it, which never asks id.
func matched(fsys fs.FS, c kitConfig, files []kitFile) []analysis.Route {
	var out []analysis.Route
	seen := map[string]bool{}
	for _, f := range files {
		if f.kind == "layout" || f.kind == "error" {
			continue
		}
		address, _, doubts := kitAddress(f.dir, true)
		for _, name := range f.matchers {
			for _, file := range entryFiles(path.Join(c.params, name), c.modules)[:len(c.modules)] {
				if _, err := fs.Stat(fsys, file); err != nil || seen[file+" "+address] {
					continue
				}
				seen[file+" "+address] = true
				kind := analysis.Page
				if f.kind == "server" {
					kind = analysis.Endpoint
				}
				out = append(out, analysis.Route{Path: address, File: file, Kind: kind, Doubts: doubts})
			}
		}
	}
	return out
}

func sortedNames[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
