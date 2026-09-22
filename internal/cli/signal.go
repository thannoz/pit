package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

// WithInterrupt returns a context that is cancelled when the user
// interrupts, and the function that stops listening.
//
// The first interrupt cancels: every long-running step takes a context,
// so the work stops and the cleanup registered along the way runs. A
// second interrupt exits immediately -- if the cleanup itself is stuck,
// the user must be able to get their terminal back, even at the price
// of leaving containers behind.
func WithInterrupt(parent context.Context) (context.Context, func()) {
	ctx, cancel := context.WithCancel(parent)

	ch := make(chan os.Signal, 2)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)

	go func() {
		select {
		case <-ch:
		case <-ctx.Done():
			return
		}

		// The wording has to be true of every command. `pit 482` is
		// cleaning up; `pit logs -f` has nothing to clean up and would
		// look broken if told it did.
		fmt.Fprintln(os.Stderr, "\nInterrupted; stopping. Press Ctrl+C again to give up waiting.")
		cancel()

		<-ch
		fmt.Fprintln(os.Stderr, "Gave up. `pit ls` shows what is still there.")
		os.Exit(130) // 128 + SIGINT, the shell convention
	}()

	return ctx, func() {
		signal.Stop(ch)
		cancel()
	}
}
