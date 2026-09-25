package analysis

import (
	"strings"
	"testing"

	"github.com/thannoz/pit/internal/diff"
)

func paths(files []File) string {
	var out []string
	for _, f := range files {
		out = append(out, f.Path)
	}
	return strings.Join(out, " ")
}

// TestTwoNewMigrations is the acceptance criterion for T-905: a pull
// request that adds two migrations has them found, in the order they
// run, apart from what else it touches.
func TestTwoNewMigrations(t *testing.T) {
	d := diff.Diff{Files: []diff.File{
		{Path: "migrations/010_refund_reasons.sql", Change: diff.Added},
		{Path: "migrations/002_refunds.sql", Change: diff.Added},
		{Path: "migrations/001_orders.sql", Change: diff.Modified},
		{Path: "migrations/README.md", Change: diff.Added},
		{Path: "main.go", Change: diff.Modified},
		{Path: "prisma/schema.prisma", Change: diff.Modified},
		{Path: "migrations/000_old.sql", Change: diff.Deleted},
	}}
	m := MigrationsOf(Classify(d, nil))
	if got := paths(m.New); got != "migrations/002_refunds.sql migrations/010_refund_reasons.sql" {
		t.Errorf("new: %s", got)
	}
	if got := paths(m.Changed); got != "migrations/001_orders.sql" {
		t.Errorf("changed: %s", got)
	}
	if got := paths(m.Removed); got != "migrations/000_old.sql" {
		t.Errorf("removed: %s", got)
	}
	if m.Empty() {
		t.Error("empty")
	}
}

// Where a project keeps its migrations is what it says, and only that.
func TestMigrationsWhereTheProjectSays(t *testing.T) {
	d := diff.Diff{Files: []diff.File{
		{Path: "db/schema/20260925_add_vat.sql", Change: diff.Added},
		{Path: "db/schema/20260924_refunds.sql", Change: diff.Added},
		{Path: "migrations/helpers.go", Change: diff.Added},
		{Path: "db/seeds/orders.sql", Change: diff.Added},
	}}
	files := ClassifyWith(d, Options{Migrations: []string{"db/schema/*.sql"}})
	m := MigrationsOf(files)
	if got := paths(m.New); got != "db/schema/20260924_refunds.sql db/schema/20260925_add_vat.sql" {
		t.Errorf("new: %s", got)
	}
	for _, f := range files {
		switch f.Path {
		case "db/schema/20260925_add_vat.sql":
			if f.Reason != `a migration, by data.migrations "db/schema/*.sql"` {
				t.Errorf("reason %q", f.Reason)
			}
		case "migrations/helpers.go":
			if f.Kind == Migration || f.Kind != Code {
				t.Errorf("helpers.go is %s: %s", f.Kind, f.Reason)
			}
		}
	}
	// Without patterns, the usual places.
	if got := paths(MigrationsOf(Classify(d, nil)).New); got != "migrations/helpers.go" {
		t.Errorf("by the usual places: %s", got)
	}
}

func TestNoMigrations(t *testing.T) {
	d := diff.Diff{Files: []diff.File{{Path: "main.go", Change: diff.Modified}}}
	if !MigrationsOf(Classify(d, nil)).Empty() {
		t.Error("not empty")
	}
}

func TestNaturalOrder(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		less bool
	}{
		{"2_add.sql", "10_drop.sql", true},
		{"10_drop.sql", "2_add.sql", false},
		{"002_add.sql", "10_drop.sql", true},
		{"20260924_a.sql", "20260925_a.sql", true},
		{"a/1.sql", "b/0.sql", true},
		{"1_a.sql", "1_b.sql", true},
	} {
		if got := naturally(tc.a, tc.b) < 0; got != tc.less {
			t.Errorf("naturally(%q, %q) < 0 = %v", tc.a, tc.b, got)
		}
	}
}

// Numbers without leading zeros run in their order, not the alphabet's.
func TestMigrationsRunInNumberOrder(t *testing.T) {
	d := diff.Diff{Files: []diff.File{
		{Path: "migrations/10_drop_legacy.sql", Change: diff.Added},
		{Path: "migrations/9_add_vat.sql", Change: diff.Added},
	}}
	if got := paths(MigrationsOf(Classify(d, nil)).New); got != "migrations/9_add_vat.sql migrations/10_drop_legacy.sql" {
		t.Errorf("new: %s", got)
	}
}

// Editing an existing migration, or removing one, is touching them too.
func TestTouchingMigrationsWithoutAddingOne(t *testing.T) {
	for _, change := range []diff.Change{diff.Modified, diff.Deleted} {
		d := diff.Diff{Files: []diff.File{{Path: "migrations/001_orders.sql", Change: change}}}
		if MigrationsOf(Classify(d, nil)).Empty() {
			t.Errorf("%s: empty", change)
		}
	}
}
