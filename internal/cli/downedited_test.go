package cli

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/thannoz/pit/internal/proc"
	"github.com/thannoz/pit/internal/runtime/runtimetest"
	"github.com/thannoz/pit/internal/sandbox"
	"github.com/thannoz/pit/internal/state"
)

// holdingBack is a database like PostgreSQL: it counts what the
// application wrote only once the application's connections close,
// which is to say once the web service is stopped.
type holdingBack struct {
	mu       sync.Mutex
	fake     *runtimetest.Fake
	project  string
	before   string // the count while the application runs
	after    string // the count once it is stopped
	saveFail bool
	ran      []string
}

func (h *holdingBack) Stream(_ context.Context, c proc.Command, stdout, _ io.Writer) error {
	line := strings.Join(c.Args, " ")
	h.mu.Lock()
	h.ran = append(h.ran, line)
	h.mu.Unlock()
	switch {
	case strings.Contains(line, "SELECT 1"):
		count := h.before
		if slices.Contains(h.fake.Stopped(h.project), "web") {
			count = h.after
		}
		_, err := io.WriteString(stdout, count+"\n")
		return err
	case strings.Contains(line, "pg_dump"):
		if h.saveFail {
			return errors.New("exit status 1")
		}
		_, err := io.WriteString(stdout, "-- dump\n")
		return err
	}
	return nil
}

// noGit answers every git command with nothing, for a sandbox whose
// worktree is only a directory.
type noGit struct{}

func (noGit) Output(context.Context, proc.Command) ([]byte, error) { return nil, nil }

func downFixture(t *testing.T, before, after string) (*sandbox.Manager, *runtimetest.Fake, *holdingBack, state.Sandbox) {
	t.Helper()
	box := snapBox(t, withWrites, "")
	box.Writes = []int64{5}
	m, fake := withManager(t, box)
	fake.Declared = []string{"web", "db"}
	if err := fake.Up(t.Context(), sandbox.RuntimeSandbox(box), nil, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	h := &holdingBack{fake: fake, project: box.Project, before: before, after: after}
	m.Proc, m.Git = h, noGit{}
	return m, fake, h, box
}

func snapshots(t *testing.T, m *sandbox.Manager, box state.Sandbox) int {
	t.Helper()
	list, err := m.Snapshots(box).List()
	if err != nil {
		t.Fatal(err)
	}
	return len(list)
}

func gone(t *testing.T, m *sandbox.Manager, box state.Sandbox) bool {
	t.Helper()
	f, err := m.Store.Load()
	if err != nil {
		t.Fatal(err)
	}
	_, ok := f.Find(box.RepoRef, box.PR)
	return !ok
}

// TestDownOffersToSaveWhatWasEntered is the acceptance criterion for
// T-708, and its hardest case: the order was entered a moment ago, and
// the database does not count it while the application's connection
// is open. pit stops the application first, sees it, and offers to
// keep it.
func TestDownOffersToSaveWhatWasEntered(t *testing.T) {
	m, fake, _, box := downFixture(t, "5", "6")

	out, err := runCLIWithInput(t, "\n", "down", "482")
	if err != nil {
		t.Fatalf("down: %v\n%s", err, out)
	}
	for _, want := range []string{"#482's data was changed since it was loaded", "Save it as a snapshot first? [Y/n]", "Saved it as sn_", "Removed"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if snapshots(t, m, box) != 1 || !gone(t, m, box) {
		t.Errorf("snapshots %d, removed %v", snapshots(t, m, box), gone(t, m, box))
	}
	// The application, not the database.
	if stopped := fake.Stopped(box.Project); !slices.Equal(stopped, []string{"web"}) {
		t.Errorf("stopped %v", stopped)
	}
}

func TestDownWhenTheAnswerIsNo(t *testing.T) {
	m, _, _, box := downFixture(t, "6", "6")
	out, err := runCLIWithInput(t, "n\n", "down", "482")
	if err != nil {
		t.Fatal(err)
	}
	if snapshots(t, m, box) != 0 || !gone(t, m, box) || !strings.Contains(out, "Removed") {
		t.Errorf("snapshots %d, removed %v:\n%s", snapshots(t, m, box), gone(t, m, box), out)
	}
}

// Data that is what was loaded can be loaded again: nothing to ask.
func TestDownOfUnchangedDataAsksNothing(t *testing.T) {
	m, _, _, box := downFixture(t, "5", "5")
	out, err := runCLIWithInput(t, "", "down", "482")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "changed") || strings.Contains(out, "[Y/n]") || snapshots(t, m, box) != 0 || !gone(t, m, box) {
		t.Errorf("snapshots %d:\n%s", snapshots(t, m, box), out)
	}
}

// So is data pit cannot count: it does not claim anything.
func TestDownOfDataPitCannotCount(t *testing.T) {
	m, _, _, box := downFixture(t, "5", "0") // restarted
	out, err := runCLIWithInput(t, "", "down", "482")
	if err != nil || strings.Contains(out, "changed") || !gone(t, m, box) {
		t.Errorf("%v:\n%s", err, out)
	}
}

func TestDownYesSaysWhatIsLost(t *testing.T) {
	m, _, _, box := downFixture(t, "5", "6")
	out, err := runCLIWithInput(t, "", "down", "482", "--yes")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "It was not saved; `pit snap save 482` beforehand keeps it.") || strings.Contains(out, "[Y/n]") ||
		snapshots(t, m, box) != 0 || !gone(t, m, box) {
		t.Errorf("snapshots %d:\n%s", snapshots(t, m, box), out)
	}
}

