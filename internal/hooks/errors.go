package hooks

import "github.com/thannoz/pit/internal/errs"

// wrapLine says which configured line went wrong. A list of hooks that
// reports only "unbalanced quote" or "exit code 1" leaves the author
// hunting through the file for which one it was.
func wrapLine(err error, index int, line string) error {
	return errs.Wrap(err, "hook %d failed: %q", index+1, line).
		WithHint("this is hooks.after_up entry %d in %s", index+1, "the .pit.yaml")
}
