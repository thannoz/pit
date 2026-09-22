// Command pit brings up the running state of a pull request on the
// reviewer's own machine.
package main

import (
	"os"

	"github.com/thannoz/pit/internal/cli"
	"github.com/thannoz/pit/internal/ui"
)

func main() {
	if err := cli.Run(os.Args[1:]); err != nil {
		// One place decides how a failure reaches the user: a message
		// and, where one exists, the next step. Never a stack trace.
		ui.Std().Error(err)
		os.Exit(1)
	}
}
