package sandbox_test

import (
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/thannoz/pit/internal/config"
	"github.com/thannoz/pit/internal/data/datatest"
	"github.com/thannoz/pit/internal/forge"
	"github.com/thannoz/pit/internal/proc"
	"github.com/thannoz/pit/internal/runtime"
	"github.com/thannoz/pit/internal/runtime/runtimetest"
	"github.com/thannoz/pit/internal/sandbox"
	"github.com/thannoz/pit/internal/workspace"
)

// withMigration makes #7 bring a migration, and the configuration one
// that runs migrate: on the pull request's commit, where the file is,
// it takes a while -- or fails.
func withMigration(t *testing.T, migrate string) (*sandbox.Manager, sandbox.UpRequest, *runtimetest.Fake) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	m, req, fake := upFixture(t)
	pushToPullRequest(t, req.Repo.Root, 7, map[string]string{"migrations/002_add_vat.sql": "ALTER TABLE orders ADD COLUMN vat integer;\n"})
	cfg, err := config.Parse([]byte("web:\n  service: web\n  port: 80\ndata:\n  migrate: [" + migrate + "]\n  scenarios:\n    - name: standard\n      apply: [\"true\"]\n  default: standard\n"))
	if err != nil {
		t.Fatal(err)
	}
	req.Config = cfg
	return m, req, fake
}

const slowAtHead = `"sh -c 'if test -f migrations/002_add_vat.sql; then sleep 0.4; fi'"`

// TestCheckMigrations is the acceptance criterion for T-906: the base
// comes up with its data, moves to the pull request, and the migrations
// run there, timed; nothing is left afterwards.
func TestCheckMigrations(t *testing.T) {
	m, req, fake := withMigration(t, slowAtHead)
	req.Scenario = "standard"

	check, err := m.CheckMigrations(t.Context(), req, &quietReporter{})
	if err != nil {
		t.Fatal(err)
	}
	if !check.Ran() || len(check.Migrations.New) != 1 || check.Migrations.New[0].Path != "migrations/002_add_vat.sql" {
		t.Fatalf("migrations %+v", check.Migrations)
	}
	// The migrations alone: 0.4s of them, not the build and the start
	// around them.
	if check.Failed != nil || check.Took < 400*time.Millisecond || check.Took > time.Second {
		t.Errorf("took %v, failed %v", check.Took, check.Failed)
	}
	if check.BaseSHA == "" || check.HeadSHA == "" || check.BaseSHA == check.HeadSHA || check.Scenario != "standard" {
		t.Errorf("check %+v", check)
	}
	// The data was loaded once, on the base, and kept for the pull
	// request.
	if got := m.Data.(*datatest.Fake).Applied(); !slices.Equal(got, []string{"standard"}) {
		t.Errorf("applied %v", got)
	}
	assertCheckGone(t, m, req)
	if project, _ := runtime.ProjectNameIn(req.Repo.Identity.Ref(), 7, "check"); fake.IsUp(project) {
		t.Errorf("%s is still up", project)
	}
	for _, ref := range []string{workspace.LocalRef(7), workspace.BaseRef(7)} {
		if _, err := workspace.ResolveRef(t.Context(), proc.Exec{}, req.Repo.Root, ref); err == nil {
			t.Errorf("%s was left", ref)
		}
	}
}

func assertCheckGone(t *testing.T, m *sandbox.Manager, req sandbox.UpRequest) {
	t.Helper()
	f, _ := m.Store.Load()
	for _, box := range f.Sandboxes {
		if box.Check {
			t.Errorf("the check is still recorded: %+v", box)
		}
	}
	if _, err := os.Stat(req.Repo.Identity.WorktreeDirIn(m.StateDir, 7, "check")); !os.IsNotExist(err) {
		t.Errorf("the check's worktree is still there: %v", err)
	}
}

// A migration that fails on the base's data is the finding, not an
// error of the check.
func TestCheckMigrationsThatFail(t *testing.T) {
	m, req, _ := withMigration(t, `"sh -c 'if test -f migrations/002_add_vat.sql; then echo column orders.vat exists >&2; exit 3; fi'"`)
	check, err := m.CheckMigrations(t.Context(), req, &quietReporter{})
	if err != nil {
		t.Fatal(err)
	}
	if check.Failed == nil || !strings.Contains(check.Failed.Error(), "data.migrate entry 1") || check.Took <= 0 {
		t.Errorf("failed %v after %v", check.Failed, check.Took)
	}
	assertCheckGone(t, m, req)
}

