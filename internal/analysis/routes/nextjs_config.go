package routes

import (
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"strings"

	"github.com/thannoz/pit/internal/analysis"
)

// nextConfig is what next.config and the middleware say about where
// pages answer, as far as their text can be read without running them.
type nextConfig struct {
	// basePath comes before every address, when it is a literal.
	basePath string
	// unread are settings present in a form pit cannot read, each a
	// doubt on every address.
	unread []analysis.Doubt
	// i18n puts a locale before the Pages Router's addresses.
	i18n bool
	// moves are redirects and rewrites, by the address they take.
	moves []move
	// middleware is the file that can rewrite requests, if any, and
	// the addresses it applies to.
	middleware string
	matchers   []matcher
}

type move struct {
	kind     string // "redirects" or "rewrites"
	source   *regexp.Regexp
	from, to string
	// sometimes is set when the move depends on the request -- a
	// cookie, a header -- rather than only on the address.
	sometimes bool
}

// matcher decides whether the middleware runs for an address.
type matcher func(address string) bool

var (
	basePathLiteral = regexp.MustCompile(`\bbasePath\s*:\s*["'` + "`" + `]([^"'` + "`" + `]*)["'` + "`" + `]`)
	basePathAny     = regexp.MustCompile(`\bbasePath\s*:`)
	i18nKey         = regexp.MustCompile(`\bi18n\s*:`)
	pageExtensions  = regexp.MustCompile(`\bpageExtensions\s*:\s*\[([^\]]*)\]`)
	quoted          = regexp.MustCompile(`["'` + "`" + `]([^"'` + "`" + `]*)["'` + "`" + `]`)
	sourceKey       = regexp.MustCompile(`\bsource\s*:\s*["'` + "`" + `]([^"'` + "`" + `]+)["'` + "`" + `]`)
	destinationKey  = regexp.MustCompile(`\bdestination\s*:\s*["'` + "`" + `]([^"'` + "`" + `]+)["'` + "`" + `]`)
	movesKey        = regexp.MustCompile(`\b(redirects|rewrites|headers)\b`)
	conditionKey    = regexp.MustCompile(`\b(has|missing)\s*:\s*\[`)
	typeKey         = regexp.MustCompile(`\btype\s*:\s*["'` + "`" + `]([^"'` + "`" + `]+)["'` + "`" + `]`)
	valueKey        = regexp.MustCompile(`\bvalue\s*:\s*["'` + "`" + `]([^"'` + "`" + `]*)["'` + "`" + `]`)
	matcherKey      = regexp.MustCompile(`\bmatcher\s*:`)
)

func readNextConfig(fsys fs.FS, root, appDir string) nextConfig {
	var c nextConfig
	for _, name := range []string{"next.config.js", "next.config.mjs", "next.config.ts", "next.config.cjs", "next.config.mts"} {
		data, err := fs.ReadFile(fsys, path.Join(root, name))
		if err != nil {
			continue
		}
		c.read(name, string(data))
		break
	}

	// The middleware sits next to app/ or pages/: at the root, or in
	// src/ when they are there. Next.js 16 calls it proxy.
	for _, dir := range []string{path.Dir(appDir), root} {
		for _, base := range []string{"middleware", "proxy"} {
			for _, ext := range []string{".ts", ".js", ".mjs", ".mts"} {
				f := path.Join(dir, base+ext)
				data, err := fs.ReadFile(fsys, f)
				if err != nil {
					continue
				}
				c.middleware = f
				c.matchers = matchersOf(string(data))
				return c
			}
		}
	}
	return c
}

func (c *nextConfig) read(name, src string) {
	src = stripComments(src)
	if m := basePathLiteral.FindStringSubmatch(src); m != nil {
		c.basePath = strings.TrimSuffix(m[1], "/")
	} else if basePathAny.MatchString(src) {
		c.unread = append(c.unread, analysis.Doubt{Confidence: analysis.Uncertain,
			Reason: name + " sets a basePath pit cannot read; every address starts with it"})
	}
	c.i18n = i18nKey.MatchString(src)
	if m := pageExtensions.FindStringSubmatch(src); m != nil {
		for _, q := range quoted.FindAllStringSubmatch(m[1], -1) {
			if strings.Contains(q[1], ".") {
				c.unread = append(c.unread, analysis.Doubt{Confidence: analysis.Uncertain,
					Reason: fmt.Sprintf("%s sets pageExtensions to names like %q, which pit does not follow", name, q[1])})
				break
			}
		}
	}

	// Each source belongs to the redirects or rewrites written last
	// before it.
	keys := movesKey.FindAllStringSubmatchIndex(src, -1)
	for _, m := range sourceKey.FindAllStringSubmatchIndex(src, -1) {
		kind := ""
		for _, k := range keys {
			if k[0] < m[0] {
				kind = src[k[2]:k[3]]
			}
		}
		if kind != "redirects" && kind != "rewrites" {
			continue // headers() has sources too, and moves nothing
		}
		from := src[m[2]:m[3]]
		re, ok := pathPattern(from)
		if !ok {
			continue
		}
		obj := enclosingObject(src, m[0])
		applies, sometimes := onLocalhost(obj)
		if !applies {
			continue
		}
		to := ""
		if d := destinationKey.FindStringSubmatch(obj); d != nil {
			to = d[1]
		}
		c.moves = append(c.moves, move{kind: kind, source: re, from: from, to: to, sometimes: sometimes})
	}
}

