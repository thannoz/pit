// Command pit brings up the running state of a pull request on the
// reviewer's own machine.
package main

import (
	"context"
	"errors"
	"os"

	"github.com/thannoz/pit/internal/cli"
	"github.com/thannoz/pit/internal/ui"
)

func main() {
	// os.Exit skips deferred calls, so the exit code travels back here
	// rather than being taken inside run: otherwise the signal handler
	// would never be stopped.
	os.Exit(run())
}

func run() int {
	ctx, stop := cli.WithInterrupt(context.Background())
	defer stop()

	err := cli.RunContext(ctx, os.Args[1:])
	switch {
	case err == nil:
		return 0
	case errors.Is(err, context.Canceled):
		// An interruption is the user's own doing, not a failure to
		// report back to them as one. 128 + SIGINT, the shell
		// convention.
		return 130
	default:
		// One place decides how a failure reaches the user: a message
		// and, where one exists, the next step. Never a stack trace.
		ui.Std().Error(err)
		return 1
	}
}
