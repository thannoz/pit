// Package forgetest provides a Forge that needs no network and no
// GitHub account.
package forgetest

import (
	"context"
	"sync"

	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/forge"
)

// Fake answers from a table of pull requests.
type Fake struct {
	mu sync.Mutex

	// PRs are the pull requests this forge knows about.
	PRs map[int]forge.PR
	// Err, when set, is returned instead of any answer.
	Err error

	asked []int
}

// New returns a Fake holding one ordinary open pull request, which is
// what most tests need.
func New(prs ...forge.PR) *Fake {
	f := &Fake{PRs: map[int]forge.PR{}}
	if len(prs) == 0 {
		prs = []forge.PR{{
			Number:     482,
			Title:      "Rework the checkout flow",
			Author:     "lisa",
			Branch:     "feat/checkout-flow",
			HeadSHA:    "a3f91c2e4b7d8091a2b3c4d5e6f708192a3b4c5d",
			BaseSHA:    "8c21f0d1e2b3a4958677889900aabbccddeeff00",
			BaseBranch: "main",
			State:      forge.Open,
			URL:        "https://github.com/acme/shop/pull/482",
		}}
	}
	for _, pr := range prs {
		f.PRs[pr.Number] = pr
	}
	return f
}

var _ forge.Forge = (*Fake)(nil)

// PullRequest returns the stored pull request.
func (f *Fake) PullRequest(_ context.Context, number int) (forge.PR, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.asked = append(f.asked, number)
	if f.Err != nil {
		return forge.PR{}, f.Err
	}

	pr, ok := f.PRs[number]
	if !ok {
		return forge.PR{}, errs.New("no pull request #%d", number).
			WithHint("check the number")
	}
	return pr, nil
}

// Asked returns the numbers this forge was asked about, in order.
func (f *Fake) Asked() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int(nil), f.asked...)
}
