package routes

import (
	"context"
	"encoding/json"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/thannoz/pit/internal/analysis"
	"github.com/thannoz/pit/internal/diff"
)

// TypeScript reads which parts of a JavaScript or TypeScript project use
// which: the names a file imports, the lines it uses them on, and the
// declaration each one leads to. Imports are resolved the way a bundler
// would: relative paths, the aliases in tsconfig.json or jsconfig.json,
// and the packages of the same repository. What comes from node_modules
// is not the project's.
//
// By name and not by file, for the same reason as in Go: a pull request
// that adds a function to a helper module every route imports has not
// changed every route. dub's #4534 added trackActivityLogsTx next to
// trackActivityLog; by file, that reached 369 addresses.
//
// It reads with patterns, not a parser. A declaration is found where
// every formatter puts it, at the start of a line, and it lasts until
// the next thing that starts there. A parser for every dialect --
// TypeScript, JSX, Vue, Svelte, Astro, MDX -- would be a large thing to
// get the same lines from; a hand-written tokenizer would lose its way
// at the first apostrophe in JSX text. A file in which no declaration
// can be found is taken as a whole.
type TypeScript struct{}

// Name implements analysis.Linker.
func (TypeScript) Name() string { return "TypeScript" }

var (
	sourceExtensions = []string{".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs", ".mts", ".cts", ".vue", ".svelte", ".astro", ".mdx"}
	// Only these are read for declarations; the others are components
	// whose one export is the file.
	scriptExtensions = []string{".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs", ".mts", ".cts"}
	skippedDirs      = []string{"node_modules", "dist", "build", "out", "coverage"}
)

