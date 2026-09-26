package kube

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"

	"github.com/thannoz/pit/internal/proc"
)

// fake answers commands by the longest prefix of their words it knows,
// and notes each command with what it was given on stdin.
type fake struct {
	mu      sync.Mutex
	answers map[string]answer
	calls   []call
}

type answer struct {
	out string
	err error
}

type call struct {
	line  string
	stdin string
}

func newFake(answers map[string]answer) *fake {
	if answers == nil {
		answers = map[string]answer{}
	}
	return &fake{answers: answers}
}

func (f *fake) answer(c proc.Command) (string, error) {
	line := strings.Join(append([]string{c.Name}, c.Args...), " ")
	in := ""
	if c.Stdin != nil {
		data, _ := io.ReadAll(c.Stdin)
		in = string(data)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call{line: line, stdin: in})
	best, found := "", false
	for prefix := range f.answers {
		if strings.HasPrefix(line, prefix) && len(prefix) >= len(best) {
			best, found = prefix, true
		}
	}
	if !found {
		return "", errors.New("exec: " + c.Name + ": no answer for " + line)
	}
	a := f.answers[best]
	return a.out, a.err
}

func (f *fake) Output(_ context.Context, c proc.Command) ([]byte, error) {
	out, err := f.answer(c)
	return []byte(out), err
}

func (f *fake) Stream(_ context.Context, c proc.Command, stdout, _ io.Writer) error {
	out, err := f.answer(c)
	_, _ = io.WriteString(stdout, out)
	return err
}

// lines are the commands, in order.
func (f *fake) lines() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.calls))
	for _, c := range f.calls {
		out = append(out, c.line)
	}
	return out
}

// find is the first call that starts with prefix.
func (f *fake) find(prefix string) (call, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if strings.HasPrefix(c.line, prefix) {
			return c, true
		}
	}
	return call{}, false
}
