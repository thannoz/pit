// Package cli wires command-line arguments to the packages that do the
// work. It parses flags and dispatches; it holds no logic of its own.
package cli

import (
	"context"
	"strings"

	"github.com/thannoz/pit/internal/errs"
)

// unknownCommandPrefix is how cobra opens its message for a command it
// does not know.
const unknownCommandPrefix = "unknown command"

// Run executes the command described by args and returns an error for
// main to report. Usage text is printed by cobra; errors are not, so
// that main controls how they reach the user.
func Run(args []string) error {
	return RunContext(context.Background(), args)
}

// RunContext is Run with a context that carries cancellation, so that
// Ctrl+C reaches every step that takes one.
func RunContext(ctx context.Context, args []string) error {
	root := newRootCmd()
	root.SetArgs(args)
	root.SetContext(ctx)

	return postProcess(root.Execute())
}

// postProcess turns cobra's own errors into ones that say what to do.
// It is separate so that tests can take the same path the binary does.
func postProcess(err error) error {
	if err == nil {
		return nil
	}
	// An error that already knows what to suggest keeps its own hint.
	// Adding one here would mask it: Hint returns the outermost, and a
	// generic "see --help" is worse than "did you mean ls?".
	if errs.Hint(err) != "" {
		return err
	}
	// cobra states what was wrong with the input but not what to do
	// about it, which is the one thing the user actually needs.
	if strings.HasPrefix(err.Error(), unknownCommandPrefix) {
		return errs.Hinted(err, "run %q to see the available commands", "pit --help")
	}
	return err
}