// A pull request that brings no migrations brings nothing up.
func TestCheckWithoutMigrations(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	m, req, fake := upFixture(t)
	check, err := m.CheckMigrations(t.Context(), req, &quietReporter{})
	if err != nil || check.Ran() {
		t.Fatalf("%+v, %v", check, err)
	}
	if len(fake.Calls()) != 0 {
		t.Errorf("the runtime was asked %v", fake.Calls())
	}
	if _, err := workspace.ResolveRef(t.Context(), proc.Exec{}, req.Repo.Root, workspace.LocalRef(7)); err == nil {
		t.Error("the fetched ref was left")
	}
}

// The reviewer's sandboxes are not the check's: they stay, and so do
// the refs they run from.
func TestCheckLeavesTheReviewersSandboxes(t *testing.T) {
	m, req, fake := withMigration(t, slowAtHead)
	own, err := m.Up(t.Context(), req, &quietReporter{})
	if err != nil {
		t.Fatal(err)
	}
	base := req
	base.Base = true
	theirBase, err := m.Up(t.Context(), base, &quietReporter{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.CheckMigrations(t.Context(), req, &quietReporter{}); err != nil {
		t.Fatal(err)
	}
	f, _ := m.Store.Load()
	if got, ok := f.Find(req.Repo.Identity.Ref(), 7); !ok || got.Project != own.Project {
		t.Errorf("the pull request's sandbox: %+v", f.Sandboxes)
	}
	if _, err := workspace.ResolveRef(t.Context(), proc.Exec{}, req.Repo.Root, workspace.LocalRef(7)); err != nil {
		t.Errorf("the pull request's ref went: %v", err)
	}
	if !fake.IsUp(own.Project) || !fake.IsUp(theirBase.Project) {
		t.Errorf("the reviewer's sandboxes went down")
	}
	assertCheckGone(t, m, req)
}

// Without migrations to check, the refs a reviewer's sandbox runs from
// stay too.
func TestNothingToCheckLeavesTheRefs(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	m, req, _ := upFixture(t)
	if _, err := m.Up(t.Context(), req, &quietReporter{}); err != nil {
		t.Fatal(err)
	}
	if check, err := m.CheckMigrations(t.Context(), req, &quietReporter{}); err != nil || check.Ran() {
		t.Fatalf("%+v, %v", check, err)
	}
	if _, err := workspace.ResolveRef(t.Context(), proc.Exec{}, req.Repo.Root, workspace.LocalRef(7)); err != nil {
		t.Errorf("the pull request's ref went: %v", err)
	}
}

// What a check that did not finish left is cleared before the next.
func TestCheckAfterOneThatDidNotFinish(t *testing.T) {
	m, req, _ := withMigration(t, slowAtHead)
	left := req
	left.Base, left.Check = true, true
	if _, err := m.Up(t.Context(), left, &quietReporter{}); err != nil {
		t.Fatal(err)
	}
	check, err := m.CheckMigrations(t.Context(), req, &quietReporter{})
	if err != nil || check.Failed != nil || check.Took < 400*time.Millisecond {
		t.Errorf("%+v, %v", check, err)
	}
	// Loaded afresh: the leftover's data was not the scenario's.
	if got := m.Data.(*datatest.Fake).Applied(); len(got) != 2 {
		t.Errorf("applied %v", got)
	}
	assertCheckGone(t, m, req)
}

// withDestruction makes #7's migration take data away, and the check
// commands tell by the worktree -- the pull request's has the migration
// -- what the database would hold. While the migration runs, orders is
// locked.
func withDestruction(t *testing.T, check bool) (*sandbox.Manager, sandbox.UpRequest) {
	t.Helper()
	m, req, _ := withMigration(t, `"sh -c 'if test -f migrations/002_add_vat.sql; then touch .locked; sleep 0.6; rm .locked; fi'"`)
	if !check {
		return m, req
	}
	req.Config.Data.Check = config.Check{
		Rows:    `sh -c 'if test -f migrations/002_add_vat.sql; then printf "orders|40\ncustomers|431\n"; else printf "orders|43\ncustomers|431\nlegacy|12\n"; fi'`,
		Columns: `sh -c 'if test -f migrations/002_add_vat.sql; then printf "orders|id\ncustomers|id\n"; else printf "orders|id\ncustomers|id\ncustomers|tax_code\nlegacy|id\n"; fi'`,
		Locks:   `sh -c 'if test -f .locked; then printf "orders|AccessExclusiveLock\n"; fi'`,
	}
	return m, req
}

// TestADestructiveMigration is the acceptance criterion for T-907: a
// migration that takes data away is reported as one -- the table, the
// column, the rows -- and so is the lock it holds.
func TestADestructiveMigration(t *testing.T) {
	m, req := withDestruction(t, true)
	check, err := m.CheckMigrations(t.Context(), req, &quietReporter{})
	if err != nil || check.Failed != nil {
		t.Fatalf("%v, %v", err, check.Failed)
	}
	if check.Rows != 486 || check.Unseen != "" {
		t.Errorf("rows %d, unseen %q", check.Rows, check.Unseen)
	}
	want := []sandbox.Loss{
		{Table: "customers", Column: "tax_code", Rows: 431},
		{Table: "legacy", Rows: 12, Dropped: true},
		{Table: "orders", Rows: 3},
	}
	if len(check.Losses) != len(want) {
		t.Fatalf("losses %+v", check.Losses)
	}
	for i := range want {
		if check.Losses[i] != want[i] {
			t.Errorf("loss %d = %+v", i, check.Losses[i])
		}
	}
	if len(check.Locks) != 1 || check.Locks[0].Table != "orders" || check.Locks[0].Mode != "AccessExclusiveLock" ||
		check.Locks[0].Held < 200*time.Millisecond || check.Locks[0].Held > time.Second {
		t.Errorf("locks %+v", check.Locks)
	}
}

// Without data.check, pit says it could not look.
func TestAMigrationCheckWithoutDataCheck(t *testing.T) {
	m, req := withDestruction(t, false)
	check, err := m.CheckMigrations(t.Context(), req, &quietReporter{})
	if err != nil || !strings.Contains(check.Unseen, "data.check is not configured") || len(check.Losses) != 0 || len(check.Locks) != 0 {
		t.Errorf("%+v, %v", check, err)
	}
}

// A check command that fails is said, and the migrations still run.
func TestAMigrationCheckWhoseCountFails(t *testing.T) {
	m, req := withDestruction(t, true)
	req.Config.Data.Check.Rows = `sh -c 'echo relation does not exist >&2; exit 1'`
	check, err := m.CheckMigrations(t.Context(), req, &quietReporter{})
	if err != nil || check.Failed != nil || !strings.Contains(check.Unseen, "relation does not exist") || check.Took <= 0 {
		t.Errorf("%+v, %v", check, err)
	}
}

// Counts come as psql prints them, with "|", or as mysql does, with
// tabs; one that is not a number is said.
func TestCheckCommandsOutput(t *testing.T) {
	m, req := withDestruction(t, true)
	req.Config.Data.Check.Rows = `sh -c 'printf "orders\t43\ncustomers\t431\n"'`
	req.Config.Data.Check.Columns = ""
	req.Config.Data.Check.Locks = ""
	check, err := m.CheckMigrations(t.Context(), req, &quietReporter{})
	if err != nil || check.Rows != 474 || check.Unseen != "" {
		t.Errorf("rows %d, unseen %q, %v", check.Rows, check.Unseen, err)
	}
	req.Config.Data.Check.Rows = `sh -c 'printf "orders|many\n"'`
	check, err = m.CheckMigrations(t.Context(), req, &quietReporter{})
	if err != nil || !strings.Contains(check.Unseen, `printed "many" for orders, which is not a number of rows`) {
		t.Errorf("unseen %q, %v", check.Unseen, err)
	}
}

// withRollback is withDestruction, with counts that go back to the
// base's once .rolledback exists -- which the rollback given makes, or
// not.
func withRollback(t *testing.T, rollback string) (*sandbox.Manager, sandbox.UpRequest) {
	t.Helper()
	m, req := withDestruction(t, true)
	const head = `test -f migrations/002_add_vat.sql && ! test -f .rolledback`
	req.Config.Data.Check.Rows = `sh -c 'if ` + head + `; then printf "orders|40\ncustomers|431\n"; else printf "orders|43\ncustomers|431\nlegacy|12\n"; fi'`
	req.Config.Data.Check.Columns = `sh -c 'if ` + head + `; then printf "orders|id\norders|vat\ncustomers|id\n"; else printf "orders|id\ncustomers|id\ncustomers|tax_code\nlegacy|id\n"; fi'`
	req.Config.Data.Rollback = []string{rollback}
	return m, req
}

// TestAMigrationThatCannotBeUndone is the acceptance criterion for
// T-908: a rollback that does not bring the schema back is reported,
// what is left and what did not come back.
func TestAMigrationThatCannotBeUndone(t *testing.T) {
	m, req := withRollback(t, "true")
	check, err := m.CheckMigrations(t.Context(), req, &quietReporter{})
	if err != nil {
		t.Fatal(err)
	}
	r := check.Rollback
	want := []string{
		"column orders.vat is still there",
		"column customers.tax_code did not come back",
		"table legacy did not come back",
	}
	if !r.Ran || r.Failed != nil || !r.Compared || r.Reversible() || !sameSet(r.Leftover, want) {
		t.Errorf("rollback %+v", r)
	}
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for _, x := range a {
		if !slices.Contains(b, x) {
			return false
		}
	}
	return true
}

func TestAMigrationThatCanBeUndone(t *testing.T) {
	m, req := withRollback(t, "touch .rolledback")
	check, err := m.CheckMigrations(t.Context(), req, &quietReporter{})
	if err != nil || !check.Rollback.Reversible() || check.Rollback.Took <= 0 {
		t.Errorf("rollback %+v, %v", check.Rollback, err)
	}
}

func TestARollbackThatFails(t *testing.T) {
	m, req := withRollback(t, `sh -c 'echo irreversible >&2; exit 4'`)
	check, err := m.CheckMigrations(t.Context(), req, &quietReporter{})
	if err != nil || check.Rollback.Failed == nil || !strings.Contains(check.Rollback.Failed.Error(), "data.rollback entry 1") || check.Rollback.Reversible() {
		t.Errorf("rollback %+v, %v", check.Rollback, err)
	}
}

// {migrations} is how many migrations the pull request adds.
func TestRollbackIsToldHowMany(t *testing.T) {
	m, req := withRollback(t, `sh -c 'test {migrations} = 1 && touch .rolledback'`)
	check, err := m.CheckMigrations(t.Context(), req, &quietReporter{})
	if err != nil || !check.Rollback.Reversible() {
		t.Errorf("rollback %+v, %v", check.Rollback, err)
	}
}

// A migration kept as an up and down pair is missed when its down is
// not there.
func TestAnUpMigrationWithoutItsDown(t *testing.T) {
	m, req, _ := withMigration(t, `"true"`)
	pushToPullRequest(t, req.Repo.Root, 7, map[string]string{
		"migrations/003_index.up.sql":   "CREATE INDEX orders_item ON orders (item);\n",
		"migrations/003_index.down.sql": "DROP INDEX orders_item;\n",
		"migrations/004_drop.up.sql":    "ALTER TABLE orders DROP COLUMN note;\n",
	})
	check, err := m.CheckMigrations(t.Context(), req, &quietReporter{})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(check.Rollback.MissingDown, " "); got != "migrations/004_drop.up.sql" {
		t.Errorf("missing down: %q (new %v)", got, check.Migrations.New)
	}
}

// Without data.check the rollback runs, and pit does not claim to know
// what it left.
func TestARollbackWithoutDataCheck(t *testing.T) {
	m, req := withDestruction(t, false)
	req.Config.Data.Rollback = []string{"true"}
	check, err := m.CheckMigrations(t.Context(), req, &quietReporter{})
	if err != nil || !check.Rollback.Ran || check.Rollback.Compared || len(check.Rollback.Leftover) != 0 {
		t.Errorf("rollback %+v, %v", check.Rollback, err)
	}
}

// A pull request that brings its own rollback has it used.
func TestARollbackThePullRequestBrings(t *testing.T) {
	m, req := withRollback(t, "true")
	pushToPullRequest(t, req.Repo.Root, 7, map[string]string{".pit.yaml": "web:\n  service: web\n  port: 80\ndata:\n" +
		"  migrate: [\"sh -c 'if test -f migrations/002_add_vat.sql; then touch .locked; sleep 0.6; rm .locked; fi'\"]\n" +
		"  rollback: [\"touch .rolledback\"]\n" +
		"  scenarios:\n    - name: standard\n      apply: [\"true\"]\n  default: standard\n"})
	check, err := m.CheckMigrations(t.Context(), req, &quietReporter{})
	if err != nil || !check.Rollback.Reversible() {
		t.Errorf("rollback %+v, %v", check.Rollback, err)
	}
}

// A pull request on a service that keeps no ref for it is checked at
// its branch, like it is reviewed.
func TestCheckFetchesTheBranchAServiceNames(t *testing.T) {
	m, req, _ := withMigration(t, `"true"`)
	req.PR.Source = forge.Source{Branch: "fix-tax", Lost: true}
	if _, err := m.CheckMigrations(t.Context(), req, &quietReporter{}); err == nil || !strings.Contains(err.Error(), "branch fix-tax is in a repository the service no longer shows") {
		t.Errorf("err = %v", err)
	}
}
