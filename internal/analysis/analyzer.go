package analysis

import (
	"cmp"
	"context"
	"io/fs"
	"slices"

	"github.com/thannoz/pit/internal/errs"
)

// Analyzer knows how one framework turns files into addresses: which
// file serves /checkout/address, which one answers POST /api/orders.
//
// The frameworks live in internal/analysis/routes and this package does
// not import it -- it cannot, because routes needs the types declared
// here, and Go refuses the cycle. That is what makes a heuristic
// exchangeable without touching the code that uses it: the compiler,
// not a convention, keeps the two apart.
type Analyzer interface {
	// Name is the framework, as a reader would write it: "Next.js".
	// It says which heuristic an address came from and which one
	// failed.
	Name() string
	// Routes lists every address the project serves, with the file
	// that serves it. Not only the changed ones: a changed component
	// leads to a page only through files that did not change, and
	// following it there needs to know where the pages are.
	//
	// A project that does not use the framework has no routes, which
	// is not an error.
	Routes(ctx context.Context, fsys fs.FS) ([]Route, error)
}

// Route is one address a project serves.
type Route struct {
	// Path is the address as the framework writes it, dynamic
	// segments and all: /orders/[id].
	Path string
	// File is the file that serves it, relative to the repository's
	// root, the way a diff names it.
	File string
	// Kind is Page or Endpoint.
	Kind Kind
}

// Entrypoint is an address a reviewer should visit, and why.
type Entrypoint struct {
	// Path is the address, as a Route has it.
	Path string
	// Kind is Page or Endpoint.
	Kind Kind
	// Files are the changed files that lead here, sorted.
	Files []string
	// Analyzer is the heuristic that found it.
	Analyzer string
}

// Guide is what a pull request asks a reviewer to look at.
type Guide struct {
	// Entrypoints are the addresses its changes lead to, sorted by
	// path.
	Entrypoints []Entrypoint
	// Unplaced are the files on the checklist that lead nowhere any
	// heuristic knows. They are still the reviewer's business: a
	// changed file nobody can place is not a changed file nobody has
	// to look at.
	Unplaced []File
}

// Entrypoints asks each analyzer where the project's routes are and
// matches them against the changed files.
//
// Only files on the checklist count, so review.ignore keeps a file out
// of the guide the same way it keeps it off the list. An address two
// heuristics both find is listed once; the first analyzer to find it
// names it, so the order of the analyzers is their precedence.
//
// A heuristic that fails fails the guide. A guide that silently lacks
// one framework's pages looks exactly like a pull request that does not
// touch them.
func Entrypoints(ctx context.Context, fsys fs.FS, files []File, analyzers []Analyzer) (Guide, error) {
	changed := map[string]bool{}
	for _, f := range files {
		if f.OnChecklist() {
			changed[f.Path] = true
		}
	}

	var g Guide
	at := map[string]int{} // path -> index into g.Entrypoints
	placed := map[string]bool{}
	for _, a := range analyzers {
		routes, err := a.Routes(ctx, fsys)
		if err != nil {
			return Guide{}, errs.Wrap(err, "finding %s routes", a.Name())
		}
		for _, r := range routes {
			if !changed[r.File] {
				continue
			}
			placed[r.File] = true
			i, ok := at[r.Path]
			if !ok {
				i = len(g.Entrypoints)
				at[r.Path] = i
				g.Entrypoints = append(g.Entrypoints, Entrypoint{Path: r.Path, Kind: r.Kind, Analyzer: a.Name()})
			}
			if !slices.Contains(g.Entrypoints[i].Files, r.File) {
				g.Entrypoints[i].Files = append(g.Entrypoints[i].Files, r.File)
			}
		}
	}

	for _, f := range files {
		if changed[f.Path] && !placed[f.Path] {
			g.Unplaced = append(g.Unplaced, f)
		}
	}
	for i := range g.Entrypoints {
		slices.Sort(g.Entrypoints[i].Files)
	}
	slices.SortStableFunc(g.Entrypoints, func(a, b Entrypoint) int { return cmp.Compare(a.Path, b.Path) })
	return g, nil
}
