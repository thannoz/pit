// Command pit brings up the running state of a pull request on the
// reviewer's own machine.
package main

import (
	"fmt"
	"os"

	"github.com/thannoz/pit/internal/cli"
)

func main() {
	if err := cli.Run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "pit:", err)
		os.Exit(1)
	}
}
