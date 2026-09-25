package sandbox

import (
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/thannoz/pit/internal/config"
	"github.com/thannoz/pit/internal/data"
	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/state"
)

// adopt reads the configuration the pull request brings with it.
//
// The pull request's file governs, because the pull request is what is
// under review. Everything else pit does already comes from it: its
// compose file decides which services exist, its Dockerfiles are
// built, its code runs. Taking the setup from the reviewer's branch
// instead means a pull request cannot add a service, move a port or
// add a scenario without the reviewer checking the branch out first --
// which is the manual work pit exists to remove.
//
// What that does add is commands. A line in .pit.yaml that does not go
// through the `compose` shorthand runs on the machine, as the
// reviewer. Those are asked about; nothing else is.
func (m *Manager) adopt(req UpRequest, worktree string, rep Reporter) (*config.Config, error) {
	path := filepath.Join(worktree, config.FileName)
	if _, err := os.Stat(path); err != nil {
		// The branch does not carry one -- it may have removed it. The
		// reviewer's stays, and saying so costs one line.
		rep.Note("#%d has no %s of its own; using yours", req.PR.Number, config.FileName)
		return req.Config, nil
	}

	theirs, err := config.Load(path)
	if err != nil {
		return nil, errs.Wrap(err, "#%d brings a %s that pit cannot use", req.PR.Number, config.FileName).
			WithHint("the file in the pull request is the one being read, not yours")
	}

	changed := config.Differences(req.Config, theirs)
	if len(changed) == 0 {
		return theirs, nil
	}
	rep.Note("using #%d's own %s; it changes %s", req.PR.Number, config.FileName, list(changed))

	// The one question worth asking: a command that runs outside the
	// sandbox is not covered by the decision to review this branch at
	// all.
	fresh := newHostCommands(req.Config, theirs)
	if len(fresh) == 0 {
		return theirs, nil
	}

	rep.Note("it also runs %s on this machine, outside the sandbox:", plural(len(fresh), "command", "commands"))
	for _, cmd := range fresh {
		rep.Note("    %s", cmd)
	}

	if req.Confirm == nil {
		return nil, errs.New("#%d wants to run %s on this machine", req.PR.Number, plural(len(fresh), "command", "commands")).
			WithHint("run pit yourself and answer the question, or take the %s out of the pull request's %s",
				pick(len(fresh), "command", "commands"), config.FileName)
	}
	if !req.Confirm("Run " + pick(len(fresh), "it", "them") + "?") {
		// The same answer covers a reviewer who said no and a run with
		// nobody to ask: nothing happened, and here is how to proceed
		// if it should.
		return nil, errs.New("stopped before running #%d's commands", req.PR.Number).
			WithHint("nothing was built; run pit from a terminal to answer, or take the %s out of the pull request's %s",
				pick(len(fresh), "command", "commands"), config.FileName)
	}
	return theirs, nil
}

// newHostCommands are the commands in theirs that would run outside a
// container and are not already in mine.
//
// Already in mine means the reviewer has them checked in and lives
// with them; the pull request is not asking for anything new.
func newHostCommands(mine, theirs *config.Config) []string {
	known := mine.HostCommands()

	var out []string
	for _, cmd := range theirs.HostCommands() {
		if !slices.Contains(known, cmd) {
			out = append(out, cmd)
		}
	}
	return out
}

// list joins names the way a sentence needs them.
func list(names []string) string {
	switch len(names) {
	case 1:
		return names[0]
	case 2:
		return names[0] + " and " + names[1]
	default:
		return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
	}
}

// pick chooses between a singular and a plural word, without the count
// that plural puts in front.
func pick(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// selectData picks what a sandbox's data is loaded from: the snapshot
// asked for, checked against the restore commands of the configuration
// that governs, or else a scenario.
func (m *Manager) selectData(mine *config.Config, req UpRequest, worktree string, rep Reporter) (data.Scenario, error) {
	if req.Snapshot == nil {
		return selectScenario(mine, req.Config, req.Scenario, req.PR.Number, rep)
	}
	box := state.Sandbox{PR: req.PR.Number, RepoRoot: req.Repo.Root, Worktree: worktree}
	if err := CheckSnapshot(box, *req.Snapshot); err != nil {
		return data.Scenario{}, errs.Wrap(err, "#%d cannot load %s", req.PR.Number, req.Snapshot.Label())
	}
	return data.Scenario{}, nil
}

// selectScenario picks the scenario to load from the configuration that
// governs the sandbox, and from the reviewer's where that one does not
// have it.
//
// The pull request's file governs, but a scenario it lacks is not one
// it changed: it is usually one the reviewer added since the branch was
// cut -- a snapshot promoted a minute ago, which is no use if it can
// only be loaded once every open pull request has been rebased.
func selectScenario(mine, governing *config.Config, requested string, pr int, rep Reporter) (data.Scenario, error) {
	sc, err := data.Select(governing, requested)
	if err == nil || requested == "" || mine == nil || mine == governing {
		return sc, err
	}
	if _, ok := governing.Scenario(requested); ok {
		return sc, err
	}
	if _, ok := mine.Scenario(requested); !ok {
		return sc, err
	}
	sc, err = data.Select(mine, requested)
	if err != nil {
		return data.Scenario{}, err
	}
	rep.Note("#%d's %s has no scenario %q; loading it from yours", pr, config.FileName, requested)
	return sc, nil
}
