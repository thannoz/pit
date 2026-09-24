package routes

import (
	"context"
	"go/ast"
	"go/token"
	"io/fs"

	"github.com/thannoz/pit/internal/analysis"
	"github.com/thannoz/pit/internal/diff"
)

// GoLinks reads which Go function uses which: a call, a type, a
// constant, by the line it is used on and the declaration it names.
//
// By declaration and not by import, because a Go package is many files
// and many routes. A changed helper in ollama's openai package reaches
// the handlers that call it, not every route of every package that
// imports openai.
//
// It resolves names the way the Go heuristic resolves handlers: a name
// in the same package, pkg.Name in another package of the tree, and
// x.Method by its name -- in its own package if it has one method of
// that name, else in the tree if the tree has exactly one. A method
// name shared by several types links nowhere rather than everywhere.
type GoLinks struct{}

// Name implements analysis.Linker.
func (GoLinks) Name() string { return "Go" }

// Links implements analysis.Linker.
func (GoLinks) Links(ctx context.Context, fsys fs.FS) ([]analysis.Link, error) {
	p, err := load(ctx, fsys)
	if err != nil || p == nil {
		return nil, err
	}
	idx := p.declarations()

	var links []analysis.Link
	seen := map[analysis.Link]bool{}
	for _, pkg := range p.sortedPackages() {
		for _, f := range pkg.files {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			var visit func(n ast.Node) bool
			visit = func(n ast.Node) bool {
				var targets []span
				switch n := n.(type) {
				case *ast.SelectorExpr:
					targets = idx.selector(p, pkg, f, n)
					// The name on the right is this use and nothing
					// else; what is on the left can be a use of its own.
					defer ast.Inspect(n.X, visit)
				case *ast.Ident:
					targets = idx.local(pkg, n)
				default:
					return true
				}
				line := p.fset.Position(n.Pos()).Line
				for _, t := range targets {
					if t.file == f.path && t.lines.Start <= line && line <= t.lines.End() {
						continue // a declaration naming itself
					}
					l := analysis.Link{
						From: f.path, At: diff.Range{Start: line, Count: 1},
						To: t.file, Target: t.lines,
					}
					if !seen[l] {
						seen[l] = true
						links = append(links, l)
					}
				}
				_, isSelector := n.(*ast.SelectorExpr)
				return !isSelector
			}
			ast.Inspect(f.syntax, visit)
		}
	}
	return links, nil
}

// span is a declaration: where it is.
type span struct {
	file  string
	lines diff.Range
}

// declarations indexes what a name can refer to, package by package.
type declarations struct {
	names   map[*goPackage]map[string][]span // functions, types, constants, variables
	methods map[*goPackage]map[string][]span
	all     map[string][]span // methods of the whole tree, by name
}

func (p *program) declarations() *declarations {
	d := &declarations{
		names:   map[*goPackage]map[string][]span{},
		methods: map[*goPackage]map[string][]span{},
		all:     map[string][]span{},
	}
	lines := func(from, to token.Pos) diff.Range {
		start, end := p.fset.Position(from).Line, p.fset.Position(to).Line
		return diff.Range{Start: start, Count: end - start + 1}
	}
	for _, pkg := range p.packages {
		d.names[pkg] = map[string][]span{}
		d.methods[pkg] = map[string][]span{}
		for _, f := range pkg.files {
			for _, decl := range f.syntax.Decls {
				switch decl := decl.(type) {
				case *ast.FuncDecl:
					from := decl.Pos()
					if decl.Doc != nil {
						from = decl.Doc.Pos()
					}
					s := span{file: f.path, lines: lines(from, decl.End())}
					if decl.Recv == nil {
						d.names[pkg][decl.Name.Name] = append(d.names[pkg][decl.Name.Name], s)
					} else {
						d.methods[pkg][decl.Name.Name] = append(d.methods[pkg][decl.Name.Name], s)
						d.all[decl.Name.Name] = append(d.all[decl.Name.Name], s)
					}
				case *ast.GenDecl:
					for _, spec := range decl.Specs {
						var names []*ast.Ident
						switch spec := spec.(type) {
						case *ast.TypeSpec:
							names = []*ast.Ident{spec.Name}
						case *ast.ValueSpec:
							names = spec.Names
						}
						from := spec.Pos()
						if len(decl.Specs) == 1 {
							from = decl.Pos() // type X struct { ... } and its comment
							if decl.Doc != nil {
								from = decl.Doc.Pos()
							}
						}
						s := span{file: f.path, lines: lines(from, spec.End())}
						for _, n := range names {
							if n.Name != "_" {
								d.names[pkg][n.Name] = append(d.names[pkg][n.Name], s)
							}
						}
					}
				}
			}
		}
	}
	return d
}

// local is what a bare name refers to in its own package.
func (d *declarations) local(pkg *goPackage, id *ast.Ident) []span {
	return d.names[pkg][id.Name]
}

// selector is what x.Name refers to: a declaration of an imported
// package of the tree, or a method.
func (d *declarations) selector(p *program, pkg *goPackage, f *goFile, sel *ast.SelectorExpr) []span {
	if x, ok := sel.X.(*ast.Ident); ok {
		if _, isImport := f.imports[x.Name]; isImport {
			if other := p.imported(f, x.Name); other != nil {
				return d.names[other][sel.Sel.Name]
			}
			return nil
		}
	}
	if ms := d.methods[pkg][sel.Sel.Name]; len(ms) == 1 {
		return ms
	} else if len(ms) > 1 {
		return nil
	}
	if ms := d.all[sel.Sel.Name]; len(ms) == 1 {
		return ms
	}
	return nil
}