// Links implements analysis.Linker.
func (TypeScript) Links(ctx context.Context, fsys fs.FS) ([]analysis.Link, error) {
	p := &project{fsys: fsys, exists: map[string]bool{}, packages: map[string]string{}, configs: map[string]*tsconfig{}}
	var sources []string
	err := fs.WalkDir(fsys, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.IsDir() {
			if name != "." && (strings.HasPrefix(d.Name(), ".") || slices.Contains(skippedDirs, d.Name())) {
				return fs.SkipDir
			}
			return nil
		}
		p.exists[name] = true
		switch {
		case d.Name() == "package.json":
			p.manifest(name)
		case slices.Contains(sourceExtensions, path.Ext(name)) && !strings.HasSuffix(name, ".d.ts"):
			sources = append(sources, name)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	p.modules = map[string]*tsModule{}
	for _, name := range sources {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, err
		}
		p.modules[name] = parseModule(string(data), slices.Contains(scriptExtensions, path.Ext(name)))
	}

	var links []analysis.Link
	for _, from := range sources {
		links = append(links, p.links(from)...)
	}
	return links, nil
}

// links are the uses one file makes of others and of itself.
func (p *project) links(from string) []analysis.Link {
	m := p.modules[from]
	var out []analysis.Link
	add := func(at diff.Range, to string, target diff.Range) {
		if to == from && target != (diff.Range{}) && at.Start >= target.Start && at.Start <= target.End() {
			return // a declaration using itself
		}
		out = append(out, analysis.Link{From: from, At: at, To: to, Target: target})
	}

	// One declaration of this file using another.
	for _, d := range m.decls {
		if d.isType {
			continue
		}
		for _, line := range m.uses[d.name] {
			add(diff.Range{Start: line, Count: 1}, from, d.lines)
		}
	}

	for _, imp := range m.imports {
		to, ok := p.resolve(from, imp.spec)
		if !ok || to == from {
			continue
		}
		if imp.local == "" {
			// import "./styles.css": the whole file depends on it.
			add(diff.Range{}, to, diff.Range{})
			continue
		}
		file, target := p.exported(to, imp.name, 0)
		if target.isType {
			continue
		}
		lines := m.uses[imp.local]
		if len(lines) == 0 {
			continue // imported and never used
		}
		for _, line := range lines {
			add(diff.Range{Start: line, Count: 1}, file, target.lines)
		}
	}

	// A barrel passes on what it re-exports. An import through it
	// usually leads past it, which exported() follows; these are for
	// the imports it cannot follow, which end at the barrel as a whole.
	for _, re := range m.reexports {
		if to, ok := p.resolve(from, re.spec); ok {
			file, target := p.exported(to, re.name, 0)
			if !target.isType {
				add(diff.Range{}, file, target.lines)
			}
		}
	}
	for _, spec := range m.stars {
		if to, ok := p.resolve(from, spec); ok {
			add(diff.Range{}, to, diff.Range{})
		}
	}
	return out
}

// exported finds the declaration a module exports under a name,
// following re-exports through barrel files. Where it cannot tell -- a
// component file, a namespace import, a name it does not find -- the
// answer is the whole file.
func (p *project) exported(file, name string, depth int) (string, declared) {
	m := p.modules[file]
	if m == nil || name == "*" || depth > 8 || !m.readable {
		return file, declared{}
	}
	if d, ok := m.exports[name]; ok {
		return file, d
	}
	if re, ok := m.reexports[name]; ok {
		if to, ok := p.resolve(file, re.spec); ok {
			return p.exported(to, re.name, depth+1)
		}
		return file, declared{}
	}
	for _, spec := range m.stars {
		if to, ok := p.resolve(file, spec); ok {
			if f, d := p.exported(to, name, depth+1); d.lines != (diff.Range{}) {
				return f, d
			}
		}
	}
	return file, declared{}
}

// tsModule is what the patterns read from one source file.
type tsModule struct {
	// readable is false where declarations are not looked for, or
	// none were found: then the file counts as one piece.
	readable  bool
	decls     []declared
	exports   map[string]declared // exported name -> its declaration
	reexports map[string]reexport // export { a as b } from "./x"
	stars     []string            // export * from "./x"
	imports   []binding
	uses      map[string][]int // identifier -> the lines it is used on, outside imports
}

type declared struct {
	name  string
	lines diff.Range
	// types are erased before the code runs, so a change to one
	// changes no page and nothing leads through it.
	isType bool
}

type reexport struct{ spec, name string }

// binding is a name an import brings into a file.
type binding struct {
	local string // empty for import "./x"
	name  string // what the other module exports it as: a name, "default" or "*"
	spec  string
}

var (
	// A declaration at the start of a line.
	declaration   = regexp.MustCompile(`^(export\s+)?(default\s+)?(?:declare\s+)?(?:async\s+)?(function\*?|const|let|var|class|abstract\s+class|interface|type|enum)\s+([A-Za-z_$][\w$]*)`)
	exportDefault = regexp.MustCompile(`^export\s+default\b`)
	// Anything else that starts a statement at the start of a line
	// ends the declaration before it.
	statement = regexp.MustCompile(`^[A-Za-z_$@]`)

	importStatement = regexp.MustCompile(`(?s)\bimport\s+(type\s+)?([^"';()]*?)\s*\bfrom\s*["']([^"'\n]+)["']|\bimport\s*["']([^"'\n]+)["']`)
	exportFrom      = regexp.MustCompile(`(?s)\bexport\s+(type\s+)?(\*(?:\s+as\s+[\w$]+)?|\{[^}]*\})\s*from\s*["']([^"'\n]+)["']`)
	exportList      = regexp.MustCompile(`(?m)^export\s*\{([^}]*)\}\s*;?\s*$`)
	dynamicImport   = regexp.MustCompile(`\b(?:require|import)\s*\(\s*["']([^"'\n]+)["']\s*\)`)
	identifier      = regexp.MustCompile(`[A-Za-z_$][\w$]*`)
)

func parseModule(src string, script bool) *tsModule {
	m := &tsModule{exports: map[string]declared{}, reexports: map[string]reexport{}, uses: map[string][]int{}}
	lines := strings.Split(src, "\n")
	lineAt := lineIndex(src)
	imported := map[int]bool{} // lines that belong to import statements

	for _, match := range importStatement.FindAllStringSubmatchIndex(src, -1) {
		first, last := lineAt(match[0]), lineAt(match[1]-1)
		for l := first; l <= last; l++ {
			imported[l] = true
		}
		if match[8] >= 0 { // import "./x"
			m.imports = append(m.imports, binding{spec: src[match[8]:match[9]]})
			continue
		}
		if match[2] >= 0 { // import type
			continue
		}
		spec := src[match[6]:match[7]]
		m.imports = append(m.imports, bindings(src[match[4]:match[5]], spec)...)
	}
	for _, match := range dynamicImport.FindAllStringSubmatch(src, -1) {
		m.imports = append(m.imports, binding{spec: match[1]})
	}
	for _, match := range exportFrom.FindAllStringSubmatchIndex(src, -1) {
		first, last := lineAt(match[0]), lineAt(match[1]-1)
		for l := first; l <= last; l++ {
			imported[l] = true
		}
		if match[2] >= 0 {
			continue
		}
		clause, spec := src[match[4]:match[5]], src[match[6]:match[7]]
		if clause == "*" {
			m.stars = append(m.stars, spec)
			continue
		}
		if ns, ok := strings.CutPrefix(clause, "*"); ok {
			name := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(ns), "as"))
			m.reexports[name] = reexport{spec: spec, name: "*"}
			continue
		}
		for _, part := range strings.Split(strings.Trim(clause, "{} \n\t"), ",") {
			orig, alias := names(part)
			if orig != "" {
				m.reexports[alias] = reexport{spec: spec, name: orig}
			}
		}
	}

	// Where each identifier is used, outside the import statements and
	// outside comments: "// Same as trackActivityLog" uses nothing.
	for i, line := range lines {
		if imported[i+1] || comment(line) {
			continue
		}
		for _, id := range identifier.FindAllString(line, -1) {
			if u := m.uses[id]; len(u) == 0 || u[len(u)-1] != i+1 {
				m.uses[id] = append(m.uses[id], i+1)
			}
		}
	}

	if !script {
		return m
	}

	// Declarations: from a line that starts one to the line before the
	// next line that starts anything.
	var open *declared
	var exported bool
	var def bool
	closeAt := func(line int) {
		if open == nil {
			return
		}
		open.lines = diff.Range{Start: open.lines.Start, Count: line - open.lines.Start + 1}
		m.decls = append(m.decls, *open)
		if exported {
			m.exports[open.name] = *open
		}
		if def {
			m.exports["default"] = *open
		}
		open = nil
	}
	last := len(lines)
	for last > 0 && strings.TrimSpace(lines[last-1]) == "" {
		last--
	}
	for i := 0; i < last; i++ {
		line := lines[i]
		if !statement.MatchString(line) {
			continue
		}
		// The comment right above a declaration is part of it, the
		// way a doc comment is in Go.
		start := i
		for start > 0 && comment(lines[start-1]) {
			start--
		}
		closeAt(previousContent(lines, start))
		if d := declaration.FindStringSubmatch(line); d != nil {
			open = &declared{name: d[4], lines: diff.Range{Start: start + 1}, isType: d[3] == "type" || d[3] == "interface"}
			exported, def = d[1] != "", d[2] != ""
		} else if exportDefault.MatchString(line) {
			open = &declared{name: "default", lines: diff.Range{Start: start + 1}}
			exported, def = false, true
		}
	}
	closeAt(last)

	// export { a, b as c } names declarations made above.
	for _, match := range exportList.FindAllStringSubmatch(src, -1) {
		for _, part := range strings.Split(match[1], ",") {
			orig, alias := names(part)
			for _, d := range m.decls {
				if d.name == orig {
					m.exports[alias] = d
				}
			}
		}
	}
	m.readable = len(m.decls) > 0 || len(m.reexports) > 0 || len(m.stars) > 0
	return m
}

