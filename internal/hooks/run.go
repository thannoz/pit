package hooks

import (
	"context"
	"io"
	"log/slog"
	"time"

	"github.com/thannoz/pit/internal/proc"
)

// Runner runs external commands. internal/proc.Exec satisfies it.
type Runner interface {
	Stream(ctx context.Context, c proc.Command, stdout, stderr io.Writer) error
}

// Run executes the configured lines in order and stops at the first
// failure.
//
// Order is the author's, and it matters: a seed that runs before its
// migration fails in a way that is tedious to diagnose. Carrying on
// after a failure would produce a sandbox that looks ready and is not,
// which is worse than no sandbox at all.
func Run(ctx context.Context, r Runner, lines []string, s Sandbox, stdout, stderr io.Writer) error {
	cmds, err := ExpandAll(lines, s)
	if err != nil {
		return err
	}

	for i, cmd := range cmds {
		started := time.Now()
		slog.DebugContext(ctx, "running hook", "index", i+1, "line", lines[i])

		if err := r.Stream(ctx, cmd, stdout, stderr); err != nil {
			// proc already names the command, its exit code and the
			// tail of its stderr; what it cannot know is which
			// configured line this was.
			return wrapLine(err, i, lines[i])
		}

		slog.DebugContext(ctx, "hook finished", "index", i+1, "took", time.Since(started))
	}
	return nil
}
