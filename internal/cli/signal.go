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

		fmt.Fprintln(os.Stderr, "\nInterrupted. Cleaning up; press Ctrl+C again to leave it.")
		cancel()

		<-ch
		fmt.Fprintln(os.Stderr, "Left as it is. `pit ls` shows what remains.")
		os.Exit(130) // 128 + SIGINT, the shell convention
	}()

	return ctx, func() {
		signal.Stop(ch)
		cancel()
	}
}