func comment(line string) bool {
	t := strings.TrimSpace(line)
	return strings.HasPrefix(t, "//") || strings.HasPrefix(t, "/*") || strings.HasPrefix(t, "*")
}

// previousContent is the last line before i that is not blank, so that
// a declaration ends where its text does.
func previousContent(lines []string, i int) int {
	for j := i - 1; j >= 0; j-- {
		if strings.TrimSpace(lines[j]) != "" {
			return j + 1
		}
	}
	return 0
}

// bindings reads an import clause: D, { a, b as c }, * as ns.
func bindings(clause, spec string) []binding {
	var out []binding
	clause = strings.TrimSpace(clause)
	if i := strings.Index(clause, "{"); i >= 0 {
		j := strings.LastIndex(clause, "}")
		if j > i {
			for _, part := range strings.Split(clause[i+1:j], ",") {
				if orig, alias := names(part); orig != "" {
					out = append(out, binding{local: alias, name: orig, spec: spec})
				}
			}
		}
		clause = clause[:i] + clause[j+1:]
	}
	for _, part := range strings.Split(clause, ",") {
		part = strings.TrimSpace(part)
		switch {
		case part == "":
		case strings.HasPrefix(part, "*"):
			ns := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(strings.TrimPrefix(part, "*")), "as"))
			out = append(out, binding{local: ns, name: "*", spec: spec})
		default:
			out = append(out, binding{local: part, name: "default", spec: spec})
		}
	}
	return out
}

