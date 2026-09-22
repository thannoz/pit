package data

import (
	"context"
	"fmt"
	"io"

	"github.com/thannoz/pit/internal/hooks"
)

// Sandbox is the running sandbox a data state is applied to.
//
// It repeats the three fields internal/runtime and internal/hooks also
// carry rather than importing either, so that a store which is not
// backed by Compose stays possible.
type Sandbox struct {
	// Project is the Compose project name pit assigned.
	Project string
	// Files are the compose files, in merge order.
	Files []string
	// Dir is the worktree the commands run in.
	Dir string
}

// Scenario is a named data state, resolved to the commands that
// produce it.
type Scenario struct {
	// Name is what a reviewer passes to --scenario.
	Name string
	// Description is what the name means, for listings.
	Description string
	// Apply are the commands that produce the state, in the order they
	// have to run.
	Apply []string
}

// Empty reports whether the scenario asks for nothing.
func (s Scenario) Empty() bool { return len(s.Apply) == 0 }

// Store puts a sandbox into a known data state.
//
// Only Apply is declared. The sketch in the architecture document also
// had Save, Restore and List; those belong to snapshots, which are a
// later stage. Declaring them now would mean either methods that fail
// when called or a fake that pretends to do something no caller asks
// for. Go's implicit interfaces make adding them later free.
type Store interface {
	// Apply produces the scenario's state inside the sandbox. The
	// writers are there because loading a fixture can take as long as
	// a build, and a reviewer watching a blank screen cannot tell a
	// slow import from a hung one.
	Apply(ctx context.Context, s Sandbox, sc Scenario, stdout, stderr io.Writer) error
}

// Commands is the Store every repository gets: the scenario's own
// commands, run inside the sandbox.
//
// pit deliberately knows nothing about databases or fixture formats. A
// scenario is a list of commands, because that is the only thing every
// project already has.
type Commands struct {
	// Runner runs them. internal/proc.Exec satisfies it.
	Runner hooks.Runner
}

var _ Store = Commands{}

// Apply runs the scenario's commands in order.
func (c Commands) Apply(ctx context.Context, s Sandbox, sc Scenario, stdout, stderr io.Writer) error {
	if sc.Empty() {
		return nil
	}

	list := hooks.List{
		Path:  fmt.Sprintf("data.scenarios[%q].apply", sc.Name),
		Lines: sc.Apply,
	}
	box := hooks.Sandbox{Project: s.Project, Files: s.Files, Dir: s.Dir}

	return hooks.Run(ctx, c.Runner, list, box, stdout, stderr)
}
