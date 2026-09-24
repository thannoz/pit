package analysis

import (
	"cmp"
	"context"
	"fmt"
	"io/fs"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/thannoz/pit/internal/diff"
	"github.com/thannoz/pit/internal/errs"
)

// WarningKind is what a warning is about.
type WarningKind string

// The kinds, in the order a guide lists them.
const (
	// SchemaChange: the schema changes, and the data that exists
	// already is what it has to survive.
	SchemaChange WarningKind = "migration"
	// RemovedAddress: an address that answered before no longer does.
	RemovedAddress WarningKind = "removed"
	// PermissionChange: who may do what.
	PermissionChange WarningKind = "permissions"
	// ErrorHandling: what happens when something fails, which a
	// reviewer clicking through the happy path never sees.
	ErrorHandling WarningKind = "errors"
)

var kindOrder = []WarningKind{SchemaChange, RemovedAddress, PermissionChange, ErrorHandling}

// Warning is something a reviewer should not miss, whatever the rest of
// the guide says: the parts of a change that clicking through pages
// does not show.
type Warning struct {
	Kind WarningKind
	// Serious marks what can lose data or open a door: a migration
	// that drops something, a removed endpoint, a check that is gone.
	Serious bool
	// File is the changed file it is about.
	File string
	// Address is the address it is about, for RemovedAddress.
	Address string
	// Lines are where, in the file as it is -- or as it was, for lines
	// that were removed.
	Lines []int
	// Message says it in a sentence.
	Message string

	names   []string // the kinds of line it found
	removes bool
}

// moved notes, on a check a file removes, where the change adds a check
// of the same kind: dub#4534 takes "you cannot delete the default
// group" out of a route and puts it into the function the route now
// calls. The warning stays serious -- whether the moved check still
// holds on every path is exactly what a reviewer has to look at -- but
// it says where to look.
func moved(ws []Warning) {
	for i, w := range ws {
		if !w.removes {
			continue
		}
		var elsewhere []string
		for _, o := range ws {
			if o.Kind == w.Kind && !o.removes && o.File != w.File &&
				slices.ContainsFunc(o.names, func(n string) bool { return slices.Contains(w.names, n) }) {
				elsewhere = append(elsewhere, o.File)
			}
		}
		if len(elsewhere) > 0 {
			ws[i].Message += "; the change adds the same kind of check in " + strings.Join(elsewhere, ", ")
		}
	}
}

// Warnings finds what a guide should call out. base and head are the
// trees before and after; base may be nil, and then what only the old
// tree can tell -- a removed address, a removed line -- is not looked
// for.
//
// By the text of the changed lines, not by parsing: a permission check
// looks much the same in every language, and a warning that fires on a
// line that merely mentions a check costs a glance, where one that
// misses a removed check costs the review.
func Warnings(ctx context.Context, base, head fs.FS, files []File, analyzers []Analyzer) ([]Warning, error) {
	var out []Warning
	changed := map[string]File{}
	for _, f := range files {
		if f.OnChecklist() {
			changed[f.Path] = f
		}
	}

	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !f.OnChecklist() {
			continue
		}
		added, removed := changedText(base, head, f.File)
		if f.Kind == Migration {
			out = append(out, migration(f, added, files))
			continue
		}
		out = append(out, scan(f, PermissionChange, permissionWords, added, removed)...)
		out = append(out, scan(f, ErrorHandling, errorWords, added, removed)...)
	}

	moved(out)

	if base != nil {
		removed, err := removedAddresses(ctx, base, head, changed, analyzers)
		if err != nil {
			return nil, err
		}
		out = append(out, removed...)
	}

	slices.SortStableFunc(out, func(a, b Warning) int {
		return cmp.Or(
			-compareBool(a.Serious, b.Serious),
			cmp.Compare(slices.Index(kindOrder, a.Kind), slices.Index(kindOrder, b.Kind)),
			cmp.Compare(a.File, b.File),
			cmp.Compare(a.Address, b.Address),
		)
	})
	return out, nil
}

