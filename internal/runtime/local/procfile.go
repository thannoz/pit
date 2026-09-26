// Package local runs a sandbox's services as processes on this machine,
// from a Procfile, where a project has no containers to run them in.
//
// Nothing isolates them but a port each and a directory: they run as
// the reviewer, with the reviewer's files in reach. What keeps them
// apart from each other is what keeps two sandboxes of a compose
// project apart -- a name and a port of their own -- and nothing more.
package local

import (
	"bufio"
	"bytes"
	"os"
	"regexp"
	"strings"

	"github.com/thannoz/pit/internal/errs"
)

// Process is one line of a Procfile: a name, and the command that runs
// it, for a shell.
type Process struct {
	Name    string `json:"name"`
	Command string `json:"command"`
}

// procLine is a Procfile's line: the name is what Heroku and foreman
// accept.
var procLine = regexp.MustCompile(`^([A-Za-z0-9_-]+):\s*(.*)$`)

// ParseProcfile reads a Procfile. Blank lines and comments are left
// out; a line that is neither, nor a process, is an error: a typo would
// otherwise be a process that silently never starts.
func ParseProcfile(data []byte) ([]Process, error) {
	var out []Process
	seen := map[string]bool{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	n := 0
	for sc.Scan() {
		n++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		m := procLine.FindStringSubmatch(line)
		if m == nil {
			return nil, errs.New("line %d is not a process: %q", n, line).
				WithHint("a Procfile's lines read `name: command`")
		}
		name, cmd := m[1], strings.TrimSpace(m[2])
		if cmd == "" {
			return nil, errs.New("line %d names %s, but no command", n, name)
		}
		if seen[name] {
			return nil, errs.New("line %d names %s a second time", n, name)
		}
		seen[name] = true
		out = append(out, Process{Name: name, Command: cmd})
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, errs.New("names no processes")
	}
	return out, nil
}

// ReadProcfile reads the Procfile at path.
func ReadProcfile(path string) ([]Process, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, errs.Wrap(err, "cannot read %s", path)
	}
	ps, err := ParseProcfile(data)
	if err != nil {
		return nil, errs.Wrap(err, "%s", path)
	}
	return ps, nil
}