// onLocalhost reads the has and missing conditions of a redirect or
// rewrite for a sandbox, which pit serves on localhost. A condition on
// the host is decided here; dub's redirects of /api/:path* apply only
// on other hosts and so not to a review. A condition on a cookie or a
// header makes the move happen sometimes.
func onLocalhost(obj string) (applies, sometimes bool) {
	applies = true
	for _, loc := range conditionKey.FindAllStringSubmatchIndex(obj, -1) {
		has := obj[loc[2]:loc[3]] == "has"
		list := obj[loc[1]-1:]
		end := closing(list)
		if end < 0 {
			continue
		}
		for _, item := range objects(list[:end+1]) {
			t := typeKey.FindStringSubmatch(item)
			if t == nil {
				continue
			}
			if t[1] != "host" {
				sometimes = true
				continue
			}
			v := valueKey.FindStringSubmatch(item)
			if v == nil {
				continue
			}
			re, err := regexp.Compile("^(?:" + strings.ReplaceAll(v[1], `\\`, `\`) + ")$")
			if err != nil {
				sometimes = true
				continue
			}
			if re.MatchString("localhost") != has {
				applies = false
			}
		}
	}
	return applies, sometimes
}

// enclosingObject is the { ... } around position i.
func enclosingObject(src string, i int) string {
	depth := 0
	start := -1
	for j := i; j >= 0; j-- {
		switch src[j] {
		case '}':
			depth++
		case '{':
			if depth == 0 {
				start = j
			} else {
				depth--
			}
		}
		if start >= 0 {
			break
		}
	}
	if start < 0 {
		return ""
	}
	end := closing(src[start:])
	if end < 0 {
		return src[start:]
	}
	return src[start : start+end+1]
}

// closing is the index of the bracket that closes the one src starts
// with, strings skipped, or -1.
func closing(src string) int {
	depth := 0
	for i := 0; i < len(src); i++ {
		switch c := src[i]; c {
		case '{', '[', '(':
			depth++
		case '}', ']', ')':
			depth--
			if depth == 0 {
				return i
			}
		case '"', '\'', '`':
			for i++; i < len(src) && src[i] != c; i++ {
				if src[i] == '\\' {
					i++
				}
			}
		}
	}
	return -1
}

// objects are the { ... } directly inside a list.
func objects(list string) []string {
	var out []string
	for i := 1; i < len(list); i++ {
		if list[i] == '{' {
			end := closing(list[i:])
			if end < 0 {
				return out
			}
			out = append(out, list[i:i+end+1])
			i += end
		}
	}
	return out
}

// doubts are what the configuration says about one address.
func (c nextConfig) doubts(r analysis.Route, pagesRouter bool) []analysis.Doubt {
	out := append([]analysis.Doubt(nil), c.unread...)
	sample := sampleAddress(r.Path)
	for _, m := range c.moves {
		if !m.source.MatchString(sample) {
			continue
		}
		verb := "redirects"
		if m.kind == "rewrites" {
			verb = "rewrites"
		}
		reason := fmt.Sprintf("next.config %s %s", verb, m.from)
		if m.to != "" {
			reason += " to " + m.to
		}
		level := analysis.Uncertain
		if m.sometimes {
			reason += ", on requests that meet its conditions"
			level = analysis.Likely
		}
		out = append(out, analysis.Doubt{Confidence: level, Reason: reason})
	}
	if c.middleware != "" && c.applies(sample) {
		out = append(out, analysis.Doubt{Confidence: analysis.Uncertain,
			Reason: c.middleware + " runs for this address and can rewrite or redirect it"})
	}
	if pagesRouter && c.i18n {
		out = append(out, analysis.Doubt{Confidence: analysis.Uncertain,
			Reason: "next.config sets i18n; the address may start with a locale"})
	}
	return out
}

// applies reports whether the middleware runs for an address. Without
// a matcher it runs for every one.
func (c nextConfig) applies(address string) bool {
	if len(c.matchers) == 0 {
		return true
	}
	for _, m := range c.matchers {
		if m(address) {
			return true
		}
	}
	return false
}

// matchersOf reads config.matcher from a middleware file: a string, a
// list of strings, or objects with a source. A matcher pit cannot read
// matches everything, which is the careful answer.
func matchersOf(src string) []matcher {
	src = stripComments(src)
	loc := matcherKey.FindStringIndex(src)
	if loc == nil {
		return nil
	}
	rest := strings.TrimLeft(src[loc[1]:], " \t\r\n")
	var patterns []string
	if strings.HasPrefix(rest, "[") {
		patterns = stringsUntilClose(rest)
	} else if m := quoted.FindStringSubmatch(rest); m != nil && strings.Index(rest, m[0]) == 0 {
		patterns = []string{m[1]}
	}
	if len(patterns) == 0 {
		return []matcher{func(string) bool { return true }}
	}
	var out []matcher
	for _, p := range patterns {
		out = append(out, matcherOf(p))
	}
	return out
}

// matcherOf turns one matcher pattern into a test. The form the Next.js
// documentation recommends, "/((?!api|_next/static).*)", excludes by
// a lookahead Go's regexp does not have; it is read as a list of
// excluded beginnings.
func matcherOf(p string) matcher {
	p = strings.ReplaceAll(p, `\\`, `\`)
	if inner, ok := strings.CutPrefix(p, "/((?!"); ok {
		if alts, tail, ok := strings.Cut(inner, ")"); ok && (tail == ".*)" || tail == ".*)$") {
			excluded, err := regexp.Compile("^(?:" + alts + ")")
			if err != nil {
				return func(string) bool { return true }
			}
			return func(address string) bool {
				return !excluded.MatchString(strings.TrimPrefix(address, "/"))
			}
		}
	}
	re, ok := pathPattern(p)
	if !ok {
		return func(string) bool { return true }
	}
	return re.MatchString
}

// pathPattern turns a path-to-regexp pattern, as next.config and
// matchers write them, into a regular expression: /blog/:slug,
// /docs/:path*, /api/:rest+.
func pathPattern(p string) (*regexp.Regexp, bool) {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(p); {
		switch {
		case p[i] == ':' || (p[i] == '/' && i+1 < len(p) && p[i+1] == ':'):
			slash := p[i] == '/'
			j := i + 1
			if slash {
				j++
			}
			k := j
			for k < len(p) && (p[k] == '_' || p[k] >= '0' && p[k] <= '9' || p[k]|0x20 >= 'a' && p[k]|0x20 <= 'z') {
				k++
			}
			mod := byte(0)
			if k < len(p) && (p[k] == '*' || p[k] == '+' || p[k] == '?') {
				mod = p[k]
				k++
			}
			switch {
			case mod == '*' && slash:
				b.WriteString(`(?:/.*)?`)
			case mod == '+' && slash:
				b.WriteString(`/.+`)
			case mod == '?' && slash:
				b.WriteString(`(?:/[^/]+)?`)
			case slash:
				b.WriteString(`/[^/]+`)
			default:
				b.WriteString(`[^/]+`)
			}
			i = k
		case p[i] == '(':
			// A custom group is already a regular expression.
			depth, j := 0, i
			for ; j < len(p); j++ {
				if p[j] == '(' {
					depth++
				} else if p[j] == ')' {
					depth--
					if depth == 0 {
						break
					}
				}
			}
			if j >= len(p) {
				return nil, false
			}
			b.WriteString(p[i : j+1])
			i = j + 1
		default:
			b.WriteString(regexp.QuoteMeta(p[i : i+1]))
			i++
		}
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	return re, err == nil
}

// sampleAddress stands in for a route's addresses: a value for each
// placeholder, so that patterns can be tried on it.
func sampleAddress(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		if strings.HasPrefix(s, "{") && strings.HasSuffix(s, "...}") {
			segs[i] = "x/y"
		} else if strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}") {
			segs[i] = "x"
		}
	}
	return strings.Join(segs, "/")
}

// stringsUntilClose reads the string literals of a list, up to the
// bracket that closes it, and the source of any object in it.
func stringsUntilClose(src string) []string {
	var out []string
	depth := 0
	for i := 0; i < len(src); i++ {
		switch c := src[i]; c {
		case '[', '{':
			depth++
		case ']', '}':
			depth--
			if depth == 0 {
				return out
			}
		case '"', '\'', '`':
			j := i + 1
			for j < len(src) && src[j] != c {
				if src[j] == '\\' {
					j++
				}
				j++
			}
			if j >= len(src) {
				return out
			}
			lit := src[i+1 : j]
			// In an object only the source is a path.
			if depth == 1 || strings.HasSuffix(strings.TrimSpace(src[max(0, i-20):i]), "source:") {
				out = append(out, lit)
			}
			i = j
		}
	}
	return out
}

// stripComments removes // and /* */ comments outside strings, so that
// a commented-out setting does not count.
func stripComments(src string) string {
	var b strings.Builder
	for i := 0; i < len(src); i++ {
		c := src[i]
		switch {
		case c == '"' || c == '\'' || c == '`':
			j := i + 1
			for j < len(src) && src[j] != c {
				if src[j] == '\\' {
					j++
				}
				j++
			}
			b.WriteString(src[i:min(j+1, len(src))])
			i = j
		case c == '/' && i+1 < len(src) && src[i+1] == '/':
			for i < len(src) && src[i] != '\n' {
				i++
			}
			b.WriteByte('\n')
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			end := strings.Index(src[i+2:], "*/")
			if end < 0 {
				return b.String()
			}
			i += end + 3
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}
