package cli

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/thannoz/pit/internal/analysis"
	"github.com/thannoz/pit/internal/diff"
	"github.com/thannoz/pit/internal/forge"
	"github.com/thannoz/pit/internal/sandbox"
	"github.com/thannoz/pit/internal/ui"
)

func migration(path string) analysis.File {
	return analysis.File{File: diff.File{Path: path, Change: diff.Added}, Kind: analysis.Migration}
}

// withCheck answers pit migrate-check with a check, and notes the
// scenario asked for.
func withCheck(t *testing.T, check sandbox.MigrationCheck, err error) *[]string {
	t.Helper()
	var asked []string
	prevPlan, prevCheck := planUp, checkMigrations
	planUp = func(c *cobra.Command, _ *upOptions, _ string) (*upPlan, error) {
		out := ui.New(c.OutOrStdout(), c.ErrOrStderr())
		return &upPlan{c: c, out: out, rep: newStepReporter(out, c.ErrOrStderr()),
			pull: forge.PR{Number: 482, BaseBranch: "main"}}, nil
	}
	checkMigrations = func(_ *upPlan, scenario string) (sandbox.MigrationCheck, error) {
		asked = append(asked, scenario)
		return check, err
	}
	t.Cleanup(func() { planUp, checkMigrations = prevPlan, prevCheck })
	return &asked
}

var passed = sandbox.MigrationCheck{
	BaseSHA: "8c21f0d1e2b3a495", HeadSHA: "a3f91c2e4b7d8091", Scenario: "standard",
	Migrations: analysis.Migrations{New: []analysis.File{migration("migrations/0042_add_vat_id_to_orders.sql")}},
	Took:       1430 * time.Millisecond,
}

// TestMigrateCheckOutput is the output half of T-906's criterion: what
// docs/03 draws -- the base, the migration, whether it ran and how long.
func TestMigrateCheckOutput(t *testing.T) {
	asked := withCheck(t, passed, nil)
	out, _, err := run(t, "migrate-check", "482", "--scenario=bulk")
	if err != nil {
		t.Fatal(err)
	}
	want := "  Base:        main @ 8c21f0d, scenario \"standard\"\n" +
		"  Migration:   migrations/0042_add_vat_id_to_orders.sql\n" +
		"\n" +
		"  ✓ ran through  1.4s\n"
	if out != want {
		t.Errorf("output\n%s\nwant\n%s", out, want)
	}
	if len(*asked) != 1 || (*asked)[0] != "bulk" {
		t.Errorf("asked %v", *asked)
	}
}

func TestMigrateCheckThatFails(t *testing.T) {
	failed := passed
	failed.Migrations.New = append(failed.Migrations.New, migration("migrations/0043_drop_tax_code.sql"))
	failed.Migrations.Changed = []analysis.File{migration("migrations/0041_orders.sql")}
	failed.Failed = errors.New("data.migrate entry 1: exit status 3\nERROR: column \"vat\" already exists")
	failed.Took = 300 * time.Millisecond
	withCheck(t, failed, nil)
	out, _, err := run(t, "migrate-check", "482")
	if err == nil || !strings.Contains(err.Error(), "the migrations of #482 fail on the data of its base") {
		t.Errorf("err = %v", err)
	}
	for _, want := range []string{
		"  Migrations:  migrations/0042_add_vat_id_to_orders.sql, migrations/0043_drop_tax_code.sql\n",
		"  Changed:     migrations/0041_orders.sql  (existing, changed",
		"  ✗ failed after 300ms\n    data.migrate entry 1: exit status 3\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestMigrateCheckWithNothingToCheck(t *testing.T) {
	withCheck(t, sandbox.MigrationCheck{Migrations: analysis.Migrations{Removed: []analysis.File{migration("migrations/0001_old.sql")}}}, nil)
	out, _, err := run(t, "migrate-check", "482")
	if err != nil || !strings.Contains(out, "#482 adds no migrations and changes none; there is nothing to check.\n  It removes migrations/0001_old.sql.") {
		t.Errorf("%v:\n%s", err, out)
	}
}

func TestMigrateCheckJSON(t *testing.T) {
	withCheck(t, passed, nil)
	out, _, err := run(t, "migrate-check", "482", "--json")
	var r migrationCheckJSON
	if err != nil || json.Unmarshal([]byte(out), &r) != nil || !r.Ran || r.TookMS != 1430 || len(r.New) != 1 || r.Failed != "" || r.BaseBranch != "main" {
		t.Errorf("%v:\n%s", err, out)
	}
}

func TestMigrateCheckErrors(t *testing.T) {
	withCheck(t, sandbox.MigrationCheck{}, errors.New("docker is not running"))
	if _, _, err := run(t, "migrate-check", "482"); err == nil || !strings.Contains(err.Error(), "docker is not running") {
		t.Errorf("err = %v", err)
	}
	if _, _, err := run(t, "migrate-check", "482", "--base"); err == nil {
		t.Error("--base accepted")
	}
}