// names reads "a", "a as b" and "type a" from a list of imports or
// exports. A type is left out.
func names(part string) (orig, alias string) {
	fields := strings.Fields(part)
	if len(fields) == 0 || fields[0] == "type" {
		return "", ""
	}
	orig, alias = fields[0], fields[0]
	if len(fields) == 3 && fields[1] == "as" {
		alias = fields[2]
	}
	return orig, alias
}

// lineIndex maps a byte offset to its line, starting at 1.
func lineIndex(src string) func(offset int) int {
	var starts []int
	starts = append(starts, 0)
	for i := 0; i < len(src); i++ {
		if src[i] == '\n' {
			starts = append(starts, i+1)
		}
	}
	return func(offset int) int {
		i, _ := slices.BinarySearch(starts, offset+1)
		return i
	}
}

// project is what resolving an import needs to know about the tree.
type project struct {
	fsys     fs.FS
	modules  map[string]*tsModule
	exists   map[string]bool
	packages map[string]string    // package name -> its package.json
	configs  map[string]*tsconfig // directory -> the tsconfig that governs it
}

func (p *project) manifest(name string) {
	data, err := fs.ReadFile(p.fsys, name)
	if err != nil {
		return
	}
	var m struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(data, &m) == nil && m.Name != "" {
		p.packages[m.Name] = name
	}
}

// resolve finds the file an import specifier names, if it is one of the
// project's.
func (p *project) resolve(from, spec string) (string, bool) {
	spec, _, _ = strings.Cut(spec, "?") // "./icon.svg?react"
	if spec == "" || strings.HasPrefix(spec, "/") || strings.Contains(spec, ":") {
		return "", false // absolute, a URL, or "node:fs"
	}
	if spec == "." || spec == ".." || strings.HasPrefix(spec, "./") || strings.HasPrefix(spec, "../") {
		return p.file(path.Join(path.Dir(from), spec))
	}
	cfg := p.config(path.Dir(from))
	if cfg != nil {
		for _, candidate := range cfg.alias(spec) {
			if f, ok := p.file(candidate); ok {
				return f, true
			}
		}
	}
	if f, ok := p.workspace(spec); ok {
		return f, true
	}
	if cfg != nil && cfg.baseURL != "" {
		return p.file(path.Join(cfg.baseURL, spec))
	}
	return "", false
}

// file finds the file a path without its extension means, the way
// bundlers do: the path itself, with an extension, or its index file.
// "./util.js" in TypeScript means util.ts.
func (p *project) file(base string) (string, bool) {
	base = path.Clean(base)
	if base == "." || strings.HasPrefix(base, "../") {
		return "", false
	}
	if p.exists[base] {
		return base, true
	}
	for js, ts := range map[string][]string{".js": {".ts", ".tsx"}, ".jsx": {".tsx"}, ".mjs": {".mts"}, ".cjs": {".cts"}} {
		if stem, ok := strings.CutSuffix(base, js); ok {
			for _, ext := range ts {
				if p.exists[stem+ext] {
					return stem + ext, true
				}
			}
		}
	}
	for _, ext := range sourceExtensions {
		if p.exists[base+ext] {
			return base + ext, true
		}
	}
	for _, ext := range sourceExtensions {
		if f := path.Join(base, "index"+ext); p.exists[f] {
			return f, true
		}
	}
	return "", false
}

