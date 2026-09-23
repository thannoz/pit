package cli

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/state"
)

func timed(pr int) state.Sandbox {
	box := recorded(pr, "github.com/acme/shop", "acme-shop-c56680", "refunds", time.Minute)
	box.Steps = []state.Step{
		{Name: "fetch", Millis: 420},
		{Name: "worktree", Millis: 180},
		{Name: "services", Millis: 16320},
		{Name: "migrate", Millis: 640},
		{Name: "data", Millis: 310},
		{Name: "healthy", Millis: 2010},
	}
	box.SetupMillis = 20400
	return box
}

// TestTimingShowsWhereTheTimeWent is the acceptance criterion for
// T-501.
func TestTimingShowsWhereTheTimeWent(t *testing.T) {
	withManager(t, timed(482))

	out, _, err := run(t, "timing", "482")
	if err != nil {
		t.Fatalf("pit timing: %v", err)
	}

	for _, want := range []string{"STEP", "TOOK", "SHARE", "fetch", "services", "healthy", "total"} {
		if !strings.Contains(out, want) {
			t.Errorf("output is missing %q:\n%s", want, out)
		}
	}

	// The one that matters has to be recognisable at a glance: it is
	// four fifths of the setup.
	if !lineWith(out, "services", "16.3s", "80%") {
		t.Errorf("the slow step is not shown with its share:\n%s", out)
	}
	if !lineWith(out, "fetch", "420ms") {
		t.Errorf("a short step is not shown at a useful resolution:\n%s", out)
	}
	if !lineWith(out, "total", "20.4s") {
		t.Errorf("the total is missing:\n%s", out)
	}
}

func TestTimingAccountsForTheTimeNoStepClaimed(t *testing.T) {
	// The steps add up to 19.9s of a 20.4s setup. Hiding the rest
	// would make the table add up to a lie.
	withManager(t, timed(482))

	out, _, err := run(t, "timing", "482")
	if err != nil {
		t.Fatalf("pit timing: %v", err)
	}
	if !lineWith(out, "the rest", "520ms") {
		t.Errorf("unaccounted time is not shown:\n%s", out)
	}
}

func TestTimingWithoutRecordedSteps(t *testing.T) {
	// Sandboxes built before this existed have no timings, and
	// inventing them would be worse than saying so.
	withManager(t, recorded(7, "github.com/acme/shop", "acme-shop-c56680", "feature", time.Minute))

	_, _, err := run(t, "timing", "7")
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "no timings") {
		t.Errorf("error = %q", err)
	}
	if errs.Hint(err) == "" {
		t.Error("the error carries no hint")
	}
}

func TestTimingJSON(t *testing.T) {
	withManager(t, timed(482))

	out, _, err := run(t, "timing", "482", "--json")
	if err != nil {
		t.Fatalf("pit timing --json: %v", err)
	}

	var rows []struct {
		Step   string `json:"step"`
		Millis int64  `json:"ms"`
	}
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}

	if len(rows) != 7 {
		t.Fatalf("got %d rows, want the six steps and the total", len(rows))
	}
	if rows[0].Step != "fetch" || rows[0].Millis != 420 {
		t.Errorf("first row = %+v, want the first step as it ran", rows[0])
	}
	if last := rows[len(rows)-1]; last.Step != "total" || last.Millis != 20400 {
		t.Errorf("last row = %+v, want the total", last)
	}
}

func TestTookReadsAtTheRightResolution(t *testing.T) {
	tests := map[time.Duration]string{
		340 * time.Millisecond:                "340ms",
		0:                                     "0ms",
		2*time.Second + 10*time.Millisecond:   "2.0s",
		16*time.Second + 320*time.Millisecond: "16.3s",
		90 * time.Second:                      "1m30s",
	}

	for d, want := range tests {
		if got := took(d); got != want {
			t.Errorf("took(%v) = %q, want %q", d, got, want)
		}
	}
}
