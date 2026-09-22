// Package cli wires command-line arguments to the packages that do the
// work. It parses flags and dispatches; it holds no logic of its own.
package cli

// Run executes the command described by args and returns an error for
// main to report. Usage text is printed by cobra; errors are not, so
// that main controls how they reach the user.
func Run(args []string) error {
	root := newRootCmd()
	root.SetArgs(args)
	return root.Execute()
}
