package routes

import (
	"context"
	"go/ast"
	"go/token"
	"io/fs"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/thannoz/pit/internal/analysis"
	"github.com/thannoz/pit/internal/diff"
)

// Go reads the routes a Go program registers with net/http, chi or gin,
// from the source: the call that registers each route, and the
// functions that handle it.
//
// It parses and does not type-check. Type-checking would need every
// dependency downloaded, and a heuristic that fetches modules to
// answer "which pages does this change touch" is a heuristic nobody
// waits for. What it gives up is certainty about what a variable is;
// it makes up for it by only reading packages that import a router,
// and by only believing a path it can read as a string.
//
// It follows a router through the program as far as the source says:
// into groups and sub-routers, into chi's Route and Mount, and into the
// functions a router is handed to. A prefix it cannot read -- built at
// run time, taken from configuration -- makes every route below it
// unknown, and an unknown route is left out rather than guessed.
type Go struct{}

// Name implements analysis.Analyzer.
func (Go) Name() string { return "Go" }

// Routes implements analysis.Analyzer.
func (Go) Routes(ctx context.Context, fsys fs.FS) ([]analysis.Route, error) {
	p, err := load(ctx, fsys)
	if err != nil || p == nil {
		return nil, err
	}

	a := &goAnalysis{program: p, slots: map[*decl]map[int]*prefixes{}}
	for _, pkg := range p.sortedPackages() {
		if len(pkg.routers) == 0 {
			continue
		}
		for _, f := range pkg.files {
			for _, d := range f.syntax.Decls {
				fn, ok := d.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				a.function(p.declOf(fn))
			}
		}
	}
	a.propagate()
	return a.routes(), nil
}

// A prefix is where a router sits, as far as one function knows: an
// offset from something only the callers know, or unknown.
type prefix struct {
	// slot is what the offset is from: a parameter of the function
	// (0 and up), the prefix the function itself is reached at
	// (ambient), or the root of the program (absolute).
	slot    int
	rest    string
	unknown bool
}

const (
	ambient  = -1
	absolute = -2
)

func (p prefix) plus(s string, ok bool) prefix {
	if !ok {
		p.unknown = true
		return p
	}
	p.rest = join(p.rest, s)
	return p
}

// A registration is one call that adds a route.
type registration struct {
	fn       *decl
	file     *goFile
	at       prefix
	method   string
	path     string
	call     *ast.CallExpr
	handlers []ast.Expr
}

// An edge is a router handed to another function: as an argument, as
// the function chi.Route calls, or as what a function returns to Mount.
type edge struct {
	from, to *decl
	slot     int // the parameter it arrives as, or ambient
	at       prefix
}

// prefixes are the values a slot can take across all callers.
type prefixes struct {
	values  map[string]bool
	unknown bool
}

type goAnalysis struct {
	*program
	registrations []registration
	edges         []edge
	slots         map[*decl]map[int]*prefixes
}

// scope is what one function body knows about its routers by name.
type scope struct {
	vars    map[string]prefix
	mounted map[string]prefix   // local routers mounted somewhere, which wins
	fresh   map[string]bool     // routers made here: chi.NewRouter() and the like
	results map[string]ast.Expr // variables holding what a call returned: h, err := s.Routes()
}

func (s scope) child() scope {
	return scope{vars: maps(s.vars), mounted: s.mounted, fresh: s.fresh, results: s.results}
}

