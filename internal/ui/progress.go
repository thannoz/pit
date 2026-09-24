package ui

import (
	"fmt"
	"io"
	"sync"
	"time"
)

// spinnerFrames are the braille cells most terminals render cleanly.
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

const spinnerInterval = 100 * time.Millisecond

// Progress narrates a sequence of steps.
//
// It writes to stderr, not stdout: the answer of a command is its
// result, and a progress line in a pipe would end up in whatever the
// caller was collecting.
type Progress struct {
	out io.Writer
	tty bool

	mu      sync.Mutex
	name    string
	started time.Time
	stop    chan struct{}
	done    sync.WaitGroup
}

// NewProgress writes progress to w, animating only when w is a
// terminal.
func NewProgress(w io.Writer) *Progress {
	return newProgress(w, isTerminal(w))
}

// newProgress separates the decision from the detection, so a test can
// exercise the animated path without a pseudo-terminal.
func newProgress(w io.Writer, tty bool) *Progress {
	return &Progress{out: w, tty: tty}
}

// Begin announces that a step has started.
//
// streams says whether the step writes output of its own. When it does,
// no spinner is shown: it would be overwritten by the step's own lines
// and leave fragments behind. A build that prints its progress needs no
// spinner anyway.
func (p *Progress) Begin(name string, streams bool) {
	p.finishSpinner()

	p.mu.Lock()
	p.name = name
	p.started = time.Now()
	p.mu.Unlock()

	if p.tty && !streams {
		p.startSpinner(name)
	}
}

// Done reports that the step begun last has finished, with how long it
// took.
func (p *Progress) Done(format string, args ...any) {
	p.finishSpinner()

	p.mu.Lock()
	name, took := p.name, time.Since(p.started)
	p.name = ""
	p.mu.Unlock()

	detail := fmt.Sprintf(format, args...)
	// Progress is commentary. A failed write costs a narration line,
	// not a result, and there is nowhere useful to report it.
	_, _ = fmt.Fprintf(p.out, "  ✓ %-10s %s%s\n", name, detail, formatTook(took))
}

// Blank ends the narration with an empty line, so the answer that
// follows on stdout is not crowded against the last step.
func (p *Progress) Blank() {
	p.finishSpinner()
	_, _ = fmt.Fprintln(p.out)
}

// formatTook renders a duration, and nothing at all when the step was
// quick enough that nobody wondered.
func formatTook(d time.Duration) string {
	if d < 500*time.Millisecond {
		return ""
	}
	if d < 10*time.Second {
		return fmt.Sprintf("  (%.1fs)", d.Seconds())
	}
	return fmt.Sprintf("  (%ds)", int(d.Seconds()))
}

func (p *Progress) startSpinner(name string) {
	stop := make(chan struct{})

	p.mu.Lock()
	p.stop = stop
	p.mu.Unlock()

	p.done.Go(func() {

		ticker := time.NewTicker(spinnerInterval)
		defer ticker.Stop()

		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			case <-ticker.C:
				_, _ = fmt.Fprintf(p.out, "\r  %s %s", spinnerFrames[i%len(spinnerFrames)], name)
			}
		}
	})
}

// finishSpinner stops the animation and clears its line, so the next
// thing written starts on a blank one.
func (p *Progress) finishSpinner() {
	p.mu.Lock()
	stop := p.stop
	p.stop = nil
	p.mu.Unlock()

	if stop == nil {
		return
	}
	close(stop)
	p.done.Wait()

	// \r returns to the start, \033[K clears to the end of the line.
	_, _ = fmt.Fprint(p.out, "\r\033[K")
}