func compareBool(a, b bool) int {
	switch {
	case a == b:
		return 0
	case a:
		return 1
	default:
		return -1
	}
}

// line is one changed line and its number.
type line struct {
	n    int
	text string
}

// changedText reads the lines a file's hunks added, from the new tree,
// and the lines they removed, from the old one.
func changedText(base, head fs.FS, f diff.File) (added, removed []line) {
	read := func(fsys fs.FS, name string) []string {
		if fsys == nil || f.Binary {
			return nil
		}
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil
		}
		return strings.Split(string(data), "\n")
	}
	pick := func(lines []string, r diff.Range) []line {
		var out []line
		for n := r.Start; n < r.Start+r.Count && n-1 < len(lines); n++ {
			if n >= 1 {
				out = append(out, line{n, lines[n-1]})
			}
		}
		return out
	}

	if f.Change != diff.Deleted {
		now := read(head, f.Path)
		for _, h := range f.Hunks {
			added = append(added, pick(now, h.New)...)
		}
	}
	old := f.Path
	if f.OldPath != "" {
		old = f.OldPath
	}
	before := read(base, old)
	for _, h := range f.Hunks {
		removed = append(removed, pick(before, h.Old)...)
	}
	return added, removed
}

// word is a pattern for one kind of line, with the name a warning uses
// for it.
type word struct {
	name string
	re   *regexp.Regexp
}

type words []word

func pattern(name, re string) word { return word{name, regexp.MustCompile(re)} }

// Kept narrow on purpose: role alone is in every role="button", policy
// in every Referrer-Policy. A warning that fires on everything is one
// nobody reads.
var permissionWords = words{
	pattern("permission", `(?i)\bpermissions?\b|\b(has|check|require|ensure|verify)_?(permission|role|access)\w*`),
	pattern("authorization", `(?i)\bauthori[sz](e|ed|es|ing|ation)\w*|\bunauthori[sz]ed\b|\bforbidden\b`),
	pattern("admin check", `(?i)\bis_?(admin|owner|superuser|staff)\b`),
	// A role compared with what roles in access control are called, not
	// with "assistant": in an LLM application every message has a role.
	pattern("role check", `(?i)\broles?\s*(===|!==|==|!=)\s*["'`+"`"+`]?(admin|owner|member|editor|viewer|manager|moderator|staff|superuser|superadmin|guest|billing)\b|\b\w*Role\.(admin|owner|member|editor|viewer|manager)\b|\b(user|member|session|me|account)\??\.roles?\b|\broles\.includes\(`),
	pattern("auth guard", `(?i)\b(require|with|ensure|is)_?(auth|authenticated|login|user|session)\w*\s*\(|\bgetServerSession\b|\bcurrentUser\b`),
	pattern("access list", `(?i)\b(rbac|acl)\b`),
	pattern("401/403", `\b(401|403)\b|Status(Unauthorized|Forbidden)\b`),
}

var errorWords = words{
	pattern("err check", `\bif\s+err\s*!=\s*nil\b|\berrors\.(New|Is|As|Join)\b|\bfmt\.Errorf\b`),
	pattern("panic", `\bpanic\(|\brecover\(\)`),
	pattern("try/catch", `\btry\s*[{:]|\bcatch\s*[({]|\.catch\(|\bfinally\s*[{:]|\bexcept\b[^\n]*:`),
	pattern("throw", `\bthrow\s|\braise\s+\w`),
	pattern("error status", `\bStatus(InternalServerError|BadRequest|NotFound|Conflict|UnprocessableEntity|ServiceUnavailable|BadGateway|GatewayTimeout)\b|\bstatus(Code)?\s*[:=(]\s*[45]\d\d\b|\bnotFound\(\)`),
}