func maps(m map[string]prefix) map[string]prefix {
	out := make(map[string]prefix, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func (a *goAnalysis) function(d *decl) {
	sc := scope{vars: map[string]prefix{}, mounted: map[string]prefix{}, fresh: map[string]bool{}, results: map[string]ast.Expr{}}
	for i, name := range params(d.node.Type) {
		sc.vars[name] = prefix{slot: i}
	}
	// Twice: a sub-router is usually filled before it is mounted, so
	// the first walk learns where the mounted ones are and the second
	// records their routes there.
	a.walk(d, d.node.Body, sc.child(), false)
	a.walk(d, d.node.Body, sc.child(), true)
}

func params(t *ast.FuncType) []string {
	var out []string
	if t.Params == nil {
		return out
	}
	for _, field := range t.Params.List {
		if len(field.Names) == 0 {
			out = append(out, "_")
		}
		for _, n := range field.Names {
			out = append(out, n.Name)
		}
	}
	return out
}

// walk reads a function body. Only the second walk records anything.
func (a *goAnalysis) walk(d *decl, body ast.Node, sc scope, record bool) {
	pending := map[*ast.CallExpr]string{} // gorilla's .Methods("GET") on a registration
	ast.Inspect(body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.AssignStmt:
			if len(n.Lhs) == len(n.Rhs) {
				for i, rhs := range n.Rhs {
					if at, ok := a.router(d, rhs, sc); ok {
						sc.vars[key(n.Lhs[i])] = at
						sc.fresh[key(n.Lhs[i])] = a.fresh(d, rhs)
					}
				}
			}
			if len(n.Rhs) == 1 {
				if call, ok := n.Rhs[0].(*ast.CallExpr); ok {
					sc.results[key(n.Lhs[0])] = call.Fun
				}
			}
			// s.handler = r hands the router to whoever reads the
			// field, which is as far as the source goes.
			for i, lhs := range n.Lhs {
				if _, field := lhs.(*ast.SelectorExpr); field && i < len(n.Rhs) && a.escapes(d, n.Rhs[i], sc) {
					d.escapes = true
				}
			}
		case *ast.ReturnStmt:
			for _, r := range n.Results {
				if a.escapes(d, r, sc) {
					d.escapes = true
				}
			}
		case *ast.ValueSpec:
			if len(n.Names) == len(n.Values) {
				for i, v := range n.Values {
					if at, ok := a.router(d, v, sc); ok {
						sc.vars[n.Names[i].Name] = at
						sc.fresh[n.Names[i].Name] = a.fresh(d, v)
					}
				}
			}
		case *ast.CompositeLit:
			// http.Server{Handler: s.Routes()} serves a router at the
			// root of a server.
			if record && a.isServer(d, n.Type) {
				for _, elt := range n.Elts {
					if kv, ok := elt.(*ast.KeyValueExpr); ok && key(kv.Key) == "Handler" {
						a.mount(d, kv.Value, prefix{slot: absolute}, sc)
					}
				}
			}
		case *ast.CallExpr:
			return a.call(d, n, sc, record, pending)
		}
		return true
	})
}

func (a *goAnalysis) isServer(d *decl, t ast.Expr) bool {
	sel, ok := t.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "Server" && a.isImport(d, sel.X, "nethttp")
}

// served is where net/http's functions take the handler they serve.
var served = map[string]int{"ListenAndServe": 1, "ListenAndServeTLS": 3, "Serve": 1, "ServeTLS": 1}

// router reports whether e makes or derives a router, and where it
// sits: gin's Group, chi's With, gorilla's PathPrefix().Subrouter(),
// or a fresh router, which sits wherever the function is reached.
func (a *goAnalysis) router(d *decl, e ast.Expr, sc scope) (prefix, bool) {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return prefix{}, false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return prefix{}, false
	}
	routers := d.pkg.routers
	switch name := sel.Sel.Name; {
	case a.isImport(d, sel.X, "gin") && (name == "New" || name == "Default"),
		a.isImport(d, sel.X, "chi") && name == "NewRouter",
		a.isImport(d, sel.X, "gorilla") && name == "NewRouter",
		a.isImport(d, sel.X, "nethttp") && name == "NewServeMux":
		return prefix{slot: ambient}, true
	case routers["gin"] && name == "Group" && len(call.Args) > 0:
		s, ok := a.str(d, call.Args[0])
		return a.at(d, sel.X, sc).plus(s, ok), true
	case routers["chi"] && name == "With":
		return a.at(d, sel.X, sc), true
	case routers["gorilla"] && name == "Subrouter":
		inner, ok := sel.X.(*ast.CallExpr)
		if !ok {
			return prefix{}, false
		}
		if s, ok := inner.Fun.(*ast.SelectorExpr); ok && s.Sel.Name == "PathPrefix" && len(inner.Args) == 1 {
			str, ok := a.str(d, inner.Args[0])
			return a.at(d, s.X, sc).plus(str, ok), true
		}
	}
	return prefix{}, false
}

