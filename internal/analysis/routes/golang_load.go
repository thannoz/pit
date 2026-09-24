package routes

import (
	"bufio"
	"bytes"
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path"
	"slices"
	"strconv"
	"strings"
)

// routerImports are the packages whose calls register routes, by the
// name the heuristic uses for them.
var routerImports = map[string]string{
	"net/http":                 "nethttp",
	"github.com/go-chi/chi":    "chi",
	"github.com/gin-gonic/gin": "gin",
	"github.com/gorilla/mux":   "gorilla",
}

func routerOf(importPath string) string {
	if r, ok := routerImports[importPath]; ok {
		return r
	}
	if strings.HasPrefix(importPath, "github.com/go-chi/chi/v") {
		return "chi"
	}
	return ""
}

// program is every Go package in the tree, parsed.
type program struct {
	fset     *token.FileSet
	packages map[string]*goPackage // by directory
	modules  []module
}

type module struct{ root, path string }

type goPackage struct {
	dir   string
	name  string
	files []*goFile
	// routers are the router packages any file of the package
	// imports. A package that sets up its routes in one file and
	// holds its router in a struct field declared in another still
	// registers routes in the first.
	routers map[string]bool
	funcs   map[string][]*decl // several: build tags, init
	methods map[string][]*decl
	consts  map[string]constant
}

type goFile struct {
	path    string
	syntax  *ast.File
	imports map[string]string // local name -> import path
}

type decl struct {
	pkg  *goPackage
	file *goFile
	node *ast.FuncDecl
	// escapes is set for a function whose router leaves it, returned
	// or stored, which puts its routes wherever the caller mounts it.
	escapes bool
}

type constant struct {
	file  *goFile
	value ast.Expr
}

// load parses the tree, or returns nil when no package imports a
// router: then there is nothing to find, and nothing to parse for it.
func load(ctx context.Context, fsys fs.FS) (*program, error) {
	p := &program{fset: token.NewFileSet(), packages: map[string]*goPackage{}}
	var sources []string
	err := fs.WalkDir(fsys, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.IsDir() {
			// The directories the go command itself ignores, and
			// vendored code, which is someone else's routes.
			base := d.Name()
			if name != "." && (strings.HasPrefix(base, ".") || strings.HasPrefix(base, "_") ||
				base == "testdata" || base == "vendor" || base == "node_modules") {
				return fs.SkipDir
			}
			return nil
		}
		switch {
		case d.Name() == "go.mod":
			data, err := fs.ReadFile(fsys, name)
			if err != nil {
				return err
			}
			if mod := modulePath(data); mod != "" {
				p.modules = append(p.modules, module{root: path.Dir(name), path: mod})
			}
		case strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go"):
			sources = append(sources, name)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	// The imports first: they are enough to tell whether there is
	// anything to find, and cheap.
	any := false
	headers := map[string]*ast.File{}
	for _, name := range sources {
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, err
		}
		f, err := parser.ParseFile(token.NewFileSet(), name, data, parser.ImportsOnly|parser.SkipObjectResolution)
		if err != nil {
			continue // a file that does not parse registers nothing we could read
		}
		headers[name] = f
		for _, imp := range f.Imports {
			if ip, err := strconv.Unquote(imp.Path.Value); err == nil && routerOf(ip) != "" {
				any = true
			}
		}
	}
	if !any {
		return nil, nil
	}

	for _, name := range sources {
		if headers[name] == nil {
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, err
		}
		syntax, err := parser.ParseFile(p.fset, name, data, parser.ParseComments|parser.SkipObjectResolution)
		if err != nil {
			continue
		}
		p.add(name, syntax)
	}
	return p, nil
}

func modulePath(gomod []byte) string {
	scanner := bufio.NewScanner(bytes.NewReader(gomod))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if rest, ok := strings.CutPrefix(line, "module"); ok && (rest == "" || rest[0] == ' ' || rest[0] == '\t') {
			rest, _, _ = strings.Cut(rest, "//")
			return strings.Trim(strings.TrimSpace(rest), `"`)
		}
	}
	return ""
}

