// Package hooks turns the commands a repository configures into
// processes pit can run. Its one piece of cleverness is the `compose`
// shorthand, which spares the author from repeating the isolation flags
// that pit chose.
package hooks

import (
	"github.com/thannoz/pit/internal/proc"
)

// Shorthand is the word a configured line starts with to mean "run this
// through the sandbox's own docker compose".
const Shorthand = "compose"

// Sandbox is what the shorthand expands against.
type Sandbox struct {
	// Project is the Compose project name pit assigned.
	Project string
	// Files are the compose files, in merge order.
	Files []string
	// Dir is the worktree the command runs in.
	Dir string
}

// Expand turns one configured line into a command.
//
// A line beginning with "compose" is rewritten into a full docker
// compose invocation carrying the project name and files. Anything else
// is run as written. The point is that the author of .pit.yaml does not
// have to know how pit isolates a sandbox in order to run a migration
// inside it.
func Expand(line string, s Sandbox) (proc.Command, error) {
	args, err := tokenize(line)
	if err != nil {
		return proc.Command{}, err
	}

	if args[0] != Shorthand {
		return proc.Command{Name: args[0], Args: args[1:], Dir: s.Dir}, nil
	}

	full := []string{"compose", "--project-name", s.Project}
	for _, f := range s.Files {
		full = append(full, "--file", f)
	}
	full = append(full, args[1:]...)

	return proc.Command{Name: "docker", Args: full, Dir: s.Dir}, nil
}

// Check reports whether a configured line can be run at all.
//
// It exists so that a broken command is caught when the file is read
// rather than halfway through a setup, with a worktree already made
// and containers already started.
func Check(line string) error {
	_, err := tokenize(line)
	return err
}

// ExpandAll expands every line, reporting which one failed rather than
// only that something did.
func ExpandAll(lines []string, s Sandbox) ([]proc.Command, error) {
	cmds := make([]proc.Command, 0, len(lines))
	for i, line := range lines {
		c, err := Expand(line, s)
		if err != nil {
			return nil, wrapLine(err, i, line)
		}
		cmds = append(cmds, c)
	}
	return cmds, nil
}
