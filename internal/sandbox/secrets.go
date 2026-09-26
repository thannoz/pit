package sandbox

import (
	"fmt"
	"maps"
	"slices"

	"github.com/thannoz/pit/internal/config"
	"github.com/thannoz/pit/internal/errs"
)

// newSecrets are the secrets the pull request's .pit.yaml names that
// the reviewer's does not, or fetches from somewhere else.
func newSecrets(mine, theirs *config.Config) []string {
	var fresh []string
	for _, n := range slices.Sorted(maps.Keys(theirs.Env.Secrets)) {
		if mine.Env.Secrets[n] != theirs.Env.Secrets[n] {
			fresh = append(fresh, n+" from "+theirs.Env.Secrets[n])
		}
	}
	return fresh
}

// confirmSecrets asks before fetching secrets the reviewer's checkout
// does not name: the pull request's services would get them, and a
// pull request can name any secret the reviewer can read.
func confirmSecrets(req UpRequest, mine *config.Config, rep Reporter) error {
	fresh := newSecrets(mine, req.Config)
	if len(fresh) == 0 {
		return nil
	}
	rep.Note("#%d gives its services %s your checkout does not:", req.PR.Number, plural(len(fresh), "secret", "secrets"))
	for _, s := range fresh {
		rep.Note("    %s", s)
	}
	if req.Confirm == nil {
		return errs.New("#%d wants %s of yours", req.PR.Number, plural(len(fresh), "secret", "secrets")).
			WithHint("run pit yourself and answer the question")
	}
	if !req.Confirm(fmt.Sprintf("Give %s to #%d?", pick(len(fresh), "it", "them"), req.PR.Number)) {
		return errs.New("stopped before fetching #%d's secrets", req.PR.Number).
			WithHint("nothing was started; run pit from a terminal to answer")
	}
	return nil
}