// escapes reports whether e carries a router made in this function,
// as it is or wrapped: return caseInsensitivePaths(r) returns r.
func (a *goAnalysis) escapes(d *decl, e ast.Expr, sc scope) bool {
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.Ident:
			found = found || sc.fresh[n.Name]
		case ast.Expr:
			found = found || a.fresh(d, n)
		}
		return !found
	})
	return found
}

// fresh reports whether e makes a new router rather than deriving one.
func (a *goAnalysis) fresh(d *decl, e ast.Expr) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	switch name := sel.Sel.Name; {
	case a.isImport(d, sel.X, "gin"):
		return name == "New" || name == "Default"
	case a.isImport(d, sel.X, "chi"), a.isImport(d, sel.X, "gorilla"):
		return name == "NewRouter"
	case a.isImport(d, sel.X, "nethttp"):
		return name == "NewServeMux"
	}
	return false
}

// at is where the router e sits.
func (a *goAnalysis) at(d *decl, e ast.Expr, sc scope) prefix {
	if at, ok := a.router(d, e, sc); ok {
		return at
	}
	k := key(e)
	if at, ok := sc.mounted[k]; ok {
		return at
	}
	if at, ok := sc.vars[k]; ok {
		return at
	}
	if a.isImport(d, e, "nethttp") {
		return prefix{slot: absolute} // http.Handle: the default mux
	}
	return prefix{slot: ambient}
}

var ginMethods = []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"}
var chiMethods = []string{"Get", "Post", "Put", "Patch", "Delete", "Head", "Options", "Connect", "Trace"}

