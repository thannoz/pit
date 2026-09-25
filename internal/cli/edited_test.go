package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

const withWrites = `version: 1
web:
  service: web
  port: 80
data:
  scenarios:
    - name: standard
      apply: ["compose exec -T db psql -f /fixtures/standard.sql"]
  snapshot:
    save: "compose exec -T db pg_dump -U app app"
    restore: "compose exec -T db psql -U app -d app"
    writes: "compose exec -T db psql -At -U app -d app -c 'SELECT 1'"
`

func lsJSONEdited(t *testing.T) *bool {
	t.Helper()
	out, _, err := run(t, "ls", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		Edited *bool `json:"edited"`
	}
	if err := json.Unmarshal([]byte(out), &rows); err != nil || len(rows) != 1 {
		t.Fatalf("%v:\n%s", err, out)
	}
	return rows[0].Edited
}

// TestLsShowsDataEditedSinceItWasLoaded is the acceptance criterion for
// T-707: after a write the listing says so, next to where the data came
// from, and says nothing it cannot know.
func TestLsShowsDataEditedSinceItWasLoaded(t *testing.T) {
	box := snapBox(t, withWrites, "")
	box.Writes = []int64{5}
	_, d := snapManager(t, box, true)

	for _, tc := range []struct {
		count  string
		marked bool
		json   *bool
	}{
		{"5\n", false, new(false)},
		{"6\n", true, new(true)},
		{"0\n", false, nil}, // restarted: pit cannot tell
	} {
		d.dump = tc.count
		out, _, err := run(t, "ls")
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Contains(out, "standard +edited"); got != tc.marked {
			t.Errorf("count %q: marked %v:\n%s", tc.count, got, out)
		}
		got := lsJSONEdited(t)
		if (got == nil) != (tc.json == nil) || (got != nil && *got != *tc.json) {
			t.Errorf("count %q: edited = %v, want %v", tc.count, deref(got), deref(tc.json))
		}
	}
}

func deref(b *bool) any {
	if b == nil {
		return nil
	}
	return *b
}

// Recorded as edited across an update, it is shown as such without
// counting.
func TestLsShowsRecordedEdits(t *testing.T) {
	box := snapBox(t, withWrites, "")
	box.Writes, box.Edited = []int64{5}, true
	_, d := snapManager(t, box, true)
	d.dump = "5\n"
	if out, _, _ := run(t, "ls"); !strings.Contains(out, "standard +edited") {
		t.Errorf("not marked:\n%s", out)
	}
}

// Restoring a snapshot is loading data: it is counted again, and what
// was edited before is gone.
func TestSnapRestoreCountsAgain(t *testing.T) {
	box := snapBox(t, withWrites, "")
	box.Writes, box.Edited = []int64{5}, true
	m, d := snapManager(t, box, true)
	id := saved(t, m, box, "")
	d.dump = "11\n"
	if _, _, err := run(t, "snap", "restore", "482", id, "--yes"); err != nil {
		t.Fatal(err)
	}
	f, err := m.Store.Load()
	if err != nil {
		t.Fatal(err)
	}
	got, _ := f.Find(box.RepoRef, box.PR)
	if len(got.Writes) != 1 || got.Writes[0] != 11 || got.Edited {
		t.Errorf("Writes = %v, Edited = %v", got.Writes, got.Edited)
	}
}

// So is loading a scenario again.
func TestDataResetCountsAgain(t *testing.T) {
	box := snapBox(t, withWrites, withWrites)
	box.Writes, box.Edited = []int64{5}, true
	m, d := snapManager(t, box, true)
	d.dump = "3\n"
	if _, _, err := run(t, "data", "reset", "482", "--yes"); err != nil {
		t.Fatal(err)
	}
	f, err := m.Store.Load()
	if err != nil {
		t.Fatal(err)
	}
	got, _ := f.Find(box.RepoRef, box.PR)
	if len(got.Writes) != 1 || got.Writes[0] != 3 || got.Edited {
		t.Errorf("Writes = %v, Edited = %v", got.Writes, got.Edited)
	}
}

// A writes command that does not print a number is said once, where
// the data is loaded, and pit ls says nothing.
func TestAWritesCommandThatPrintsNoNumber(t *testing.T) {
	box := snapBox(t, withWrites, withWrites)
	m, d := snapManager(t, box, true)
	d.dump = "ERROR: relation does not exist\n"
	_, stderr, err := run(t, "data", "reset", "482", "--yes")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, "could not count the writes to #482's data") || !strings.Contains(stderr, "is not a number") {
		t.Errorf("stderr:\n%s", stderr)
	}
	f, _ := m.Store.Load()
	if got, _ := f.Find(box.RepoRef, box.PR); got.Writes != nil {
		t.Errorf("Writes = %v", got.Writes)
	}
}
