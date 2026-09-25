package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thannoz/pit/internal/data/datatest"
	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/inspect"
	"github.com/thannoz/pit/internal/notes"
	"github.com/thannoz/pit/internal/report"
	"github.com/thannoz/pit/internal/sandbox"
	"github.com/thannoz/pit/internal/state"
)

var orderSteps = []inspect.Step{
	{Action: inspect.Goto, Path: "/"},
	{Action: inspect.Fill, Selector: `input[name="item"]`, Text: "item", Value: "Genmaicha"},
	{Action: inspect.Click, Selector: "body > form > button", Text: "Order it"},
}

// withRecorder answers pit open --record with the steps given, as if a
// reviewer had taken them, and notes the address it was opened at.
func withRecorder(t *testing.T, steps []inspect.Step, err error) *[]string {
	t.Helper()
	var opened []string
	previous := recordBrowser
	recordBrowser = func(_ context.Context, address string, onStep func(inspect.Step)) ([]inspect.Step, error) {
		opened = append(opened, address)
		for _, s := range steps {
			onStep(s)
		}
		return steps, err
	}
	t.Cleanup(func() { recordBrowser = previous })
	return &opened
}

// recordingBox is a running sandbox with scenarios to load.
func recordingBox(t *testing.T, edited bool) (*sandbox.Manager, state.Sandbox, *datatest.Fake) {
	t.Helper()
	box := recordedWithConfig(t, 482, "standard", withScenarios)
	box.URL, box.SHA, box.Edited = "http://localhost:41234/", "c56680aa11", edited
	store := dataFake(t, box)
	m, err := manager()
	if err != nil {
		t.Fatal(err)
	}
	return m, box, store
}

func TestOpenRecordKeepsTheStepsForTheNextNote(t *testing.T) {
	m, box, _ := recordingBox(t, false)
	opened := withRecorder(t, orderSteps, nil)
	out, err := runCLIWithInput(t, "", "open", "482", "--record")
	if err != nil {
		t.Fatal(err)
	}
	if len(*opened) != 1 || (*opened)[0] != "http://localhost:41234/" {
		t.Errorf("opened %v", *opened)
	}
	for _, want := range []string{
		"  1. open /\n  2. type \"Genmaicha\" into \"item\"\n  3. click \"Order it\"\n",
		"Recorded 3 steps. `pit note 482 \"what you found\"` keeps them with the note.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}

	withPage(t, nil, nil)
	out, _, err = run(t, "note", "482", "A new order shows no refund line")
	if err != nil || !strings.Contains(out, "steps 3 recorded") {
		t.Fatalf("%v:\n%s", err, out)
	}
	list, _ := m.Notes(box.Repo, box.RepoRef, 482).List()
	r := list[0].Recording
	if r == nil || r.SHA != "c56680aa11" || r.Scenario != "standard" || r.Edited || len(r.Steps) != 3 || r.Steps[1].Value != "Genmaicha" {
		t.Errorf("recording = %+v", r)
	}
	// It went to that note, not the next.
	if _, _, err := run(t, "note", "482", "Another"); err != nil {
		t.Fatal(err)
	}
	if list, _ := m.Notes(box.Repo, box.RepoRef, 482).List(); list[1].Recording != nil {
		t.Error("a recording went to two notes")
	}
}

// A recording is done again from the scenario, so on data changed by
// hand pit offers to load the scenario first.
func TestOpenRecordOnChangedData(t *testing.T) {
	t.Run("loads the scenario", func(t *testing.T) {
		m, box, store := recordingBox(t, true)
		withRecorder(t, orderSteps, nil)
		out, err := runCLIWithInput(t, "\nn\n", "open", "482", "--record")
		if err != nil {
			t.Fatal(err)
		}
		if got := store.Applied(); len(got) != 1 || got[0] != "standard" {
			t.Errorf("applied %v:\n%s", got, out)
		}
		r, _ := m.Notes(box.Repo, box.RepoRef, 482).TakeRecording()
		if r == nil || r.Edited {
			t.Errorf("recording = %+v", r)
		}
	})
	t.Run("records on it anyway", func(t *testing.T) {
		m, box, store := recordingBox(t, true)
		withRecorder(t, orderSteps, nil)
		out, err := runCLIWithInput(t, "n\n", "open", "482", "--record")
		if err != nil {
			t.Fatal(err)
		}
		if got := store.Applied(); len(got) != 0 {
			t.Errorf("applied %v:\n%s", got, out)
		}
		r, _ := m.Notes(box.Repo, box.RepoRef, 482).TakeRecording()
		if r == nil || !r.Edited {
			t.Errorf("recording = %+v", r)
		}
	})
}

func TestOpenRecordOfNothing(t *testing.T) {
	m, box, _ := recordingBox(t, false)
	withRecorder(t, nil, nil)
	out, err := runCLIWithInput(t, "", "open", "482", "--record")
	if err != nil || !strings.Contains(out, "Nothing was recorded.") {
		t.Errorf("%v:\n%s", err, out)
	}
	if r, _ := m.Notes(box.Repo, box.RepoRef, 482).TakeRecording(); r != nil {
		t.Errorf("kept %+v", r)
	}
}

func TestOpenRecordNeedsARunningSandbox(t *testing.T) {
	withManager(t, recorded(482, "github.com/acme/shop", "acme-shop-c56680", "refunds", 0))
	opened := withRecorder(t, orderSteps, nil)
	_, err := runCLIWithInput(t, "", "open", "482", "--record")
	if err == nil || !strings.Contains(err.Error(), "nothing is running for #482") || len(*opened) != 0 {
		t.Errorf("err = %v, opened %v", err, *opened)
	}
}

// withReplayer answers pit replay as a browser would, and notes what it
// was asked to do.
type replayed struct {
	base  string
	steps []inspect.Step
	opts  inspect.ReplayOptions
}

func withReplayer(t *testing.T, err error) *[]replayed {
	t.Helper()
	var calls []replayed
	previous := replayBrowser
	replayBrowser = func(_ context.Context, base string, steps []inspect.Step, o inspect.ReplayOptions) (inspect.Report, error) {
		calls = append(calls, replayed{base, steps, o})
		for i, s := range steps {
			o.OnStep(i+1, s)
		}
		return inspect.Report{URL: base + "orders/1004"}, err
	}
	t.Cleanup(func() { replayBrowser = previous })
	return &calls
}

// recipeFile is a comment with the recipe of a note, saved as a file.
func recipeFile(t *testing.T, pr int, notesWithSteps ...notes.Note) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "comment.md")
	body := report.Comment(report.Input{PR: pr, Notes: notesWithSteps})
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func recordedNote(id int, sha, scenario string) notes.Note {
	return notes.Note{ID: id, Text: "A new order shows no refund line", URL: "http://localhost:43077/orders/1004", SHA: sha,
		Recording: &notes.Recording{SHA: sha, Scenario: scenario, Steps: orderSteps}}
}

