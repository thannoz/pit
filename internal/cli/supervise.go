package cli

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/thannoz/pit/internal/runtime/local"
)

// superviseCommand is the hidden command pit runs itself with, detached,
// to keep a sandbox's processes running once pit is gone.
const superviseCommand = "__supervise"

func newSuperviseCmd() *cobra.Command {
	return &cobra.Command{
		Use:    superviseCommand + " <dir>",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			// Not the command's context: the supervisor answers to
			// signals itself, and stops its processes when it does.
			return local.Supervise(context.Background(), args[0])
		},
	}
}
