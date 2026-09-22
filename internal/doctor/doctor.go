// Package doctor checks whether this machine can run pit, and says what
// to do about whatever it cannot.
package doctor

import (
	"context"
	"strings"
)

// Result is how a check turned out.
type Result string

const (
	// OK means the check passed.
	OK Result = "ok"
	// Warn means pit works, but something is worth knowing.
	Warn Result = "warn"
	// Fail means pit cannot do its job until this is fixed.
	Fail Result = "fail"
)

// Finding is one check's outcome.
type Finding struct {
	// Name is what was checked.
	Name string `json:"name"`
	// Result is how it went.
	Result Result `json:"result"`
	// Detail says what was found.
	Detail string `json:"detail"`
	// Fix says what to do, when there is something to do.
	Fix string `json:"fix,omitempty"`
}

// Report is everything that was checked.
type Report struct {
	Findings []Finding `json:"findings"`
}

// Failed reports whether anything is broken enough to stop pit working.
func (r Report) Failed() bool {
	for _, f := range r.Findings {
		if f.Result == Fail {
			return true
		}
	}
	return false
}

// Counts summarises the report.
func (r Report) Counts() (ok, warn, fail int) {
	for _, f := range r.Findings {
		switch f.Result {
		case OK:
			ok++
		case Warn:
			warn++
		case Fail:
			fail++
		}
	}
	return ok, warn, fail
}

// Check is one thing to look at. Checks are values so that the list
// reads as a list, and a test can run one on its own.
type Check func(ctx context.Context) Finding

// Run performs the checks in order.
//
// Order matters: a failure early on explains the ones after it, and a
// report that starts with "docker is not installed" is easier to act on
// than one that starts with the consequences.
func Run(ctx context.Context, checks []Check) Report {
	findings := make([]Finding, 0, len(checks))
	for _, check := range checks {
		findings = append(findings, check(ctx))
	}
	return Report{Findings: findings}
}

// firstLine keeps a tool's banner to one line; several of them print
// more than one.
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
