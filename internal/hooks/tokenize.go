package hooks

import (
	"strings"

	"github.com/thannoz/pit/internal/errs"
)

// tokenize splits a configured command line into arguments the way a
// shell would, honouring single quotes, double quotes and backslash
// escapes -- but it does not interpret anything else.
//
// No shell is involved on purpose. A shell would bring word splitting
// of paths that contain spaces, glob expansion at a moment nobody
// expects, and behaviour that differs between systems. Anyone who
// genuinely needs a pipe can write `sh -c "..."` and say so.
func tokenize(line string) ([]string, error) {
	var (
		args    []string
		current strings.Builder
		started bool // distinguishes "" as an argument from no argument
	)

	const (
		plain = iota
		inSingle
		inDouble
	)
	state := plain

	for i := 0; i < len(line); i++ {
		c := line[i]

		switch state {
		case inSingle:
			// Inside single quotes nothing is special, not even a
			// backslash. That is what makes them useful for SQL.
			if c == '\'' {
				state = plain
				continue
			}
			current.WriteByte(c)

		case inDouble:
			if c == '\\' && i+1 < len(line) && (line[i+1] == '"' || line[i+1] == '\\') {
				i++
				current.WriteByte(line[i])
				continue
			}
			if c == '"' {
				state = plain
				continue
			}
			current.WriteByte(c)

		default:
			switch {
			case c == '\'':
				state, started = inSingle, true
			case c == '"':
				state, started = inDouble, true
			case c == '\\' && i+1 < len(line):
				i++
				current.WriteByte(line[i])
				started = true
			case c == ' ' || c == '\t':
				if started {
					args = append(args, current.String())
					current.Reset()
					started = false
				}
			default:
				current.WriteByte(c)
				started = true
			}
		}
	}

	if state != plain {
		quote := "'"
		if state == inDouble {
			quote = `"`
		}
		return nil, errs.New("unbalanced %s in %q", quote, line).
			WithHint("close the quote, or escape it with a backslash")
	}
	if started {
		args = append(args, current.String())
	}
	if len(args) == 0 {
		return nil, errs.New("the command is empty")
	}
	return args, nil
}
