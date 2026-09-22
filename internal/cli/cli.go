// Package cli wires command-line arguments to the packages that do the
// work. It parses flags and dispatches; it holds no logic of its own.
package cli

import (
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
	root := newRootCmd()
	root.SetArgs(args)

	err := root.Execute()
	if err == nil {
		return nil
	}
	// cobra states what was wrong with the input but not what to do
	// about it; that is the one thing the user actually needs. There is
	// no typed error to match on, so this keys off cobra's wording —
	// TestCobraStillSaysUnknownCommand pins that assumption.
	if strings.HasPrefix(err.Error(), unknownCommandPrefix) {
		return errs.Hinted(err, "run %q to see the available commands", "pit --help")
	}
	return err
}
