package sandbox_test

import (
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/thannoz/pit/internal/config"
	"github.com/thannoz/pit/internal/data/datatest"
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
