package data

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/thannoz/pit/internal/config"
	"github.com/thannoz/pit/internal/errs"
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
	// Steps are the scenarios it is made of, base first. A scenario
	// without extends has exactly one.
	Steps []Step
	// Migrate are the migrations, run again after a snapshot is
	// loaded: it holds the schema of the commit it was saved at, and
	// the code under review expects its own.
	Migrate []string
}

// Step is one stage of an extends chain.
//
// The commands keep the name they were written under, which is not
// necessarily the scenario that was asked for: when a base fixture
// fails, the author has to be sent to the base, not to the scenario
// that happens to build on it.
type Step struct {
	// Scenario is the name the commands are configured under.
	Scenario string
	// Restores are the dumps it loads before its commands run, for a
	// scenario that was a snapshot once.
	Restores []config.Restore
	// Apply are its commands, in the author's order.
	Apply []string
}

// Empty reports whether the scenario asks for nothing. A scenario that
// only declares a description is a legitimate choice -- "leer" in the
// data concept is exactly that.
func (s Scenario) Empty() bool {
	for _, step := range s.Steps {
		if len(step.Apply) > 0 || len(step.Restores) > 0 {
			return false
		}
	}
	return true
}

// Commands returns every command the scenario runs, in order, without
// their origin. It is for callers that only want to show them.
func (s Scenario) Commands() []string {
	var out []string
	for _, step := range s.Steps {
		for _, r := range step.Restores {
			out = append(out, r.Command+" < "+r.File)
		}
		out = append(out, step.Apply...)
	}
	return out
}

// Describe names the scenario, and the chain it came from when there
// is one. A reviewer who asked for "teilerstattung" and gets the data
// of "standard" as well should be able to see that.
func (s Scenario) Describe() string {
	if len(s.Steps) < 2 {
		return s.Name
	}

	names := make([]string, 0, len(s.Steps))
	for _, step := range s.Steps {
		names = append(names, step.Scenario)
	}
	return s.Name + " (" + strings.Join(names, " → ") + ")"
}

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

// Apply runs the scenario's commands, base first.
//
// Each step is run as its own list so that a failure names the
// scenario the command is written under rather than the one that was
// asked for.
func (c Commands) Apply(ctx context.Context, s Sandbox, sc Scenario, stdout, stderr io.Writer) error {
	box := hooks.Sandbox{Project: s.Project, Files: s.Files, Dir: s.Dir}

	for _, step := range sc.Steps {
		if len(step.Restores) > 0 {
			if err := c.restore(ctx, box, step, sc.Migrate, stdout, stderr); err != nil {
				return err
			}
		}
		if len(step.Apply) == 0 {
			continue
		}

		list := hooks.List{
			Path:  fmt.Sprintf("data.scenarios[%q].apply", step.Scenario),
			Lines: step.Apply,
		}
		if err := hooks.Run(ctx, c.Runner, list, box, stdout, stderr); err != nil {
			return err
		}
	}
	return nil
}

// restore loads a step's dumps, each into its restore command's stdin,
// and runs the migrations after them.
func (c Commands) restore(ctx context.Context, box hooks.Sandbox, step Step, migrate []string, stdout, stderr io.Writer) error {
	for _, r := range step.Restores {
		if err := c.load(ctx, box, step.Scenario, r, stdout, stderr); err != nil {
			return err
		}
	}
	return hooks.Run(ctx, c.Runner, hooks.Migrations(migrate), box, stdout, stderr)
}

func (c Commands) load(ctx context.Context, box hooks.Sandbox, scenario string, r config.Restore, stdout, stderr io.Writer) error {
	name := filepath.Base(r.File)
	f, err := r.Open()
	if err != nil {
		return errs.Wrap(err, "scenario %q loads %s, which cannot be read", scenario, name).
			WithHint("it is named under data.scenarios[%q].snapshot, relative to .pit.yaml", scenario)
	}
	defer func() { _ = f.Close() }()

	cmd, err := hooks.Expand(r.Command, box)
	if err != nil {
		return errs.Wrap(err, "cannot run data.snapshot's restore")
	}
	cmd.Stdin = f
	if err := c.Runner.Stream(ctx, cmd, stdout, stderr); err != nil {
		return errs.Wrap(err, "loading %s for scenario %q failed", name, scenario).
			WithHint("it goes through data.snapshot's restore command; `pit logs` shows what the database said")
	}
	return nil
}
