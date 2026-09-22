// Package cli wires command-line arguments to the packages that do the
// work. It parses flags and dispatches; it holds no logic of its own.
package cli

import "fmt"

// Set via -ldflags at build time; see the Makefile.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

// Run executes the command described by args. Command parsing moves to
// cobra in T-002.
func Run(args []string) error {
	if len(args) > 0 && args[0] == "version" {
		fmt.Printf("pit %s (%s, built %s)\n", version, commit, date)
		return nil
	}
	return fmt.Errorf("no commands implemented yet")
}
