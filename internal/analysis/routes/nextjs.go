package routes

import (
	"cmp"
	"context"
	"encoding/json"
	"io/fs"
	"path"
	"slices"
	"strings"

	"github.com/thannoz/pit/internal/analysis"
)

// NextJS reads the routes of Next.js applications, both routers: the
// App Router under app/ and the Pages Router under pages/.
//
// The rules follow Next.js itself (16.2): route groups and parallel
// slots are not part of the address, a folder or file whose name starts
// with an underscore is private to the App Router, app/ and pages/ are
// looked for next to package.json before src/, and an intercepting
// route answers at the address it intercepts.
//
// What the files alone cannot say is left alone: rewrites, redirects and
// middleware in the application's code, a basePath, i18n prefixes, and
// pageExtensions other than the usual ones.
type NextJS struct{}

// Name implements analysis.Analyzer.
func (NextJS) Name() string { return "Next.js" }

// Routes implements analysis.Analyzer. Every directory with a
// package.json that depends on next is an application; a monorepo has
// several, and each has its own app/ and pages/.
func (NextJS) Routes(ctx context.Context, fsys fs.FS) ([]analysis.Route, error) {
	apps, err := nextApps(ctx, fsys)
	if err != nil {
		return nil, err
	}

	var out []analysis.Route
	for _, root := range apps {
		if dir := findDir(fsys, root, "app"); dir != "" {
			routes, err := appRouter(ctx, fsys, dir)
			if err != nil {
				return nil, err
			}
			out = append(out, routes...)
		}
		if dir := findDir(fsys, root, "pages"); dir != "" {
			routes, err := pagesRouter(ctx, fsys, dir)
			if err != nil {
				return nil, err
			}
			out = append(out, routes...)
		}
	}
	return out, nil
}

// nextApps finds the directories whose package.json depends on next.
//
// A package.json that cannot be read as JSON is passed over rather than
// failing the search: it is as likely to be a test fixture as an
// application, and next itself could not start from it either.
func nextApps(ctx context.Context, fsys fs.FS) ([]string, error) {
	var apps []string
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.IsDir() {
			if p != "." && (d.Name() == "node_modules" || strings.HasPrefix(d.Name(), ".")) {
				return fs.SkipDir
			}
			return nil
		}
		if d.Name() != "package.json" {
			return nil
		}
		data, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		var manifest struct {
			Dependencies    map[string]string `json:"dependencies"`
			DevDependencies map[string]string `json:"devDependencies"`
		}
		if json.Unmarshal(data, &manifest) != nil {
			return nil
		}
		if _, ok := manifest.Dependencies["next"]; ok {
			apps = append(apps, path.Dir(p))
		} else if _, ok := manifest.DevDependencies["next"]; ok {
			apps = append(apps, path.Dir(p))
		}
		return nil
	})
	return apps, err
}

// findDir looks for app/ or pages/ the way Next.js does: next to
// package.json first, then under src/. The two are looked for
// separately, so pages/ at the root and src/app/ can be one
// application.
func findDir(fsys fs.FS, root, name string) string {
	for _, dir := range []string{path.Join(root, name), path.Join(root, "src", name)} {
		if info, err := fs.Stat(fsys, dir); err == nil && info.IsDir() {
			return dir
		}
	}
	return ""
}

// extensions are the ones Next.js reads pages from by default, and mdx,
// which a project only has page files in when it has configured it.
var extensions = []string{"tsx", "ts", "jsx", "js", "mdx"}

// splitName takes a file name apart into what Next.js looks at.
func splitName(name string) (stem string, ok bool) {
	ext := path.Ext(name)
	if !slices.Contains(extensions, strings.TrimPrefix(ext, ".")) || strings.HasSuffix(name, ".d.ts") {
		return "", false
	}
	return strings.TrimSuffix(name, ext), true
}

// wrappers are the App Router files that do not serve an address of
// their own but show up on every page below them.
//
// error, not-found, forbidden and unauthorized are not among them,
// though they belong to a folder the same way: they show up when
// something goes wrong on a page, and opening the page is exactly what
// does not show them. Neither is default, which fills a slot only where
// nothing else does. Better unplaced than placed where they are not.
var wrappers = []string{"layout", "template", "loading"}

