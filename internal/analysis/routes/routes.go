package routes

import (
	"strings"

	"github.com/thannoz/pit/internal/analysis"
	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/suggest"
)

// Auto is the review.routes.framework that asks every heuristic.
const Auto = "auto"

// registry is every heuristic pit knows, in order of precedence: when
// two find the same address, the earlier one names it. The key is what
// review.routes.framework calls it.
//
// A new framework is a new file in this package and a line here.
// Nothing outside the package changes.
var registry = []struct {
	key      string
	analyzer analysis.Analyzer
}{
	{"nextjs", NextJS{}},
	{"go", Go{}},
}

// All returns every heuristic, in order of precedence.
func All() []analysis.Analyzer {
	out := make([]analysis.Analyzer, 0, len(registry))
	for _, r := range registry {
		out = append(out, r.analyzer)
	}
	return out
}

// Frameworks returns what review.routes.framework accepts.
func Frameworks() []string {
	out := []string{Auto}
	for _, r := range registry {
		out = append(out, r.key)
	}
	return out
}

// For returns the heuristics review.routes.framework asks for: all of
// them for auto, which is the default, or the one it names.
//
// Naming one is for a project where another heuristic finds addresses
// that are not there -- a folder that happens to be called pages/ in an
// application that is not a Next.js one.
func For(framework string) ([]analysis.Analyzer, error) {
	if framework == "" || framework == Auto {
		return All(), nil
	}
	for _, r := range registry {
		if r.key == framework {
			return []analysis.Analyzer{r.analyzer}, nil
		}
	}

	err := errs.New("review.routes.framework is %q, which pit has no heuristic for", framework)
	if near := suggest.Closest(framework, Frameworks()); near != "" {
		return nil, err.WithHint("did you mean %q?", near)
	}
	return nil, err.WithHint("known: %s", strings.Join(Frameworks(), ", "))
}