// call handles one call. It returns whether to look inside it.
func (a *goAnalysis) call(d *decl, call *ast.CallExpr, sc scope, record bool, pending map[*ast.CallExpr]string) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		if record {
			a.handOver(d, call, sc)
		}
		return true
	}
	routers, name, args := d.pkg.routers, sel.Sel.Name, call.Args
	reg := func(method string, pathArg ast.Expr, handlers []ast.Expr) bool {
		if !record {
			return true
		}
		p, ok := a.str(d, pathArg)
		if !ok {
			return true // a path we cannot read is a route we do not know
		}
		if m, rest, ok := patternMethod(p); ok {
			method, p = m, rest
		}
		if method == "" {
			method = pending[call]
		}
		if !strings.HasPrefix(p, "/") {
			// net/http allows a host before the path; the address
			// is what comes after it.
			i := strings.Index(p, "/")
			if i < 0 {
				return true
			}
			p = p[i:]
		}
		// chi's r.With(auth).Delete(...) wraps this one route in auth.
		for x := sel.X; ; {
			inner, ok := x.(*ast.CallExpr)
			if !ok {
				break
			}
			s, ok := inner.Fun.(*ast.SelectorExpr)
			if !ok {
				break
			}
			if s.Sel.Name == "With" {
				handlers = append(slices.Clip(handlers), inner.Args...)
			}
			x = s.X
		}
		at := a.at(d, sel.X, sc)
		a.registrations = append(a.registrations, registration{
			fn: d, file: d.file, at: at, method: method, path: p, call: call, handlers: handlers,
		})
		if len(handlers) == 1 {
			a.mount(d, handlers[0], at.plus(p, true), sc)
		}
		return true
	}

	if i, ok := served[name]; ok && record && a.isImport(d, sel.X, "nethttp") && len(args) > i {
		a.mount(d, args[i], prefix{slot: absolute}, sc)
	}

	switch {
	case routers["gorilla"] && name == "Methods" && len(args) > 0:
		if inner, ok := sel.X.(*ast.CallExpr); ok {
			if m, ok := a.str(d, args[0]); ok {
				pending[inner] = strings.ToUpper(m)
			}
		}
	case routers["gin"] && slices.Contains(ginMethods, name) && len(args) >= 1:
		return reg(name, args[0], args[1:])
	case routers["gin"] && name == "Any" && len(args) >= 1:
		return reg("", args[0], args[1:])
	case routers["chi"] && slices.Contains(chiMethods, name) && len(args) >= 1:
		return reg(strings.ToUpper(name), args[0], args[1:])
	case (routers["chi"] || routers["gin"]) && (name == "Method" || name == "MethodFunc" || name == "Handle") && len(args) >= 3:
		m, _ := a.str(d, args[0])
		return reg(strings.ToUpper(m), args[1], args[2:])
	case (routers["nethttp"] || routers["chi"] || routers["gorilla"]) && (name == "Handle" || name == "HandleFunc") && len(args) == 2:
		return reg("", args[0], args[1:])
	case routers["chi"] && name == "Route" && len(args) == 2:
		p, ok := a.str(d, args[0])
		at := a.at(d, sel.X, sc).plus(p, ok)
		return a.nested(d, args[1], at, sc, record)
	case routers["chi"] && name == "Group" && len(args) == 1:
		return a.nested(d, args[0], a.at(d, sel.X, sc), sc, record)
	case routers["chi"] && name == "Mount" && len(args) == 2:
		p, ok := a.str(d, args[0])
		at := a.at(d, sel.X, sc).plus(p, ok)
		if _, local := sc.vars[key(args[1])]; local {
			sc.mounted[key(args[1])] = at
		} else if record {
			a.mount(d, args[1], at, sc)
		}
		return true
	}
	if record {
		a.handOver(d, call, sc)
	}
	return true
}

// mount records a handler that is a router some function returned --
// admin.Routes() in place, or h after h, err := s.Routes() -- as sitting
// at the prefix it is mounted at.
func (a *goAnalysis) mount(d *decl, h ast.Expr, at prefix, sc scope) {
	var fn ast.Expr
	switch h := h.(type) {
	case *ast.CallExpr:
		fn = h.Fun
	case *ast.Ident, *ast.SelectorExpr:
		fn = sc.results[key(h)]
	}
	if fn == nil {
		return
	}
	for _, to := range a.resolve(d, fn) {
		a.edges = append(a.edges, edge{from: d, to: to, slot: ambient, at: at})
	}
}

// nested walks the function chi's Route or Group call with the router:
// a literal in place, a named function through an edge.
func (a *goAnalysis) nested(d *decl, fn ast.Expr, at prefix, sc scope, record bool) bool {
	if lit, ok := fn.(*ast.FuncLit); ok {
		inner := sc.child()
		if names := params(lit.Type); len(names) > 0 {
			inner.vars[names[0]] = at
		}
		a.walk(d, lit.Body, inner, record)
		return false
	}
	if record {
		for _, to := range a.resolve(d, fn) {
			a.edges = append(a.edges, edge{from: d, to: to, slot: 0, at: at})
		}
	}
	return true
}

// handOver records a router passed to another function as an argument.
func (a *goAnalysis) handOver(d *decl, call *ast.CallExpr, sc scope) {
	var targets []*decl
	for i, arg := range call.Args {
		at, ok := a.router(d, arg, sc)
		if !ok {
			k := key(arg)
			if k == "" {
				continue
			}
			if at, ok = sc.mounted[k]; !ok {
				if at, ok = sc.vars[k]; !ok {
					continue
				}
			}
		}
		if targets == nil {
			targets = a.resolve(d, call.Fun)
		}
		for _, to := range targets {
			a.edges = append(a.edges, edge{from: d, to: to, slot: i, at: at})
		}
	}
}