// scan looks for one kind of line among the changed ones.
//
// A check counts as removed only where the change has fewer of it than
// before: an if statement that is reformatted is on both sides of the
// diff and has not gone anywhere. Removed, for permissions, is serious
// -- a check that is gone is the one change a reviewer cannot click
// their way to.
func scan(f File, kind WarningKind, w words, added, removed []line) []Warning {
	count := func(lines []line) (map[string]int, map[string][]int) {
		n, at := map[string]int{}, map[string][]int{}
		for _, l := range lines {
			for _, p := range w {
				if p.re.MatchString(l.text) {
					n[p.name]++
					at[p.name] = append(at[p.name], l.n)
				}
			}
		}
		return n, at
	}
	nowN, nowAt := count(added)
	thenN, thenAt := count(removed)

	what := "error handling"
	if kind == PermissionChange {
		what = "permission checks"
	}
	var out []Warning
	var gone []string
	var goneAt []int
	for _, p := range w {
		if thenN[p.name] > nowN[p.name] {
			gone = append(gone, p.name)
			goneAt = append(goneAt, thenAt[p.name]...)
		}
	}
	if len(gone) > 0 {
		goneAt = slices.Compact(slices.Sorted(slices.Values(goneAt)))
		out = append(out, Warning{
			Kind: kind, Serious: kind == PermissionChange, File: f.Path, Lines: goneAt, names: gone, removes: true,
			Message: fmt.Sprintf("%s removes %s: %s, at lines %s as they were", f.Path, what, strings.Join(gone, ", "), spans(goneAt)),
		})
	}
	var names []string
	var at []int
	for _, p := range w {
		if nowN[p.name] > 0 {
			names = append(names, p.name)
			at = append(at, nowAt[p.name]...)
		}
	}
	if len(at) > 0 {
		at = slices.Compact(slices.Sorted(slices.Values(at)))
		out = append(out, Warning{
			Kind: kind, File: f.Path, Lines: at, names: names,
			Message: fmt.Sprintf("%s changes %s: %s, at lines %s", f.Path, what, strings.Join(names, ", "), spans(at)),
		})
	}
	return out
}

var destructive = regexp.MustCompile(`(?i)\b(drop\s+(table|column|index|constraint|schema|database|type|view)|truncate\b|delete\s+from|alter\s+table\s+\S+\s+(drop|rename)|alter\s+column\s+\S+\s+(type|set\s+not\s+null)|rename\s+(column|to)\b)`)

// migration describes a changed migration. Adding one is what migrations
// are for; editing or deleting one that already exists is not, because
// every database that ran it keeps what it did.
func migration(f File, added []line, files []File) Warning {
	w := Warning{Kind: SchemaChange, File: f.Path}
	schema := strings.HasSuffix(f.Path, ".prisma")
	switch {
	case f.Change == diff.Deleted && !schema:
		w.Serious = true
		w.Message = f.Path + " is deleted; databases that already ran it keep what it did, and new ones will not get it"
		return w
	case schema:
		w.Message = "the data model " + f.Path + " changes"
		if !slices.ContainsFunc(files, func(o File) bool { return o.Kind == Migration && !strings.HasSuffix(o.Path, ".prisma") }) {
			w.Message += ", and no migration comes with it"
		}
		w.Message += "; check the data the sandbox already has"
	case f.Change == diff.Modified:
		w.Serious = true
		w.Message = f.Path + " is an existing migration and is changed; databases that already ran it will not run it again"
	default:
		w.Message = f.Path + " changes the schema"
		if what := statements(added); what != "" {
			w.Message += " (" + what + ")"
		}
		w.Message += "; check the data that exists before it runs"
	}

	var what []string
	for _, l := range added {
		if m := destructive.FindString(l.text); m != "" {
			w.Lines = append(w.Lines, l.n)
			m = strings.Join(strings.Fields(m), " ")
			if !slices.Contains(what, m) {
				what = append(what, m)
			}
		}
	}
	if len(what) > 0 {
		w.Serious = true
		w.Message += fmt.Sprintf("; it can destroy data: %s at lines %s", strings.Join(what, ", "), spans(w.Lines))
	}
	return w
}

