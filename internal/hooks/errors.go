package hooks

import "github.com/thannoz/pit/internal/errs"

// wrapLine says which configured line went wrong. A list of hooks that
// reports only "unbalanced quote" leaves the author hunting.
func wrapLine(err error, index int, line string) error {
	return errs.Wrap(err, "hook %d (%q) cannot be run", index+1, line)
}