func appRouter(ctx context.Context, fsys fs.FS, dir string) ([]analysis.Route, error) {
	var (
		routes  []analysis.Route
		slotted []analysis.Route // pages in a parallel slot
		wrapped []string         // files that wrap the pages below them
	)
	err := fs.WalkDir(fsys, dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if p != dir && strings.HasPrefix(d.Name(), "_") {
			// Private: neither the folder nor anything in it is routed.
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}

		stem, ok := splitName(d.Name())
		if !ok {
			return nil
		}
		var kind analysis.Kind
		switch {
		case stem == "page":
			kind = analysis.Page
		case stem == "route":
			kind = analysis.Endpoint
		case slices.Contains(wrappers, stem):
			wrapped = append(wrapped, p)
			return nil
		default:
			return nil
		}

		folders := strings.Split(strings.Trim(strings.TrimPrefix(path.Dir(p), dir), "/"), "/")
		r := analysis.Route{Path: appAddress(folders), File: p, Kind: kind}
		if slices.ContainsFunc(folders, func(f string) bool { return strings.HasPrefix(f, "@") }) {
			slotted = append(slotted, r)
		} else {
			routes = append(routes, r)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	// A page in a parallel slot is rendered next to the page at its
	// address, not instead of one. Where there is such a page, that is
	// where to look. Where there is none, opening the address shows no
	// page at all, and a link to it would be a link to nothing -- the
	// file is better left unplaced than placed wrongly.
	served := map[string]bool{}
	for _, r := range routes {
		served[r.Path] = true
	}
	for _, r := range slotted {
		if served[r.Path] {
			routes = append(routes, r)
		}
	}
	return append(routes, nearest(routes, wrapped)...), nil
}

// appAddress turns the folders between app/ and a page into the address
// the page answers at.
func appAddress(folders []string) string {
	var segments []string
	for _, f := range folders {
		if f == "" {
			continue
		}
		if up, rest, ok := interception(f); ok {
			// An intercepting route answers at the address it
			// intercepts: (..) is one segment up from here, (...) the
			// root. Groups and slots are not segments, so they are not
			// counted.
			if up < 0 || up > len(segments) {
				segments = nil
			} else {
				segments = segments[:len(segments)-up]
			}
			f = rest
		}
		switch {
		case strings.HasPrefix(f, "(") && strings.HasSuffix(f, ")"):
			continue // a route group
		case strings.HasPrefix(f, "@"):
			continue // a parallel slot
		}
		segment, optional := segmentOf(f)
		if optional {
			// An optional catch-all matches nothing as well, so the
			// address without it is the one that needs no value.
			break
		}
		segments = append(segments, segment)
	}
	return "/" + strings.Join(segments, "/")
}

// interception recognises the markers of an intercepting route and says
// how many segments each goes up; -1 is the root.
func interception(folder string) (up int, rest string, ok bool) {
	// The order is Next.js's own: the first marker that matches wins.
	for _, m := range []struct {
		marker string
		up     int
	}{{"(..)(..)", 2}, {"(.)", 0}, {"(..)", 1}, {"(...)", -1}} {
		if rest, ok := strings.CutPrefix(folder, m.marker); ok {
			return m.up, rest, true
		}
	}
	return 0, folder, false
}

// segmentOf translates one folder or file name into a segment of an
// address, in the placeholder syntax analysis.Route uses. optional is
// true for [[...name]], which matches the empty rest as well.
func segmentOf(name string) (segment string, optional bool) {
	switch {
	case strings.HasPrefix(name, "[[...") && strings.HasSuffix(name, "]]"):
		return "", true
	case strings.HasPrefix(name, "[...") && strings.HasSuffix(name, "]"):
		return "{" + name[4:len(name)-1] + "...}", false
	case strings.HasPrefix(name, "[") && strings.HasSuffix(name, "]"):
		return "{" + name[1:len(name)-1] + "}", false
	case len(name) >= 3 && strings.EqualFold(name[:3], "%5F"):
		// How Next.js spells a segment that starts with an underscore
		// without making the folder private.
		return "_" + name[3:], false
	default:
		return name, false
	}
}

func pagesRouter(ctx context.Context, fsys fs.FS, dir string) ([]analysis.Route, error) {
	var (
		routes  []analysis.Route
		wrapped []string
	)
	err := fs.WalkDir(fsys, dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		stem, ok := splitName(d.Name())
		if !ok {
			return nil
		}

		rel := strings.Trim(strings.TrimPrefix(path.Dir(p), dir), "/")
		if rel == "" && strings.HasPrefix(stem, "_") {
			// _app, _document and _error wrap every page.
			wrapped = append(wrapped, p)
			return nil
		}

		var folders []string
		if rel != "" {
			folders = strings.Split(rel, "/")
		}
		if stem != "index" {
			folders = append(folders, stem)
		}

		kind := analysis.Page
		if rel == "api" || strings.HasPrefix(rel, "api/") {
			kind = analysis.Endpoint
		}

		var segments []string
		for _, f := range folders {
			segment, optional := segmentOf(f)
			if optional {
				break
			}
			segments = append(segments, segment)
		}
		routes = append(routes, analysis.Route{Path: "/" + strings.Join(segments, "/"), File: p, Kind: kind})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return append(routes, nearest(routes, wrapped)...), nil
}

// nearest gives each wrapping file one page to be seen on: the one
// below it that is quickest to open -- fewest placeholders to fill, then
// fewest segments.
//
// One, not all of them. A root layout wraps every page there is, and a
// checklist that lists them all because the layout changed says less
// than one that lists the page where the layout can be looked at.
func nearest(routes []analysis.Route, wrapped []string) []analysis.Route {
	var out []analysis.Route
	for _, w := range wrapped {
		dir := path.Dir(w) + "/"
		var best *analysis.Route
		for i, r := range routes {
			if r.Kind != analysis.Page || !strings.HasPrefix(r.File, dir) {
				continue
			}
			if best == nil || closer(r, *best) {
				best = &routes[i]
			}
		}
		if best != nil {
			out = append(out, analysis.Route{Path: best.Path, File: w, Kind: analysis.Page})
		}
	}
	return out
}

func closer(a, b analysis.Route) bool {
	holes := func(r analysis.Route) int { return strings.Count(r.Path, "{") }
	depth := func(r analysis.Route) int { return strings.Count(strings.TrimSuffix(r.Path, "/"), "/") }
	return cmp.Or(
		cmp.Compare(holes(a), holes(b)),
		cmp.Compare(depth(a), depth(b)),
		cmp.Compare(a.Path, b.Path),
	) < 0
}
