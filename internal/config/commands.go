package config

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/thannoz/pit/internal/hooks"
)

// command is one configured command line, together with where it is
// written and how to find its line in the file.
type command struct {
	// Path is the setting, as a person reads it.
	Path string
	// Line is the command itself.
	Line string
	// at finds the line number in the document, which differs per
	// setting: some are scalars, some are entries in a list.
	at func(*yaml.Node) int
}

// commands lists every command the configuration can cause to run.
//
// One list rather than several: the validation checks these, and the
// question of which of them would run outside a container is asked of
// the same set. Two lists would drift, and the one that drifted would
// be the one nobody was looking at.
func (c *Config) commands() []command {
	var out []command

	for i, line := range c.Hooks.AfterUp {
		out = append(out, command{
			Path: fmt.Sprintf("hooks.after_up[%d]", i),
			Line: line,
			at:   func(n *yaml.Node) int { return lineOfIndex(n, i, "hooks", "after_up") },
		})
	}

	for i, line := range c.Data.Migrate {
		out = append(out, command{
			Path: fmt.Sprintf("data.migrate[%d]", i),
			Line: line,
			at:   func(n *yaml.Node) int { return lineOfIndex(n, i, "data", "migrate") },
		})
	}

	for si, s := range c.Data.Scenarios {
		for i, line := range s.Apply {
			out = append(out, command{
				Path: fmt.Sprintf("data.scenarios[%d].apply[%d]", si, i),
				Line: line,
				// The scenario's own line: a list inside a list has no
				// position of its own in the document.
				at: func(n *yaml.Node) int { return lineOfIndex(n, si, "data", "scenarios") },
			})
		}
	}

	for i, part := range c.Data.Snapshot.Parts {
		for _, cmd := range []struct{ name, line string }{{"save", part.Save}, {"restore", part.Restore}, {"writes", part.Writes}} {
			if cmd.line == "" {
				continue
			}
			out = append(out, command{
				Path: fmt.Sprintf("data.snapshot[%d].%s", i, cmd.name),
				Line: cmd.line,
				at:   func(n *yaml.Node) int { return lineOfIndex(n, i, "data", "snapshot") },
			})
		}
	}

	for _, s := range []struct{ path, line string }{
		{"data.snapshot.save", c.Data.Snapshot.Save},
		{"data.snapshot.restore", c.Data.Snapshot.Restore},
		{"data.snapshot.writes", c.Data.Snapshot.Writes},
		{"data.production_like.fetch", c.Data.ProductionLike.Fetch},
	} {
		if s.line == "" {
			continue
		}
		out = append(out, command{
			Path: s.path,
			Line: s.line,
			at:   func(n *yaml.Node) int { return lineOf(n, strings.Split(s.path, ".")...) },
		})
	}
	return out
}

// HostCommands are the configured commands that would run on the
// machine itself rather than inside one of the sandbox's containers.
//
// The distinction is the one that matters when a pull request brings
// its own configuration. Everything pit does with a pull request
// already runs its code: its Dockerfiles are built, its services are
// started, its compose file decides what they may touch. A command
// that goes through the `compose` shorthand lands in that same box. A
// command without it does not -- it runs as the reviewer, with the
// reviewer's files and the reviewer's keys.
func (c *Config) HostCommands() []string {
	var out []string
	for _, cmd := range c.commands() {
		if onHost(cmd.Line) {
			out = append(out, cmd.Line)
		}
	}
	return out
}

// onHost reports whether a configured line runs outside the sandbox.
func onHost(line string) bool {
	fields := strings.Fields(line)
	return len(fields) > 0 && fields[0] != hooks.Shorthand
}
