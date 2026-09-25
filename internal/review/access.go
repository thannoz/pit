package review

import (
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/thannoz/pit/internal/analysis"
	"github.com/thannoz/pit/internal/runtime"
	"github.com/thannoz/pit/internal/state"
)

// Visit is one request the web service logged.
type Visit struct {
	Method string
	Path   string
	At     time.Time
}

// requestLine finds a request in the forms servers log them: Next.js
// in development ("GET /orders 200 in 12ms"), gin ('| GET "/orders"'),
// nginx, uvicorn and Django ('"GET /orders HTTP/1.1" 200'), Rails
// ('Started GET "/orders"'), morgan, PHP's built-in server, chi's
// logger with the host in front. A full URL counts only for this
// machine: "GET https://api.stripe.com/v1/charges" is a request the
// application made, not one it answered.
var requestLine = regexp.MustCompile(`\b(GET|HEAD|POST|PUT|PATCH|DELETE|OPTIONS)\s+"?(?:https?://(?:localhost|127\.0\.0\.1|0\.0\.0\.0|\[::1\])(?::\d+)?)?(/[^\s"?#]*)`)

// Visits reads the requests from a service's log.
func Visits(lines []runtime.LogLine) []Visit {
	var out []Visit
	for _, l := range lines {
		if m := requestLine.FindStringSubmatch(l.Text); m != nil {
			out = append(out, Visit{Method: m[1], Path: m[2], At: l.At})
		}
	}
	return out
}

// Covered finds, for each item, the last visit that shows the reviewer
// looked at it: a request to its address with one of its methods, made
// after the sandbox came up with the commit under review. The last and
// not the first, because a mark taken back is weighed against it: a
// visit after taking it back is the reviewer looking again.
//
// pit's own requests do not count. The one that made sure the sandbox
// answers happened before it was recorded as up; a later one, when a
// running sandbox is reused, asked for the root, and a request for the
// root counts only after it. What pit inspect loaded happened within
// the spans own.
func Covered(list Checklist, visits []Visit, since, probed time.Time, own ...state.Span) map[int]time.Time {
	out := map[int]time.Time{}
	for i, it := range list.Items {
		re := addressPattern(it.Path)
		for _, v := range visits {
			if !v.At.After(since) || (v.Path == "/" && !v.At.After(probed)) || byPit(v, own) {
				continue
			}
			if !re.MatchString(v.Path) || !methodFits(it, v.Method) {
				continue
			}
			if last, ok := out[i]; !ok || v.At.After(last) {
				out[i] = v.At
			}
		}
	}
	return out
}

func byPit(v Visit, own []state.Span) bool {
	for _, s := range own {
		if s.Contains(v.At) {
			return true
		}
	}
	return false
}

// methodFits reports whether a request's method is one the item is
// about. A page is opened with GET, and a HEAD is the same request
// without the body.
func methodFits(it Item, method string) bool {
	methods := it.Methods
	if len(methods) == 0 {
		if it.Kind != analysis.Page {
			return true
		}
		methods = []string{"GET"}
	}
	return slices.Contains(methods, method) || (method == "HEAD" && slices.Contains(methods, "GET"))
}

// addressPattern turns an address into a pattern a request path is
// matched with: /orders/{id} matches /orders/42. An address with a part
// pit could not read keeps its "…" as it is, which no request path
// has: a visit to it cannot be told from one to somewhere else.
func addressPattern(p string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString("^")
	for _, seg := range strings.Split(strings.Trim(p, "/"), "/") {
		if seg == "" {
			continue
		}
		b.WriteString("/")
		switch {
		case strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "...}"):
			b.WriteString(".+")
		case strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}"):
			b.WriteString("[^/]+")
		default:
			b.WriteString(regexp.QuoteMeta(seg))
		}
	}
	b.WriteString("/?$")
	return regexp.MustCompile(b.String())
}

// RecordVisits marks the items a visit covers as looked at, and keeps
// that with the sandbox. An item looked at already at this commit is
// left as it is, and so is one whose mark was taken back after the
// visit: the reviewer said otherwise, and said it later.
func RecordVisits(store *state.Store, box state.Sandbox, list Checklist, covered map[int]time.Time) (state.Sandbox, error) {
	if len(covered) == 0 {
		return box, nil
	}
	err := store.Update(func(f *state.File) error {
		current, ok := f.Find(box.RepoRef, box.PR)
		if !ok {
			return nil
		}
		for _, i := range slices.Sorted(maps.Keys(covered)) {
			at := covered[i]
			address := list.Items[i].Address()
			k := slices.IndexFunc(current.Checked, func(c state.Check) bool { return c.Address == address })
			if k >= 0 {
				c := current.Checked[k]
				if (c.SHA == list.Head && !c.Undone) || (c.Undone && !at.After(c.At)) {
					continue
				}
				current.Checked = slices.Delete(current.Checked, k, k+1)
			}
			current.Checked = append(current.Checked, state.Check{Address: address, SHA: list.Head, At: at, Visited: true})
		}
		f.Put(current)
		box = current
		return nil
	})
	return box, err
}
