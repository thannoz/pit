package hooks

import "github.com/thannoz/pit/internal/errs"

// wrapLine says which configured line went wrong. A list of commands
// that reports only "unbalanced quote" or "exit code 1" leaves the
// author hunting through the file for which one it was.
//
// It adds a hint only when the cause carries none. errs.Hint takes the
// outermost hint it finds, so a generic one here would be printed
// instead of the specific advice tokenize and proc already give.
func wrapLine(err error, l List, index int) error {
	wrapped := errs.Wrap(err, "%s entry %d failed: %q", l.Path, index+1, l.Lines[index])
	if errs.Hint(err) != "" {
		return wrapped
	}
	return wrapped.WithHint("this is entry %d under %s in the .pit.yaml", index+1, l.Path)
}
