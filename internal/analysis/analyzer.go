package analysis

import (
	"cmp"
	"context"
	"io/fs"
	"slices"

	"github.com/thannoz/pit/internal/diff"
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
	// Path is the address, with the parts that change from one
	// request to the next as placeholders: {id} for one segment,
	// {path...} for the rest of the address. Every heuristic writes
	// them this way, whatever its framework calls them, so that Fill
	// works on all of them. It is the syntax of Go's own router.
	Path string
	// File is the file that serves it, relative to the repository's
	// root, the way a diff names it.
	File string
	// Kind is Page or Endpoint.
	Kind Kind
	// Method is the HTTP method, where the route is registered for
	// one. Empty means any, or that the framework does not say.
	Method string
	// Lines narrows the route to part of File: the function that
	// handles it, the call that registers it. A file that registers
	// forty routes has changed for one of them when the change is on
	// its line, not for all forty. Zero means the whole file.
	Lines diff.Range
}

// Entrypoint is an address a reviewer should visit, and why.
type Entrypoint struct {
	// Path is the address, as a Route has it.
	Path string
	// Kind is Page or Endpoint.
	Kind Kind
	// Methods are the HTTP methods the changes reach, sorted; empty
	// when no route said.
	Methods []string
	// Files are the changed files that lead here, sorted.
	Files []string
	// Via are the ways a changed file that does not serve the address
	// itself leads here: the file, what uses it, and so on up to the
	// file that serves it. The shortest one for each such file.
	Via []Trail
	// Analyzer is the heuristic that found it.
	Analyzer string
}

// Trail is a changed file and the files that use it, in order, up to
// one that serves an address.
type Trail []string

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
	// Wide are the changed files that reach more addresses than a
	// guide can list. Their nearest MaxReach addresses are in
	// Entrypoints; how many there are in all is here.
	Wide []Reach
}

// Reach is how many addresses one changed file leads to.
type Reach struct {
	File      string
	Addresses int
}

// MaxReach is how many addresses one changed file may add to a guide.
// A file every page uses has changed every page, and a list of all of
// them is a list nobody works through; the few nearest to the change,
// and the number of the rest, say more.
const MaxReach = 10

// MaxDepth is how many files away from the change the guide looks for
// an address. A component inside a form inside a section inside a page
// is four; a change further away than this reaches so much that one
// more address would say less than none.
const MaxDepth = 8

// Entrypoints works out which addresses the changed files lead to:
// the ones they serve themselves, and the ones served by files that use
// them, followed up the links until an address is found.
//
// Only files on the checklist count, so review.ignore keeps a file out
// of the guide the same way it keeps it off the list. An address two
// heuristics both find is listed once; the first analyzer to find it
// names it, so the order of the analyzers is their precedence.
//
// A heuristic that fails fails the guide. A guide that silently lacks
// one framework's pages looks exactly like a pull request that does not
// touch them.
func Entrypoints(ctx context.Context, fsys fs.FS, files []File, analyzers []Analyzer, linkers []Linker) (Guide, error) {
	var routes []ranked
	served := map[string][]int{} // file -> indexes into routes
	for rank, a := range analyzers {
		found, err := a.Routes(ctx, fsys)
		if err != nil {
			return Guide{}, errs.Wrap(err, "finding %s routes", a.Name())
		}
		for _, r := range found {
			served[r.File] = append(served[r.File], len(routes))
			routes = append(routes, ranked{Route: r, by: a.Name(), rank: rank})
		}
	}
	users := map[string][]Link{} // file -> the links to it
	for _, l := range linkers {
		found, err := l.Links(ctx, fsys)
		if err != nil {
			return Guide{}, errs.Wrap(err, "reading how %s files use each other", l.Name())
		}
		for _, link := range found {
			users[link.To] = append(users[link.To], link)
		}
	}

	g := guideBuilder{at: map[string]int{}}
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return Guide{}, err
		}
		if !f.OnChecklist() {
			continue
		}
		if !g.follow(f, routes, served, users) {
			g.Unplaced = append(g.Unplaced, f)
		}
	}
	return g.done(), nil
}

type ranked struct {
	Route
	by   string
	rank int
}

type guideBuilder struct {
	Guide
	at    map[string]int // address -> index into Entrypoints
	ranks []int
}

