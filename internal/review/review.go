// Package review puts together what a reviewer of one sandbox should
// look at: the addresses a pull request's changes lead to, as links into
// the running sandbox, and what clicking through them will not show.
package review

import (
	"cmp"
	"context"
	"io/fs"
	"slices"
	"strings"

	"github.com/thannoz/pit/internal/analysis"
	"github.com/thannoz/pit/internal/config"
	"github.com/thannoz/pit/internal/diff"
)

// Item is one address to visit.
type Item struct {
	// Number is its place on the list, from 1.
	Number int
	Kind   analysis.Kind
	// Methods are the HTTP methods the change reaches, when the route
	// says.
	Methods []string
	// Path is the address as the heuristic has it: /orders/{id}.
	Path string
	// URL is the link into the sandbox. It is empty when there is no
	// link that would lead to the right screen: a placeholder has no
	// example value, or part of the address could not be read.
	URL string
	// Missing are the placeholders without an example value.
	Missing []string
	// Files are the changed files that lead here.
	Files []string
	// Via are the ways a file that does not serve the address leads to
	// it.
	Via        []analysis.Trail
	Confidence analysis.Confidence
	Doubts     []string

	// Mark is how far the reviewer has got with it, CheckedAt the
	// commit it was looked at, and ChangedSince the files leading here
	// that have changed from that commit to this one.
	Mark         Mark
	CheckedAt    string
	ChangedSince []string
	// Visited says the mark comes from a request in the web service's
	// log rather than from the reviewer.
	Visited bool
}

// Checklist is everything a reviewer of one sandbox is shown.
type Checklist struct {
	// Base is the commit the change is measured from, Head the one
	// under review.
	Base, Head string
	// URL is the sandbox's root.
	URL string
	// Scenario is the data the sandbox holds; example values come from
	// it.
	Scenario string
	Items    []Item
	Warnings []analysis.Warning
	// Unplaced are changed files no address could be found for.
	Unplaced []analysis.File
	// Wide are changed files with more addresses than are listed.
	Wide []analysis.Reach
}

// Input is what a checklist is made from.
type Input struct {
	Diff diff.Diff
	// Base and Head are the trees before and after. Base may be nil.
	Base, Head fs.FS
	Config     *config.Config
	// Scenario names the data the sandbox holds.
	Scenario string
	// URL is the sandbox's root.
	URL string
	// Analyzers and Linkers are the heuristics to use.
	Analyzers []analysis.Analyzer
	Linkers   []analysis.Linker
}

// Build makes the checklist.
//
// Certain addresses come first, then likely ones, then uncertain ones;
// within each, pages before endpoints. The order is the order a
// reviewer with little time should go in.
func Build(ctx context.Context, in Input) (Checklist, error) {
	files := analysis.Classify(in.Diff, in.Config.Review.Ignore)

	guide, err := analysis.Entrypoints(ctx, in.Head, files, in.Analyzers, in.Linkers)
	if err != nil {
		return Checklist{}, err
	}
	warnings, err := analysis.Warnings(ctx, in.Base, in.Head, files, in.Analyzers)
	if err != nil {
		return Checklist{}, err
	}

	values := map[string]string{}
	if in.Scenario != "" {
		if v, err := in.Config.Params(in.Scenario); err == nil {
			values = v
		}
	}

	c := Checklist{
		Base: in.Diff.Base, Head: in.Diff.Head, URL: in.URL, Scenario: in.Scenario,
		Warnings: warnings, Wide: guide.Wide,
	}
	// A migration never has an address, and its warning already says
	// everything; listing it again as unplaced would say it twice.
	for _, f := range guide.Unplaced {
		if f.Kind != analysis.Migration {
			c.Unplaced = append(c.Unplaced, f)
		}
	}
	for _, e := range guide.Entrypoints {
		filled, missing := analysis.Fill(e.Path, values)
		item := Item{
			Kind: e.Kind, Methods: e.Methods, Path: e.Path, Missing: missing,
			Files: e.Files, Via: e.Via, Confidence: e.Confidence, Doubts: e.Doubts,
		}
		if len(missing) == 0 && !strings.Contains(filled, "…") && in.URL != "" {
			item.URL = strings.TrimSuffix(in.URL, "/") + filled
		}
		c.Items = append(c.Items, item)
	}
	slices.SortStableFunc(c.Items, func(a, b Item) int {
		return cmp.Or(
			-cmp.Compare(a.Confidence, b.Confidence),
			cmp.Compare(kindRank(a.Kind), kindRank(b.Kind)),
			cmp.Compare(a.Path, b.Path),
		)
	})
	for i := range c.Items {
		c.Items[i].Number = i + 1
	}
	return c, nil
}

func kindRank(k analysis.Kind) int {
	if k == analysis.Page {
		return 0
	}
	return 1
}