// TestReplayLoadsTheScenarioAndTakesTheSteps is the command's half of
// the acceptance criterion for T-808: from a comment, the sandbox is
// brought to where the reviewer's was.
func TestReplayLoadsTheScenarioAndTakesTheSteps(t *testing.T) {
	m, box, store := recordingBox(t, false)
	calls := withReplayer(t, nil)
	file := recipeFile(t, 482, recordedNote(3, "c56680aa11", "standard"))

	out, err := runCLIWithInput(t, "y\n", "replay", "482", file, "--password", "1234")
	if err != nil {
		t.Fatal(err)
	}
	if got := store.Applied(); len(got) != 1 || got[0] != "standard" {
		t.Errorf("applied %v", got)
	}
	if len(*calls) != 1 || (*calls)[0].base != "http://localhost:41234/" || len((*calls)[0].steps) != 3 || (*calls)[0].opts.Password != "1234" {
		t.Fatalf("replayed %+v", *calls)
	}
	for _, want := range []string{
		`This loads scenario "standard" on #482 again, then takes the 3 steps recorded.`,
		"  1. open /\n  2. type \"Genmaicha\" into \"item\"\n  3. click \"Order it\"\n",
		"Replayed note 3: #482 is where the recording left it, at http://localhost:41234/orders/1004",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "recorded at") {
		t.Errorf("a recording at the same commit is said to differ:\n%s", out)
	}
	// What pit loaded is not what the reviewer looked at.
	f, _ := m.Store.Load()
	if got, _ := f.Find(box.RepoRef, box.PR); len(got.Browsed) != 1 {
		t.Errorf("Browsed = %+v", got.Browsed)
	}
}

// Loading the scenario replaces the data: without a yes, nothing is
// touched -- not when the answer is no, and not when nobody is there to
// give one.
func TestReplayAsksFirst(t *testing.T) {
	for _, tc := range []struct {
		name  string
		stdin string
	}{{"no", "n\n"}, {"enter", "\n"}, {"nobody", ""}} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, store := recordingBox(t, false)
			calls := withReplayer(t, nil)
			file := recipeFile(t, 482, recordedNote(3, "c56680aa11", "standard"))
			var out string
			var err error
			if tc.stdin == "" {
				out, err = runReplayWithoutInput(t, "482", file)
			} else {
				out, err = runCLIWithInput(t, tc.stdin, "replay", "482", file)
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(store.Applied()) != 0 || len(*calls) != 0 || !strings.Contains(out, "Nothing was changed.") {
				t.Errorf("applied %v, replayed %d:\n%s", store.Applied(), len(*calls), out)
			}
		})
	}
}

func runReplayWithoutInput(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newRootCmd()
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(devNull(t))
	cmd.SetArgs(append([]string{"replay"}, args...))
	cmd.SetContext(t.Context())
	err := postProcess(cmd.Execute())
	return out.String(), err
}

