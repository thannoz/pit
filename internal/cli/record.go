package cli

import (
	"context"
	"time"

	"github.com/spf13/cobra"

	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/inspect"
	"github.com/thannoz/pit/internal/notes"
	"github.com/thannoz/pit/internal/sandbox"
	"github.com/thannoz/pit/internal/state"
	"github.com/thannoz/pit/internal/ui"
)

// gifLength is how much of the end of a recording its GIF shows: the
// seconds before the reviewer stopped, where what they found is.
const gifLength = 10 * time.Second

// recordBrowser opens a browser the reviewer uses and writes down what
// they do; a variable so that tests need no window.
var recordBrowser = func(ctx context.Context, address string, onStep func(inspect.Step)) (inspect.Recorded, error) {
	return inspect.Record(ctx, address, inspect.RecordOptions{OnStep: onStep, GIF: gifLength})
}

// recordSandbox is pit open --record: a browser pit watches, and the
// steps taken in it kept for the next note.
func recordSandbox(c *cobra.Command, out *ui.Printer, box state.Sandbox) error {
	m, err := manager()
	if err != nil {
		return err
	}
	if err := needsRunning(c, m, box); err != nil {
		return err
	}

	// Steps are done again from the scenario. Steps taken on data that
	// was changed before lead somewhere the scenario and the steps do
	// not.
	edited := m.EditedNow(c.Context(), box) == sandbox.Edited
	if edited && box.Scenario != "" {
		out.Printf("The data of #%d was changed since scenario %q was loaded. A recording is done again from the scenario,\nso it is best made from there too.\n", box.PR, box.Scenario)
		if askYes(c, out, "Load the scenario again first?") {
			if err := reloadScenario(c, m, out, box, box.Scenario, true); err != nil {
				return err
			}
			edited = false
		}
	}

	out.Printf("Recording in the browser that opened. Do what leads to what you found; close the browser to stop.\n")
	start := time.Now()
	n := 0
	recorded, err := recordBrowser(c.Context(), box.URL, func(s inspect.Step) {
		n++
		out.Printf("  %d. %s\n", n, s)
	})
	if err != nil {
		return err
	}
	steps := recorded.Steps
	if len(steps) == 0 {
		out.Printf("Nothing was recorded.\n")
		return out.Err()
	}
	err = m.Notes(box.Repo, box.RepoRef, box.PR).KeepRecording(notes.Recording{
		SHA: box.SHA, Scenario: box.Scenario, Edited: edited, At: start, Steps: steps,
	}, recorded.GIF)
	if err != nil {
		return err
	}
	if recorded.GIFError != nil {
		out.Warnf("no GIF of the last seconds: %v", recorded.GIFError)
	}
	out.Printf("Recorded %s. `pit note %d \"what you found\"` keeps them with the note.\n", plural(len(steps), "step", "steps"), box.PR)
	return out.Err()
}

// needsRunning refuses a sandbox that has nothing running to load a
// page from.
func needsRunning(c *cobra.Command, m *sandbox.Manager, box state.Sandbox) error {
	entry, err := m.FindBox(c.Context(), box)
	if err != nil {
		return err
	}
	if !entry.AnyRunning() {
		return errs.New("nothing is running for #%d", box.PR).
			WithHint("`pit %d` brings the sandbox up again", box.PR)
	}
	return nil
}

// reloadScenario loads a scenario again, and offers -- when offer says
// to ask at all -- to save data changed by hand before it is replaced.
func reloadScenario(c *cobra.Command, m *sandbox.Manager, out *ui.Printer, box state.Sandbox, name string, offer bool) error {
	scenario, err := scenarioFor(box, name)
	if err != nil {
		return err
	}
	if m.EditedNow(c.Context(), box) == sandbox.Edited {
		if err := saveBeforeReplacing(c, m, out, box, offer); err != nil {
			return err
		}
	}
	return m.ResetData(c.Context(), box, scenario, newStepReporter(out, c.ErrOrStderr()))
}
