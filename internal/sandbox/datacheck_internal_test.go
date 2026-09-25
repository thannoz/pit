package sandbox

import (
	"testing"
	"time"
)

func TestLosses(t *testing.T) {
	before := shape{
		rows:    map[string]int64{"orders": 43, "customers": 431, "legacy_orders": 12, "notes": 7},
		columns: map[string][]string{"orders": {"id", "item"}, "customers": {"id", "tax_code", "name"}, "legacy_orders": {"id"}, "notes": {"id"}},
	}
	after := shape{
		rows:    map[string]int64{"orders": 40, "customers": 431, "notes": 9, "refunds": 3},
		columns: map[string][]string{"orders": {"id", "item", "vat"}, "customers": {"id", "name"}, "notes": {"id"}, "refunds": {"id"}},
	}
	got := losses(before, after)
	want := []Loss{
		{Table: "customers", Column: "tax_code", Rows: 431},
		{Table: "legacy_orders", Rows: 12, Dropped: true},
		{Table: "orders", Rows: 3},
	}
	if len(got) != len(want) {
		t.Fatalf("losses %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("loss %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if l := losses(before, before); len(l) != 0 {
		t.Errorf("nothing changed, and yet %+v", l)
	}
	// Columns only: a table is gone when its columns are.
	onlyColumns := shape{columns: map[string][]string{"a": {"x"}, "b": {"y"}}}
	if l := losses(onlyColumns, shape{columns: map[string][]string{"b": {"y"}}}); len(l) != 1 || !l[0].Dropped || l[0].Table != "a" {
		t.Errorf("losses %+v", l)
	}
}

func TestShapeTotal(t *testing.T) {
	if n := (shape{rows: map[string]int64{"a": 2, "b": 40}}).total(); n != 42 {
		t.Errorf("total %d", n)
	}
}

// A lock seen first in a weaker mode and then in the strongest is told
// in the strongest.
func TestLockWatchKeepsTheStrongestMode(t *testing.T) {
	w := &lockWatch{seen: map[string]*Lock{}, first: map[string]time.Time{}, stop: func() {}, done: make(chan struct{})}
	close(w.done)
	start := time.Unix(1000, 0)
	w.saw(start, [][]string{{"orders", "ShareLock"}})
	w.saw(start.Add(300*time.Millisecond), [][]string{{"orders", "AccessExclusiveLock"}, {"notes", "ShareLock"}})
	w.saw(start.Add(600*time.Millisecond), [][]string{{"orders", "ShareLock"}, {"short"}})
	got := w.locks()
	if len(got) != 2 || got[1].Table != "orders" || got[1].Mode != "AccessExclusiveLock" || got[1].Held != 600*time.Millisecond ||
		got[0].Table != "notes" || got[0].Held != 0 {
		t.Errorf("locks %+v", got)
	}
}