func TestReplayWithYesAsksNothing(t *testing.T) {
	_, _, store := recordingBox(t, true)
	calls := withReplayer(t, nil)
	file := recipeFile(t, 482, recordedNote(3, "c56680aa11", "standard"))
	// Someone is there to ask; --yes said not to.
	out, err := runCLIWithInput(t, "", "replay", "482", file, "--yes")
	if err != nil {
		t.Fatal(err)
	}
	if len(store.Applied()) != 1 || len(*calls) != 1 || strings.Contains(out, "[Y/n]") || strings.Contains(out, "[y/N]") {
		t.Errorf("applied %v, replayed %d:\n%s", store.Applied(), len(*calls), out)
	}
}

func TestReplayFromStandardInputPicksTheNote(t *testing.T) {
	_, _, _ = recordingBox(t, false)
	calls := withReplayer(t, nil)
	body := report.Comment(report.Input{PR: 482, Notes: []notes.Note{recordedNote(1, "c56680aa11", "standard"), recordedNote(4, "c56680aa11", "standard")}})

	_, err := runCLIWithInput(t, body, "replay", "482", "-", "--yes")
	if err == nil || !strings.Contains(err.Error(), "has a recipe for more than one note") || !strings.Contains(errs.Hint(err), "notes 1, 4; --note picks one") {
		t.Errorf("err = %v", err)
	}
	if _, err := runCLIWithInput(t, body, "replay", "482", "-", "--yes", "--note", "2"); err == nil || !strings.Contains(err.Error(), "there is no recipe for note 2") {
		t.Errorf("err = %v", err)
	}
	if _, err := runCLIWithInput(t, body, "replay", "482", "-", "--yes", "--note", "4"); err != nil || len(*calls) != 1 {
		t.Errorf("err = %v, replayed %d", err, len(*calls))
	}
}

func TestReplayOfAnotherPullRequestsRecipe(t *testing.T) {
	recordingBox(t, false)
	calls := withReplayer(t, nil)
	file := recipeFile(t, 7, recordedNote(3, "c56680aa11", "standard"))
	_, err := runCLIWithInput(t, "y\n", "replay", "482", file)
	if err == nil || !strings.Contains(err.Error(), "the recipe is for #7, not #482") || len(*calls) != 0 {
		t.Errorf("err = %v, replayed %d", err, len(*calls))
	}
}

func TestReplayAtAnotherCommit(t *testing.T) {
	recordingBox(t, false)
	withReplayer(t, nil)
	file := recipeFile(t, 482, recordedNote(3, "9a8b7c6d5e", "standard"))
	out, err := runCLIWithInput(t, "y\n", "replay", "482", file)
	if err != nil || !strings.Contains(out, "recorded at 9a8b7c6; #482 is at c56680a now, so the steps may not fit") {
		t.Errorf("%v:\n%s", err, out)
	}
}

// A recording that did not start from a scenario is replayed on the
// data as it is, with nothing loaded and nothing asked.
func TestReplayWithoutAScenario(t *testing.T) {
	_, _, store := recordingBox(t, false)
	calls := withReplayer(t, nil)
	file := recipeFile(t, 482, recordedNote(3, "c56680aa11", ""))
	out, err := runReplayWithoutInput(t, "482", file)
	if err != nil || len(store.Applied()) != 0 || len(*calls) != 1 || !strings.Contains(out, "the recording did not start from a scenario") {
		t.Errorf("%v, applied %v, replayed %d:\n%s", err, store.Applied(), len(*calls), out)
	}
}

func TestReplayThatFails(t *testing.T) {
	recordingBox(t, false)
	withReplayer(t, errors.New(`step 3, click "Order it": nothing on / is "Order it"`))
	file := recipeFile(t, 482, recordedNote(3, "c56680aa11", "standard"))
	if _, err := runCLIWithInput(t, "y\n", "replay", "482", file); err == nil || !strings.Contains(err.Error(), "step 3") {
		t.Errorf("err = %v", err)
	}
}

func TestReplayJSON(t *testing.T) {
	recordingBox(t, false)
	withReplayer(t, nil)
	file := recipeFile(t, 482, recordedNote(3, "c56680aa11", "standard"))
	out, err := runReplayWithoutInput(t, "482", file, "--yes", "--json")
	var r struct {
		Note   int            `json:"note"`
		Steps  int            `json:"steps"`
		Report inspect.Report `json:"report"`
	}
	if err != nil || json.Unmarshal([]byte(lastJSON(out)), &r) != nil || r.Note != 3 || r.Steps != 3 {
		t.Errorf("%v:\n%s", err, out)
	}
}

// lastJSON is the JSON object at the end of output that has other
// lines before it.
func lastJSON(out string) string {
	if i := strings.Index(out, "{\n"); i >= 0 {
		return out[i:]
	}
	return out
}