// follow walks from the lines a file changed to the addresses they
// reach, breadth first, so the trail kept for each address is the
// shortest. It reports whether any was found.
//
// A step goes from lines of one file to the places that use them. Where
// those places serve an address, the walk ends there: past a page lie
// the pages that link to it, which is not what the change touched. The
// same file and lines are never visited twice, which is what makes a
// cycle of imports end.
func (g *guideBuilder) follow(f File, routes []ranked, served map[string][]int, users map[string][]Link) bool {
	type step struct {
		file  string
		lines diff.Range
		trail Trail
	}
	var queue []step
	for _, r := range changedLines(f.File) {
		queue = append(queue, step{file: f.Path, lines: r, trail: Trail{f.Path}})
	}
	seen := map[spot]bool{}
	for _, s := range queue {
		seen[spot{s.file, s.lines}] = true
	}
	type hit struct {
		route int
		trail Trail
	}
	var hits []hit
	nearest := map[string]int{} // address -> fewest steps to it
	for len(queue) > 0 {
		s := queue[0]
		queue = queue[1:]

		found := false
		for _, i := range served[s.file] {
			if overlaps(routes[i].Lines, s.lines) {
				hits = append(hits, hit{route: i, trail: s.trail})
				if _, ok := nearest[routes[i].Path]; !ok {
					nearest[routes[i].Path] = len(s.trail)
				}
				found = true
			}
		}
		if found {
			continue
		}
		if len(s.trail) > MaxDepth {
			continue
		}
		for _, l := range users[s.file] {
			if !overlaps(l.Target, s.lines) {
				continue
			}
			next := spot{file: l.From, lines: l.At}
			if seen[next] {
				continue
			}
			seen[next] = true
			trail := s.trail
			if l.From != trail[len(trail)-1] {
				// One function using another in the same file is a
				// step of the walk, not a file of the trail.
				trail = append(slices.Clip(trail), l.From)
			}
			queue = append(queue, step{file: l.From, lines: l.At, trail: trail})
		}
	}

	keep := func(string) bool { return true }
	if len(nearest) > MaxReach {
		addresses := make([]string, 0, len(nearest))
		for a := range nearest {
			addresses = append(addresses, a)
		}
		slices.SortFunc(addresses, func(a, b string) int {
			return cmp.Or(cmp.Compare(nearest[a], nearest[b]), cmp.Compare(a, b))
		})
		kept := addresses[:MaxReach]
		keep = func(a string) bool { return slices.Contains(kept, a) }
		g.Wide = append(g.Wide, Reach{File: f.Path, Addresses: len(addresses)})
	}
	for _, h := range hits {
		if keep(routes[h.route].Path) {
			g.add(routes[h.route], f.Path, h.trail)
		}
	}
	return len(hits) > 0
}

// spot is a place a walk can be: lines of a file.
type spot struct {
	file  string
	lines diff.Range
}

func (g *guideBuilder) add(r ranked, changed string, trail Trail) {
	i, ok := g.at[r.Path]
	if !ok {
		i = len(g.Entrypoints)
		g.at[r.Path] = i
		g.Entrypoints = append(g.Entrypoints, Entrypoint{Path: r.Path, Kind: r.Kind, Analyzer: r.by})
		g.ranks = append(g.ranks, r.rank)
	}
	e := &g.Entrypoints[i]
	if r.rank < g.ranks[i] {
		// An earlier heuristic found it after a later one did.
		e.Analyzer, e.Kind, g.ranks[i] = r.by, r.Kind, r.rank
	}
	if !slices.Contains(e.Files, changed) {
		e.Files = append(e.Files, changed)
		if len(trail) > 1 {
			e.Via = append(e.Via, trail)
		}
	}
	if r.Method != "" && !slices.Contains(e.Methods, r.Method) {
		e.Methods = append(e.Methods, r.Method)
	}
}

func (g *guideBuilder) done() Guide {
	for i := range g.Entrypoints {
		e := &g.Entrypoints[i]
		slices.Sort(e.Files)
		slices.Sort(e.Methods)
		slices.SortFunc(e.Via, func(a, b Trail) int { return cmp.Compare(a[0], b[0]) })
	}
	slices.SortStableFunc(g.Entrypoints, func(a, b Entrypoint) int { return cmp.Compare(a.Path, b.Path) })
	return g.Guide
}

// changedLines are the places a file changed, as ranges of its new
// lines. A pure deletion has no new lines, only the place where they
// were: it stands for the lines on either side. A file changed without
// a hunk -- a binary, a new mode -- changed as a whole.
func changedLines(f diff.File) []diff.Range {
	if len(f.Hunks) == 0 {
		return []diff.Range{{}}
	}
	out := make([]diff.Range, 0, len(f.Hunks))
	for _, h := range f.Hunks {
		r := h.New
		if r.Count == 0 {
			r.Count = 2
		}
		out = append(out, r)
	}
	return out
}

// overlaps reports whether two ranges share a line. The zero range is
// the whole file, and shares a line with anything.
func overlaps(a, b diff.Range) bool {
	if a == (diff.Range{}) || b == (diff.Range{}) {
		return true
	}
	return a.Start <= b.End() && b.Start <= a.End()
}
