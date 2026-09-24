// Package analysistest provides an Analyzer that reads no files, so the
// code that uses heuristics can be tested for what it does with routes
// rather than for whether a framework's conventions were guessed right.
package analysistest

import (
	"context"
	"io/fs"
	"sync"

	"github.com/thannoz/pit/internal/analysis"
)

// Fake is an Analyzer that reports whatever routes it was given.
type Fake struct {
	mu sync.Mutex

	// Framework is what Name returns.
	Framework string
	// Found is what Routes returns.
	Found []analysis.Route
	// Err is what Routes fails with, so a test can see what a broken
	// heuristic does to a guide.
	Err error

	seen []fs.FS
}

// New returns a Fake for a framework that serves the given routes.
func New(framework string, routes ...analysis.Route) *Fake {
	return &Fake{Framework: framework, Found: routes}
}

var _ analysis.Analyzer = (*Fake)(nil)

// Name returns the framework the Fake stands in for.
func (f *Fake) Name() string { return f.Framework }

// Routes records which tree it was asked about and returns Found.
func (f *Fake) Routes(_ context.Context, fsys fs.FS) ([]analysis.Route, error) {
	f.mu.Lock()
	f.seen = append(f.seen, fsys)
	f.mu.Unlock()

	if f.Err != nil {
		return nil, f.Err
	}
	return append([]analysis.Route(nil), f.Found...), nil
}

// Seen returns the trees Routes was asked about, in order.
func (f *Fake) Seen() []fs.FS {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fs.FS(nil), f.seen...)
}