// Asked to keep it and unable to, pit keeps the sandbox.
func TestDownKeepsTheSandboxWhenSavingFails(t *testing.T) {
	m, _, h, box := downFixture(t, "5", "6")
	h.saveFail = true
	_, err := runCLIWithInput(t, "y\n", "down", "482")
	if err == nil || !strings.Contains(err.Error(), "saving #482's data failed, so it was not removed") {
		t.Errorf("err = %v", err)
	}
	if gone(t, m, box) {
		t.Error("removed although saving failed")
	}
}

func TestDownOfAStoppedSandboxWithChangedData(t *testing.T) {
	box := snapBox(t, withWrites, "")
	box.Writes, box.Edited = []int64{5}, true
	m, fake := withManager(t, box)
	fake.Declared = []string{"web", "db"}
	m.Proc, m.Git = &holdingBack{fake: fake, project: box.Project}, noGit{}
	out, err := runCLIWithInput(t, "", "down", "482")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "It is not running, so pit cannot save it; `pit 482` starts it again.") || !gone(t, m, box) {
		t.Errorf("output:\n%s", out)
	}
}

func TestDownAllSaysWhichDataWasChanged(t *testing.T) {
	m, _, _, box := downFixture(t, "6", "6")
	out, err := runCLIWithInput(t, "y\nn\n", "down", "--all")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "(its data was changed since it was loaded)") || !strings.Contains(out, "Save it as a snapshot first?") ||
		snapshots(t, m, box) != 0 || !gone(t, m, box) {
		t.Errorf("snapshots %d:\n%s", snapshots(t, m, box), out)
	}
}

func replaceFixture(t *testing.T, count string) (*sandbox.Manager, *holdingBack, state.Sandbox) {
	t.Helper()
	box := snapBox(t, withWrites, withWrites)
	box.Writes = []int64{5}
	m, fake := withManager(t, box)
	fake.Declared = []string{"web", "db"}
	if err := fake.Up(t.Context(), sandbox.RuntimeSandbox(box), nil, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	h := &holdingBack{fake: fake, project: box.Project, before: count, after: count}
	m.Proc, m.Git = h, noGit{}
	return m, h, box
}

// Loading a scenario again replaces changed data too: first the
// question whether to load it, then the offer to keep what is there.
func TestDataResetOffersToSaveChangedData(t *testing.T) {
	m, _, box := replaceFixture(t, "6")
	out, err := runCLIWithInput(t, "y\n\n", "data", "reset", "482")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "is about to be replaced") || snapshots(t, m, box) != 1 {
		t.Errorf("snapshots %d:\n%s", snapshots(t, m, box), out)
	}
	if applied := m.Data.(interface{ Applied() []string }).Applied(); len(applied) != 1 {
		t.Errorf("applied %v", applied)
	}

	m, _, box = replaceFixture(t, "5")
	out, err = runCLIWithInput(t, "y\n", "data", "reset", "482")
	if err != nil || strings.Contains(out, "changed") || snapshots(t, m, box) != 0 {
		t.Errorf("unchanged data: %v, snapshots %d:\n%s", err, snapshots(t, m, box), out)
	}
}

func TestSnapRestoreYesSaysWhatIsLost(t *testing.T) {
	m, h, box := replaceFixture(t, "6")
	id := saved(t, m, box, "")
	h.before, h.after = "6", "6"
	out, err := runCLIWithInput(t, "", "snap", "restore", "482", id, "--yes")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "It was not saved; `pit snap save 482` beforehand keeps it.") || strings.Contains(out, "[Y/n]") || snapshots(t, m, box) != 1 {
		t.Errorf("snapshots %d:\n%s", snapshots(t, m, box), out)
	}
}

// Failing to save leaves the data alone.
func TestSnapRestoreKeepsTheDataWhenSavingFails(t *testing.T) {
	m, h, box := replaceFixture(t, "6")
	id := saved(t, m, box, "")
	h.saveFail = true
	_, err := runCLIWithInput(t, "y\n\n", "snap", "restore", "482", id)
	if err == nil || !strings.Contains(err.Error(), "saving #482's data failed, so it was left as it is") {
		t.Errorf("err = %v", err)
	}
	for _, line := range h.ran {
		if strings.Contains(line, "psql -U app -d app") && !strings.Contains(line, "SELECT") {
			t.Errorf("restored anyway: %s", line)
		}
	}
}

// A sandbox that is not running is not asked for its count: its
// containers would not answer, and pit could not save it anyway.
func TestDownOfAStoppedSandboxIsNotCounted(t *testing.T) {
	box := snapBox(t, withWrites, "")
	box.Writes = []int64{5}
	m, fake := withManager(t, box)
	fake.Declared = []string{"web", "db"}
	h := &holdingBack{fake: fake, project: box.Project, before: "6", after: "6"}
	m.Proc, m.Git = h, noGit{}
	out, err := runCLIWithInput(t, "", "down", "482")
	if err != nil || strings.Contains(out, "changed") || !gone(t, m, box) {
		t.Errorf("%v:\n%s", err, out)
	}
	for _, line := range h.ran {
		if strings.Contains(line, "SELECT 1") {
			t.Errorf("counted a stopped sandbox: %s", line)
		}
	}
}
