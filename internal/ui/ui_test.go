package ui

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/thannoz/pit/internal/errs"
)

// hasANSI reports whether s carries terminal escape sequences.
func hasANSI(s string) bool { return strings.Contains(s, "\033[") }

func newTest() (p *Printer, out, errOut *bytes.Buffer) {
	out, errOut = &bytes.Buffer{}, &bytes.Buffer{}
	return New(out, errOut), out, errOut
}

func TestAnswersGoToStdoutAndCommentaryToStderr(t *testing.T) {
	p, out, errOut := newTest()

	p.Println("the answer")
	p.Warnf("something is off")
	p.Error(errors.New("it broke"))

	if !strings.Contains(out.String(), "the answer") {
		t.Errorf("stdout = %q, want the answer", out)
	}
	// This is what makes `pit ls | jq` work.
	for _, unwanted := range []string{"something is off", "it broke"} {
		if strings.Contains(out.String(), unwanted) {
			t.Errorf("stdout = %q, must not contain %q", out, unwanted)
		}
	}
	for _, want := range []string{"something is off", "it broke"} {
		if !strings.Contains(errOut.String(), want) {
			t.Errorf("stderr = %q, want %q", errOut, want)
		}
	}
}

func TestNoEscapeSequencesWhenNotATerminal(t *testing.T) {
	// A bytes.Buffer is not a character device, so colour must be off.
	p, out, errOut := newTest()

	p.Println("plain")
	p.Warnf("warned")
	p.Error(errs.New("broken").WithHint("do this"))

	if hasANSI(out.String()) || hasANSI(errOut.String()) {
		t.Errorf("escape sequences leaked into non-terminal output:\nstdout %q\nstderr %q", out, errOut)
	}
}

func TestErrorPrintsTheHint(t *testing.T) {
	p, _, errOut := newTest()

	p.Error(errs.New("docker is not running").WithHint("start Docker and try again"))

	got := errOut.String()
	for _, want := range []string{"pit:", "docker is not running", "hint:", "start Docker and try again"} {
		if !strings.Contains(got, want) {
			t.Errorf("stderr = %q, missing %q", got, want)
		}
	}
}

func TestErrorWithoutHintPrintsNoHintLine(t *testing.T) {
	p, _, errOut := newTest()

	p.Error(errors.New("plain failure"))

	if strings.Contains(errOut.String(), "hint:") {
		t.Errorf("stderr = %q, want no hint line for an error without one", errOut)
	}
}

func TestErrorIgnoresNil(t *testing.T) {
	p, out, errOut := newTest()

	p.Error(nil)

	if out.Len() != 0 || errOut.Len() != 0 {
		t.Errorf("nil error produced output:\nstdout %q\nstderr %q", out, errOut)
	}
}

func TestColorDisabledByEnvironment(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
	}{
		{"NO_COLOR is set", map[string]string{"NO_COLOR": "1"}},
		{"TERM is dumb", map[string]string{"TERM": "dumb"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			// os.Stdout is a terminal under some runners and not under
			// others; the environment must win either way.
			if colorEnabled(os.Stdout) {
				t.Error("colour is on although the environment opted out")
			}
		})
	}
}

func TestIsTerminalRejectsNonFiles(t *testing.T) {
	if isTerminal(&bytes.Buffer{}) {
		t.Error("a bytes.Buffer was reported as a terminal")
	}
}

func TestWriteFailureIsRemembered(t *testing.T) {
	p := New(failingWriter{}, &bytes.Buffer{})

	if p.Err() != nil {
		t.Fatalf("Err() = %v before any write, want nil", p.Err())
	}

	p.Println("this cannot be written")

	if p.Err() == nil {
		t.Error("Err() = nil after a failed write, want the failure")
	}
}

func TestFirstWriteFailureWins(t *testing.T) {
	p := New(failingWriter{}, &bytes.Buffer{})

	p.Println("first")
	first := p.Err()
	p.Println("second")

	if !errors.Is(p.Err(), first) {
		t.Errorf("Err() = %v, want the first failure %v", p.Err(), first)
	}
}

// failingWriter rejects every write, standing in for a closed pipe or a
// full disk.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }
