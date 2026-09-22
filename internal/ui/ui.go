// Package ui renders progress, messages and errors for a human reader.
// It is the only package that writes to stdout.
//
// Diagnostic logging is a separate concern and goes through log/slog to
// stderr; see internal/cli. Keeping the two apart is what makes `pit ls
// | jq` work: stdout carries the answer, stderr carries the commentary.
package ui

import (
	"fmt"
	"io"
	"os"

	"github.com/thannoz/pit/internal/errs"
)

// ANSI codes, used only when the destination is a terminal.
const (
	ansiReset = "\033[0m"
	ansiRed   = "\033[31m"
	ansiCyan  = "\033[36m"
	ansiDim   = "\033[2m"
)

// Printer writes user facing output. Answers go to out, commentary and
// failures to err.
type Printer struct {
	out   io.Writer
	err   io.Writer
	color bool
	// writeErr holds the first failed write. Returning an error from
	// every print call would make the API unusable, so failures are
	// remembered the way bufio.Writer does it and read back once.
	writeErr error
}

// New returns a Printer writing to out and err. Colour is enabled only
// when err is a terminal and the environment does not opt out, so piped
// and redirected output stays free of escape sequences.
func New(out, err io.Writer) *Printer {
	return &Printer{out: out, err: err, color: colorEnabled(err)}
}

// Std returns a Printer writing to the process's stdout and stderr.
func Std() *Printer { return New(os.Stdout, os.Stderr) }

// Err returns the first write that failed, or nil. Worth checking once
// at the end of a command: output that silently vanished is worse than
// a command that admits it could not write.
func (p *Printer) Err() error { return p.writeErr }

// note records a failed write. Its signature matches what the fmt.Fprint
// family returns, so calls stay one line.
func (p *Printer) note(_ int, err error) {
	if err != nil && p.writeErr == nil {
		p.writeErr = err
	}
}

// Out returns the writer carrying the answer, for callers that render
// their own output such as JSON encoders.
func (p *Printer) Out() io.Writer { return p.out }

// Printf writes an answer to stdout.
func (p *Printer) Printf(format string, args ...any) {
	p.note(fmt.Fprintf(p.out, format, args...))
}

// Println writes an answer to stdout, followed by a newline.
func (p *Printer) Println(args ...any) {
	p.note(fmt.Fprintln(p.out, args...))
}

// Warnf writes a warning to stderr. It is commentary, not an answer, so
// it never lands on stdout.
func (p *Printer) Warnf(format string, args ...any) {
	p.note(fmt.Fprintf(p.err, "%swarning:%s %s\n", p.paint(ansiDim), p.paint(ansiReset), fmt.Sprintf(format, args...)))
}

// Error writes a failure to stderr, followed by its hint when the error
// carries one. It prints nothing for a nil error.
func (p *Printer) Error(err error) {
	if err == nil {
		return
	}
	p.note(fmt.Fprintf(p.err, "%spit:%s %s\n", p.paint(ansiRed), p.paint(ansiReset), err))
	if hint := errs.Hint(err); hint != "" {
		p.note(fmt.Fprintf(p.err, "%shint:%s %s\n", p.paint(ansiCyan), p.paint(ansiReset), hint))
	}
}

// paint returns the escape code when colour is on, and nothing when it
// is off, so format strings stay identical either way.
func (p *Printer) paint(code string) string {
	if !p.color {
		return ""
	}
	return code
}

// colorEnabled reports whether w is a terminal that wants colour. It
// honours the NO_COLOR convention and TERM=dumb.
func colorEnabled(w io.Writer) bool {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return false
	}
	return isTerminal(w)
}

// isTerminal reports whether w is a character device, which is the
// cheapest reliable check without pulling in a dependency.
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}
