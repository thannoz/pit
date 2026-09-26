// Package hooks turns the commands a repository configures into
// processes pit can run. Its one piece of cleverness is the `compose`
// shorthand, which spares the author from repeating the isolation flags
// that pit chose.
package hooks

import (
	"strings"

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
	// Env is added to the environment of every command: a sandbox of
	// processes has no containers to keep its settings, so the
	// commands are given them.
	Env []string
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
		return proc.Command{Name: args[0], Args: args[1:], Dir: s.Dir, Env: s.Env}, nil
	}

	full := []string{"compose", "--project-name", s.Project}
	for _, f := range s.Files {
		full = append(full, "--file", f)
	}
	full = append(full, args[1:]...)

	return proc.Command{Name: "docker", Args: full, Dir: s.Dir, Env: s.Env}, nil
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

// List is a sequence of configured commands together with the setting
// they were written under.
//
// The two travel together because a failure has to name the place to
// fix. "hook 2" was enough while hooks were the only caller; now that
// the same machinery also runs a scenario's fixtures, the message has
// to say which of the two the author should look at.
type List struct {
	// Path is the setting in .pit.yaml the lines came from, such as
	// "hooks.after_up" or `data.scenarios["standard"].apply`.
	Path string
	// Lines are the commands, in the author's order.
	Lines []string
}

// AfterUp is the list of hooks configured under hooks.after_up. The
// path lives here rather than at the call site so that renaming the
// setting is one edit.
func AfterUp(lines []string) List {
	return List{Path: "hooks.after_up", Lines: lines}
}

// Migrations is the list configured under data.migrate.
func Migrations(lines []string) List {
	return List{Path: "data.migrate", Lines: lines}
}

// Empty reports whether there is nothing to run.
func (l List) Empty() bool { return len(l.Lines) == 0 }

// ExpandAll expands every line, reporting which one failed rather than
// only that something did.
func ExpandAll(l List, s Sandbox) ([]proc.Command, error) {
	cmds := make([]proc.Command, 0, len(l.Lines))
	for i, line := range l.Lines {
		c, err := Expand(line, s)
		if err != nil {
			return nil, wrapLine(err, l, i)
		}
		cmds = append(cmds, c)
	}
	return cmds, nil
}

// execFlags are the options of `docker compose exec` that take a value,
// in their short and long spellings.
var execFlags = map[string]bool{
	"-u": true, "--user": true, "-w": true, "--workdir": true,
	"-e": true, "--env": true, "--index": true,
}

// ExecService reads the service a `compose exec` line runs in:
// "compose exec -T -u app db pg_dump" is db. Any other line, and one
// that cannot be read, has none.
func ExecService(line string) (string, bool) {
	args, err := tokenize(line)
	if err != nil || len(args) < 3 || args[0] != Shorthand || args[1] != "exec" {
		return "", false
	}
	for i := 2; i < len(args); i++ {
		a := args[i]
		switch {
		case execFlags[a]:
			i++ // its value
		case strings.HasPrefix(a, "-"):
			// -T, --detach, --privileged, --user=app: nothing follows.
		default:
			return a, true
		}
	}
	return "", false
}