var statement = regexp.MustCompile(`(?i)^\s*(create|alter|drop|insert|update|delete|truncate|rename)\b`)

// statements says what the added lines of a SQL migration do, in their
// own words: the first few statements, one line each.
func statements(added []line) string {
	const shown = 3
	var found []string
	for _, l := range added {
		if statement.MatchString(l.text) {
			found = append(found, strings.TrimSuffix(strings.Join(strings.Fields(l.text), " "), ";"))
		}
	}
	switch {
	case len(found) == 0:
		return ""
	case len(found) <= shown:
		return strings.Join(found, "; ")
	default:
		return strings.Join(found[:shown], "; ") + fmt.Sprintf("; and %d more", len(found)-shown)
	}
}

// removedAddresses are the addresses the old tree answers at and the new
// one does not, where a file of the change served them.
func removedAddresses(ctx context.Context, base, head fs.FS, changed map[string]File, analyzers []Analyzer) ([]Warning, error) {
	oldPaths := map[string]File{}
	for _, f := range changed {
		old := f.Path
		if f.OldPath != "" {
			old = f.OldPath
		}
		oldPaths[old] = f
	}

	now := map[string]bool{}
	movedTo := map[string][]string{} // new file -> addresses it answers at
	var before []Route
	for _, a := range analyzers {
		after, err := a.Routes(ctx, head)
		if err != nil {
			return nil, errs.Wrap(err, "finding %s routes", a.Name())
		}
		for _, r := range after {
			now[r.Method+" "+r.Path] = true
			movedTo[r.File] = append(movedTo[r.File], r.Path)
		}
		then, err := a.Routes(ctx, base)
		if err != nil {
			return nil, errs.Wrap(err, "finding %s routes before the change", a.Name())
		}
		before = append(before, then...)
	}

	var out []Warning
	seen := map[string]bool{}
	for _, r := range before {
		address := strings.TrimSpace(r.Method + " " + r.Path)
		f, ok := oldPaths[r.File]
		if !ok || now[r.Method+" "+r.Path] || seen[address] {
			continue
		}
		seen[address] = true
		w := Warning{Kind: RemovedAddress, File: f.Path, Address: address, Serious: r.Kind == Endpoint}
		switch {
		case f.Change == diff.Deleted:
			w.Message = fmt.Sprintf("%s no longer answers: %s is deleted", address, f.Path)
		case f.Change == diff.Renamed && len(movedTo[f.Path]) > 0:
			w.Message = fmt.Sprintf("%s no longer answers: %s moved to %s, which answers at %s",
				address, f.OldPath, f.Path, strings.Join(slices.Compact(slices.Sorted(slices.Values(movedTo[f.Path]))), ", "))
		default:
			w.Message = fmt.Sprintf("%s no longer answers; %s served it", address, f.Path)
		}
		out = append(out, w)
	}
	return out, nil
}

// spans writes line numbers compactly: 3, 7–9, 12. Past a handful it
// says how many, which is what a reader takes from a long list anyway.
func spans(lines []int) string {
	const listed = 6
	ls := slices.Sorted(slices.Values(lines))
	var parts []string
	for i := 0; i < len(ls); {
		j := i
		for j+1 < len(ls) && ls[j+1] == ls[j]+1 {
			j++
		}
		if j > i {
			parts = append(parts, strconv.Itoa(ls[i])+"–"+strconv.Itoa(ls[j]))
		} else {
			parts = append(parts, strconv.Itoa(ls[i]))
		}
		i = j + 1
	}
	if len(parts) > listed {
		return fmt.Sprintf("%d places, the first %s", len(ls), parts[0])
	}
	return strings.Join(parts, ", ")
}
