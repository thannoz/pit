package hooks

import "github.com/thannoz/pit/internal/errs"

// wrapLine says which configured line went wrong. A list of commands
// that reports only "unbalanced quote" or "exit code 1" leaves the
// author hunting through the file for which one it was.
//
// It adds no hint of its own. Whatever the cause knows -- that a
// program is not installed, that a quote is unbalanced -- is more
// useful than anything that could be said here, and errs.Hint takes
// the outermost hint it finds, so a generic one would be printed
// instead of it. Saying "this is entry 1 under data.migrate" below a
// message that already says so is not advice, it is an echo.
func wrapLine(err error, l List, index int) error {
	return errs.Wrap(err, "%s entry %d failed: %q", l.Path, index+1, l.Lines[index])
}