// workspace resolves an import of another package of the repository:
// @acme/ui, @acme/ui/button.
func (p *project) workspace(spec string) (string, bool) {
	name := ""
	for n := range p.packages {
		if (spec == n || strings.HasPrefix(spec, n+"/")) && len(n) > len(name) {
			name = n
		}
	}
	sub := strings.TrimPrefix(strings.TrimPrefix(spec, name), "/")
	manifestPath, ok := p.packages[name]
	if !ok {
		return "", false
	}
	dir := path.Dir(manifestPath)
	data, err := fs.ReadFile(p.fsys, manifestPath)
	if err != nil {
		return "", false
	}
	var m struct {
		Main    string          `json:"main"`
		Module  string          `json:"module"`
		Types   string          `json:"types"`
		Exports json.RawMessage `json:"exports"`
	}
	if json.Unmarshal(data, &m) != nil {
		return "", false
	}

	var targets []string
	key := "."
	if sub != "" {
		key = "./" + sub
	}
	targets = append(targets, exported(m.Exports, key)...)
	if sub == "" {
		targets = append(targets, m.Module, m.Main, m.Types, "src/index", "index")
	} else {
		targets = append(targets, sub, "src/"+sub)
	}
	for _, t := range targets {
		if t == "" {
			continue
		}
		if f, ok := p.built(path.Join(dir, t)); ok {
			return f, true
		}
	}
	return "", false
}

// built finds the source of what a package declares as its entry. A
// monorepo's packages often point at dist/, which is not there before a
// build and is not what a pull request changes: src/ is.
func (p *project) built(target string) (string, bool) {
	if f, ok := p.file(strings.TrimSuffix(target, ".d.ts")); ok {
		return f, true
	}
	rooted := "/" + target
	for _, out := range []string{"/dist/", "/build/", "/lib/"} {
		if i := strings.Index(rooted, out); i >= 0 {
			src := rooted[:i] + "/src/" + rooted[i+len(out):]
			src = strings.TrimSuffix(strings.TrimPrefix(src, "/"), ".d.ts")
			if f, ok := p.file(src); ok {
				return f, true
			}
		}
	}
	return "", false
}

// exported reads a package.json "exports" for one subpath: a string, a
// map of subpaths, or a map of conditions, nested as deep as authors
// nest them.
func exported(raw json.RawMessage, key string) []string {
	if len(raw) == 0 {
		return nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if key == "." {
			return []string{s}
		}
		return nil
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	subpaths := false
	for k := range m {
		subpaths = subpaths || strings.HasPrefix(k, ".")
	}
	if !subpaths {
		if key != "." {
			return nil
		}
		return conditions(raw)
	}
	if v, ok := m[key]; ok {
		return conditions(v)
	}
	for k, v := range m {
		if prefix, suffix, ok := strings.Cut(k, "*"); ok && strings.HasPrefix(key, prefix) && strings.HasSuffix(key, suffix) && len(key) >= len(prefix)+len(suffix) {
			star := key[len(prefix) : len(key)-len(suffix)]
			var out []string
			for _, t := range conditions(v) {
				out = append(out, strings.ReplaceAll(t, "*", star))
			}
			return out
		}
	}
	return nil
}

// conditions picks the targets of a conditional export, source-like
// conditions first.
func conditions(raw json.RawMessage) []string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return []string{s}
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	var out []string
	for _, c := range []string{"source", "development", "import", "module", "default", "require", "node", "browser", "types"} {
		if v, ok := m[c]; ok {
			out = append(out, conditions(v)...)
		}
	}
	return out
}

// tsconfig is the part of a tsconfig.json or jsconfig.json that says
// where imports lead.
type tsconfig struct {
	baseURL string              // relative to the tree's root
	paths   map[string][]string // pattern -> substitutions, relative to the tree's root
}

// alias applies the paths whose pattern matches spec, the longest
// prefix first, as TypeScript does.
func (c *tsconfig) alias(spec string) []string {
	best, bestLen := "", -1
	for pattern := range c.paths {
		prefix, suffix, wildcard := strings.Cut(pattern, "*")
		switch {
		case !wildcard && pattern == spec && len(pattern) > bestLen:
			best, bestLen = pattern, len(pattern)
		case wildcard && strings.HasPrefix(spec, prefix) && strings.HasSuffix(spec, suffix) &&
			len(spec) >= len(prefix)+len(suffix) && len(prefix) > bestLen:
			best, bestLen = pattern, len(prefix)
		}
	}
	if bestLen < 0 {
		return nil
	}
	prefix, suffix, _ := strings.Cut(best, "*")
	star := strings.TrimSuffix(strings.TrimPrefix(spec, prefix), suffix)
	var out []string
	for _, sub := range c.paths[best] {
		out = append(out, strings.ReplaceAll(sub, "*", star))
	}
	return out
}