// propagate works out which prefixes each function's routers can have.
// A function that is handed a router sits wherever the callers' routers
// do. One that is handed none sits at the root -- unless the router it
// makes leaves it, returned or stored: then it sits wherever that
// router is mounted, and where nothing in the source mounts it, nobody
// knows where it sits.
func (a *goAnalysis) propagate() {
	reached := map[*decl]bool{}
	for _, e := range a.edges {
		if e.slot == ambient {
			reached[e.to] = true
		}
	}
	slot := func(d *decl, i int) *prefixes {
		if a.slots[d] == nil {
			a.slots[d] = map[int]*prefixes{}
		}
		if a.slots[d][i] == nil {
			a.slots[d][i] = &prefixes{values: map[string]bool{}}
			switch {
			case i == absolute || (i == ambient && !reached[d] && !d.escapes):
				a.slots[d][i].values[""] = true
			case i == ambient && !reached[d]:
				a.slots[d][i].unknown = true
			}
		}
		return a.slots[d][i]
	}

	// Bounded: a recursion through routers would otherwise grow a
	// prefix forever, and a dozen levels is more than any program has.
	for round := 0; round < 12; round++ {
		changed := false
		for _, e := range a.edges {
			from, to := slot(e.from, e.at.slot), slot(e.to, e.slot)
			if (from.unknown || e.at.unknown) && !to.unknown {
				to.unknown, changed = true, true
			}
			if e.at.unknown {
				continue
			}
			for v := range from.values {
				full := join(v, e.at.rest)
				if !to.values[full] && len(to.values) < 32 {
					to.values[full], changed = true, true
				}
			}
		}
		if !changed {
			break
		}
	}
	for _, r := range a.registrations {
		slot(r.fn, r.at.slot)
	}
}

func (a *goAnalysis) routes() []analysis.Route {
	var out []analysis.Route
	for _, r := range a.registrations {
		if r.at.unknown || a.slots[r.fn][r.at.slot].unknown {
			continue
		}
		s := a.slots[r.fn][r.at.slot]
		values := make([]string, 0, len(s.values))
		for v := range s.values {
			values = append(values, v)
		}
		slices.Sort(values)

		files := a.handlerFiles(r)
		for _, v := range values {
			address := placeholders(join(join(v, r.at.rest), r.path))
			kind := kindOf(r.method, address)
			out = append(out, analysis.Route{
				Path: address, File: r.file.path, Kind: kind, Method: r.method, Lines: a.lines(r.call),
			})
			for _, h := range files {
				out = append(out, analysis.Route{
					Path: address, File: h.file.path, Kind: kind, Method: r.method, Lines: a.lines(h.node),
				})
			}
		}
	}
	return out
}

// handlerFiles resolves every function a registration names after the
// path: the handler, and the middleware around it. A changed middleware
// changes every route it wraps.
func (a *goAnalysis) handlerFiles(r registration) []*decl {
	var out []*decl
	var visit func(e ast.Expr)
	visit = func(e ast.Expr) {
		switch e := e.(type) {
		case *ast.Ident, *ast.SelectorExpr:
			for _, d := range a.resolve(r.fn, e) {
				if !slices.Contains(out, d) {
					out = append(out, d)
				}
			}
		case *ast.CallExpr:
			visit(e.Fun)
			for _, arg := range e.Args {
				visit(arg)
			}
		case *ast.ParenExpr:
			visit(e.X)
		case *ast.UnaryExpr:
			visit(e.X)
		}
	}
	for _, h := range r.handlers {
		visit(h)
	}
	return out
}

func (a *goAnalysis) lines(n ast.Node) diff.Range {
	start, end := a.fset.Position(n.Pos()).Line, a.fset.Position(n.End()).Line
	if fn, ok := n.(*ast.FuncDecl); ok && fn.Doc != nil {
		start = a.fset.Position(fn.Doc.Pos()).Line
	}
	return diff.Range{Start: start, Count: end - start + 1}
}