func (p *program) add(name string, syntax *ast.File) {
	dir := path.Dir(name)
	pkg := p.packages[dir]
	if pkg == nil {
		pkg = &goPackage{
			dir: dir, name: syntax.Name.Name, routers: map[string]bool{},
			funcs: map[string][]*decl{}, methods: map[string][]*decl{}, consts: map[string]constant{},
		}
		p.packages[dir] = pkg
	}
	f := &goFile{path: name, syntax: syntax, imports: map[string]string{}}
	pkg.files = append(pkg.files, f)

	for _, imp := range syntax.Imports {
		ip, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}
		local := defaultName(ip)
		if imp.Name != nil {
			local = imp.Name.Name
		}
		f.imports[local] = ip
		if r := routerOf(ip); r != "" {
			pkg.routers[r] = true
		}
	}

	for _, d := range syntax.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			fd := &decl{pkg: pkg, file: f, node: d}
			if d.Recv == nil {
				pkg.funcs[d.Name.Name] = append(pkg.funcs[d.Name.Name], fd)
			} else {
				pkg.methods[d.Name.Name] = append(pkg.methods[d.Name.Name], fd)
			}
		case *ast.GenDecl:
			if d.Tok != token.CONST && d.Tok != token.VAR {
				continue
			}
			for _, spec := range d.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok || len(vs.Names) != len(vs.Values) {
					continue
				}
				for i, n := range vs.Names {
					pkg.consts[n.Name] = constant{file: f, value: vs.Values[i]}
				}
			}
		}
	}
}

// defaultName is the name an import is used by when it is not renamed:
// the last element of its path, without a major version.
func defaultName(importPath string) string {
	parts := strings.Split(importPath, "/")
	last := parts[len(parts)-1]
	if version.MatchString(last) && len(parts) > 1 {
		last = parts[len(parts)-2]
	}
	if i := strings.Index(last, ".v"); i > 0 {
		last = last[:i] // gopkg.in/yaml.v3
	}
	return strings.ReplaceAll(last, "-", "_")
}

func (p *program) sortedPackages() []*goPackage {
	dirs := make([]string, 0, len(p.packages))
	for d := range p.packages {
		dirs = append(dirs, d)
	}
	slices.Sort(dirs)
	out := make([]*goPackage, 0, len(dirs))
	for _, d := range dirs {
		out = append(out, p.packages[d])
	}
	return out
}

func (p *program) declOf(fn *ast.FuncDecl) *decl {
	pos := p.fset.Position(fn.Pos()).Filename
	pkg := p.packages[path.Dir(pos)]
	all := pkg.methods[fn.Name.Name]
	if fn.Recv == nil {
		all = pkg.funcs[fn.Name.Name]
	}
	for _, d := range all {
		if d.node == fn {
			return d
		}
	}
	return nil
}

// imported is the package a file refers to by name, when it is part of
// the tree.
func (p *program) imported(f *goFile, name string) *goPackage {
	ip, ok := f.imports[name]
	if !ok {
		return nil
	}
	best := module{}
	for _, m := range p.modules {
		if (ip == m.path || strings.HasPrefix(ip, m.path+"/")) && len(m.path) > len(best.path) {
			best = m
		}
	}
	if best.path == "" {
		return nil
	}
	return p.packages[path.Join(best.root, strings.TrimPrefix(ip, best.path))]
}

// isImport reports whether e is the name of an imported router package.
func (a *goAnalysis) isImport(d *decl, e ast.Expr, router string) bool {
	id, ok := e.(*ast.Ident)
	if !ok {
		return false
	}
	ip, ok := d.file.imports[id.Name]
	return ok && routerOf(ip) == router
}

// resolve finds the functions an expression names: f in this package,
// pkg.F in another package of the tree, x.M a method called M. Without
// types, x.M is every method of that name in the package -- or, when
// the package has none, the one method of that name in the tree, if
// there is exactly one.
func (p *program) resolve(d *decl, e ast.Expr) []*decl {
	switch e := e.(type) {
	case *ast.Ident:
		return d.pkg.funcs[e.Name]
	case *ast.SelectorExpr:
		if x, ok := e.X.(*ast.Ident); ok {
			if _, isImport := d.file.imports[x.Name]; isImport {
				if other := p.imported(d.file, x.Name); other != nil {
					return other.funcs[e.Sel.Name]
				}
				return nil
			}
		}
		if ms := d.pkg.methods[e.Sel.Name]; len(ms) > 0 {
			return ms
		}
		var found []*decl
		for _, pkg := range p.packages {
			found = append(found, pkg.methods[e.Sel.Name]...)
		}
		if len(found) == 1 {
			return found
		}
	}
	return nil
}