// config is the tsconfig.json or jsconfig.json nearest above dir.
func (p *project) config(dir string) *tsconfig {
	if c, ok := p.configs[dir]; ok {
		return c
	}
	var c *tsconfig
	for _, name := range []string{"tsconfig.json", "jsconfig.json"} {
		if f := path.Join(dir, name); p.exists[f] {
			c = p.readConfig(f, 0)
			break
		}
	}
	if c == nil && dir != "." {
		c = p.config(path.Dir(dir))
	}
	p.configs[dir] = c
	return c
}

// readConfig reads a tsconfig and what it extends. Paths in the file
// are relative to it; they are made relative to the tree's root here,
// so that an extending file can use its base's aliases unchanged.
func (p *project) readConfig(file string, depth int) *tsconfig {
	if depth > 8 {
		return nil
	}
	data, err := fs.ReadFile(p.fsys, file)
	if err != nil {
		return nil
	}
	var raw struct {
		Extends         json.RawMessage `json:"extends"`
		CompilerOptions struct {
			BaseURL *string             `json:"baseUrl"`
			Paths   map[string][]string `json:"paths"`
		} `json:"compilerOptions"`
	}
	if json.Unmarshal(jsonc(data), &raw) != nil {
		return nil
	}

	c := &tsconfig{}
	var bases []string
	var one string
	if json.Unmarshal(raw.Extends, &one) == nil {
		bases = []string{one}
	} else {
		_ = json.Unmarshal(raw.Extends, &bases)
	}
	for _, ext := range bases {
		var baseFile string
		if strings.HasPrefix(ext, ".") {
			baseFile = path.Join(path.Dir(file), ext)
		} else if f, ok := p.workspaceFile(ext); ok {
			baseFile = f
		}
		if baseFile != "" && !strings.HasSuffix(baseFile, ".json") && !p.exists[baseFile] {
			baseFile += ".json"
		}
		if base := p.readConfig(baseFile, depth+1); base != nil {
			if base.baseURL != "" {
				c.baseURL = base.baseURL
			}
			if base.paths != nil {
				c.paths = base.paths
			}
		}
	}

	dir := path.Dir(file)
	if raw.CompilerOptions.BaseURL != nil {
		c.baseURL = path.Join(dir, *raw.CompilerOptions.BaseURL)
	}
	if raw.CompilerOptions.Paths != nil {
		// Relative to baseUrl when there is one, to this file when not.
		from := dir
		if c.baseURL != "" {
			from = c.baseURL
		}
		c.paths = map[string][]string{}
		for pattern, subs := range raw.CompilerOptions.Paths {
			for _, s := range subs {
				c.paths[pattern] = append(c.paths[pattern], path.Join(from, s))
			}
		}
	}
	return c
}

// workspaceFile resolves "@acme/tsconfig/nextjs.json" to a file of the
// repository.
func (p *project) workspaceFile(spec string) (string, bool) {
	for name, manifest := range p.packages {
		if rest, ok := strings.CutPrefix(spec, name+"/"); ok {
			f := path.Join(path.Dir(manifest), rest)
			return f, p.exists[f] || p.exists[f+".json"]
		}
	}
	return "", false
}

// jsonc turns the JSON with comments and trailing commas that
// tsconfig.json is written in into JSON.
func jsonc(data []byte) []byte {
	var out []byte
	inString, escaped := false, false
	for i := 0; i < len(data); i++ {
		c := data[i]
		switch {
		case inString:
			out = append(out, c)
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
		case c == '"':
			inString = true
			out = append(out, c)
		case c == '/' && i+1 < len(data) && data[i+1] == '/':
			for i < len(data) && data[i] != '\n' {
				i++
			}
			out = append(out, '\n')
		case c == '/' && i+1 < len(data) && data[i+1] == '*':
			i += 2
			for i+1 < len(data) && (data[i] != '*' || data[i+1] != '/') {
				i++
			}
			i++
		default:
			out = append(out, c)
		}
	}
	return trailingCommas.ReplaceAll(out, []byte("$1"))
}

var trailingCommas = regexp.MustCompile(`,(\s*[}\]])`)