// kindOf guesses. Whether a handler writes a page or JSON is in its
// body, not in its registration; an address under /api or /v1 is an
// endpoint, a GET elsewhere is most likely something to look at.
func kindOf(method, address string) analysis.Kind {
	if method != "" && method != "GET" && method != "HEAD" {
		return analysis.Endpoint
	}
	for _, seg := range strings.Split(address, "/") {
		if seg == "api" || version.MatchString(seg) {
			return analysis.Endpoint
		}
	}
	return analysis.Page
}

var version = regexp.MustCompile(`^v[0-9]+$`)

// patternMethod splits the method off a net/http pattern: "GET /x".
func patternMethod(p string) (method, rest string, ok bool) {
	m, rest, ok := strings.Cut(p, " ")
	if !ok || m == "" || strings.ToUpper(m) != m || strings.Contains(m, "/") {
		return "", p, false
	}
	return m, strings.TrimLeft(rest, " "), true
}

// join puts a path below a prefix the way the routers do: "/api" and
// "/users" make "/api/users", and "/" below "/api" is "/api" itself.
func join(prefix, p string) string {
	switch {
	case prefix == "":
		return p
	case p == "" || p == "/":
		return prefix
	default:
		return strings.TrimSuffix(prefix, "/") + "/" + strings.TrimPrefix(p, "/")
	}
}

// placeholders writes every router's parameters the way analysis.Route
// wants them: gin's :id and *path, chi's {id:[0-9]+} and *, and Go's
// own {id}, {path...} and {$}.
func placeholders(p string) string {
	if p == "" {
		return "/"
	}
	segs := strings.Split(p, "/")
	for i, s := range segs {
		switch {
		case strings.HasPrefix(s, ":") && len(s) > 1:
			segs[i] = "{" + s[1:] + "}"
		case s == "*":
			segs[i] = "{rest...}"
		case strings.HasPrefix(s, "*"):
			segs[i] = "{" + s[1:] + "...}"
		case s == "{$}":
			segs[i] = ""
		case strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}"):
			name := s[1 : len(s)-1]
			if n, _, ok := strings.Cut(name, ":"); ok {
				name = n
			}
			segs[i] = "{" + name + "}"
		}
	}
	return strings.Join(segs, "/")
}

// str reads e as a string, if the source says what it is: a literal,
// a constant, or a sum of them.
func (a *goAnalysis) str(d *decl, e ast.Expr) (string, bool) {
	return a.program.str(d.file, d.pkg, e, 0)
}

func (p *program) str(f *goFile, pkg *goPackage, e ast.Expr, depth int) (string, bool) {
	if depth > 8 {
		return "", false
	}
	switch e := e.(type) {
	case *ast.BasicLit:
		if e.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(e.Value)
		return s, err == nil
	case *ast.ParenExpr:
		return p.str(f, pkg, e.X, depth+1)
	case *ast.BinaryExpr:
		if e.Op != token.ADD {
			return "", false
		}
		l, ok := p.str(f, pkg, e.X, depth+1)
		if !ok {
			return "", false
		}
		r, ok := p.str(f, pkg, e.Y, depth+1)
		return l + r, ok
	case *ast.Ident:
		if c, ok := pkg.consts[e.Name]; ok {
			return p.str(c.file, pkg, c.value, depth+1)
		}
	case *ast.SelectorExpr:
		x, ok := e.X.(*ast.Ident)
		if !ok {
			return "", false
		}
		if other := p.imported(f, x.Name); other != nil {
			if c, ok := other.consts[e.Sel.Name]; ok {
				return p.str(c.file, other, c.value, depth+1)
			}
		}
	}
	return "", false
}

// key names a router variable: r, api, s.router.
func key(e ast.Expr) string {
	switch e := e.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		if x := key(e.X); x != "" {
			return x + "." + e.Sel.Name
		}
	}
	return ""
}
