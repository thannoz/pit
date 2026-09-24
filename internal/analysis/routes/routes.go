package routes

import "github.com/thannoz/pit/internal/analysis"

// All returns every heuristic pit knows, in order of precedence: when
// two find the same address, the earlier one names it.
//
// A new framework is a new file in this package and a line here.
// Nothing outside the package changes.
func All() []analysis.Analyzer {
	return []analysis.Analyzer{}
}
