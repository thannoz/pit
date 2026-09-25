package cli

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thannoz/pit/internal/config"
	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/sandbox"
	"github.com/thannoz/pit/internal/snapshot"
	"github.com/thannoz/pit/internal/workspace"
)

const promoteConfig = `version: 1
web:
  service: web
  port: 80
data:
  scenarios:
    - name: standard
  # save prints the dump, restore reads it back.
  snapshot:
    save: "compose exec -T db pg_dump -U app app"
    restore: "compose exec -T db psql -U app -d app"
`

// inRepo makes the commands believe they were run in a checkout at a
// fixed place, with a .pit.yaml, and returns that place.
func inRepo(t *testing.T, yaml string) (string, workspace.Identity) {
	t.Helper()
	root := t.TempDir()
	for name, content := range map[string]string{
		".git": "", "docker-compose.yml": snapCompose, config.FileName: yaml,
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	id := workspace.Identity{Host: "github.com", Owner: "acme", Name: "shop"}
	previous := currentRepo
	currentRepo = func(context.Context) (workspace.Repo, error) {
		return workspace.Repo{Root: root, Identity: id}, nil
	}
	t.Cleanup(func() { currentRepo = previous })
	return root, id
}

func savedIn(t *testing.T, m *sandbox.Manager, id workspace.Identity, name, dump string) snapshot.Snapshot {
	t.Helper()
	st := snapshot.Store{Dir: filepath.Join(m.StateDir, "snapshots", id.Ref())}
	snap, err := st.Save(t.Context(), snapshot.Snapshot{Name: name, Repo: id.String(), PR: 482, SHA: "a3f91c2e4b7d", Scenario: "standard"},
		snapshot.One(func(_ context.Context, w io.Writer) error {
			_, err := io.WriteString(w, dump)
			return err
		}))
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

// TestSnapPromoteWritesTheScenario: the file and the scenario are
// there, and the scenario is one `pit --scenario` can select.
func TestSnapPromoteWritesTheScenario(t *testing.T) {
	m, _ := withManager(t)
	root, id := inRepo(t, promoteConfig)
	savedIn(t, m, id, "voucher", "-- dump\n")

	out, stderr, err := run(t, "snap", "promote", "voucher")
	if err != nil {
		t.Fatalf("promote: %v\n%s", err, stderr)
	}
	for _, want := range []string{"Wrote fixtures/voucher.sql (8 B).", `Added scenario "voucher" to .pit.yaml.`, "pit <pull request number> --scenario=voucher", "pit data reset <pull request number> --scenario=voucher"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	cfg, err := config.Load(filepath.Join(root, config.FileName))
	if err != nil {
		t.Fatalf("the edited file does not load: %v", err)
	}
	sc, _ := cfg.Scenario("voucher")
	if sc.Snapshot.File != "fixtures/voucher.sql" || sc.Description != "Saved in #482 at a3f91c2" {
		t.Errorf("scenario = %+v", sc)
	}
	edited, _ := os.ReadFile(filepath.Join(root, config.FileName))
	if !strings.Contains(string(edited), "  # save prints the dump, restore reads it back.\n") {
		t.Errorf("the comment went:\n%s", edited)
	}
}

func TestSnapPromoteAs(t *testing.T) {
	m, _ := withManager(t)
	root, id := inRepo(t, promoteConfig)
	snap := savedIn(t, m, id, "", "-- dump\n")

	_, _, err := run(t, "snap", "promote", snap.ID)
	if err == nil || !strings.Contains(errs.Hint(err), "--as") {
		t.Fatalf("a snapshot without a name: %v", err)
	}
	if _, _, err := run(t, "snap", "promote", snap.ID, "--as=expired-voucher", "--description=An expired voucher", "--dir=db"); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(filepath.Join(root, config.FileName))
	if err != nil {
		t.Fatal(err)
	}
	if sc, _ := cfg.Scenario("expired-voucher"); sc.Snapshot.File != "db/expired-voucher.sql" || sc.Description != "An expired voucher" {
		t.Errorf("scenario = %+v", sc)
	}
}

func TestSnapPromoteAgainAsksFirst(t *testing.T) {
	m, _ := withManager(t)
	root, id := inRepo(t, promoteConfig)
	savedIn(t, m, id, "voucher", "-- first\n")
	if _, _, err := run(t, "snap", "promote", "voucher"); err != nil {
		t.Fatal(err)
	}
	second := savedIn(t, m, id, "", "-- second\n")
	file := filepath.Join(root, "fixtures", "voucher.sql")

	out, err := runCLIWithInput(t, "n\n", "snap", "promote", second.ID, "--as=voucher")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `Scenario "voucher" loads fixtures/voucher.sql already.`) || !strings.Contains(out, "Nothing was written.") {
		t.Errorf("output:\n%s", out)
	}
	if got, _ := os.ReadFile(file); string(got) != "-- first\n" {
		t.Errorf("replaced without asking: %q", got)
	}

	out, _, err = run(t, "snap", "promote", second.ID, "--as=voucher", "--yes")
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(file); string(got) != "-- second\n" {
		t.Errorf("file = %q", got)
	}
	if !strings.Contains(out, ".pit.yaml is unchanged") {
		t.Errorf("output:\n%s", out)
	}
}

func TestSnapPromoteIntoAFileItCannotEdit(t *testing.T) {
	m, _ := withManager(t)
	root, id := inRepo(t, "{version: 1, web: {service: web, port: 80}, data: {snapshot: {save: a, restore: b}}}\n")
	savedIn(t, m, id, "voucher", "-- dump\n")

	out, _, err := run(t, "snap", "promote", "voucher")
	if err == nil || !strings.Contains(err.Error(), "cannot add the scenario") {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(out, "Add this under data.scenarios in .pit.yaml:\n\n    - name: voucher\n") {
		t.Errorf("output:\n%s", out)
	}
	if !strings.Contains(errs.Hint(err), "fixtures/voucher.sql is written") {
		t.Errorf("hint = %q", errs.Hint(err))
	}
	if _, err := os.Stat(filepath.Join(root, "fixtures", "voucher.sql")); err != nil {
		t.Error(err)
	}
}

func TestSnapPromoteJSON(t *testing.T) {
	m, _ := withManager(t)
	inRepo(t, promoteConfig)
	savedIn(t, m, id(t), "voucher", "-- dump\n")

	out, _, err := run(t, "snap", "promote", "voucher", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var got promoteJSON
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("%v:\n%s", err, out)
	}
	if got.Scenario != "voucher" || len(got.Files) != 1 || got.Size != 8 || !got.Added || got.Replaced {
		t.Errorf("got %+v", got)
	}
}

func id(t *testing.T) workspace.Identity {
	t.Helper()
	repo, err := currentRepo(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return repo.Identity
}

// A snapshot taken on a scenario the pull request brought keeps its
// example values, read from that pull request's file while its sandbox
// is there.
func TestSnapPromoteKeepsTheValuesOfThePullRequestsScenario(t *testing.T) {
	root, id := inRepo(t, promoteConfig)
	box := recorded(482, id.String(), id.Ref(), "refunds", 0)
	box.Worktree = t.TempDir()
	for name, content := range map[string]string{
		".git": "", "docker-compose.yml": snapCompose,
		config.FileName: "version: 1\nweb: {service: web, port: 80}\ndata:\n  scenarios:\n    - {name: refunded, params: {id: \"1002\"}}\n",
	} {
		if err := os.WriteFile(filepath.Join(box.Worktree, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	m, _ := withManager(t, box)
	st := snapshot.Store{Dir: filepath.Join(m.StateDir, "snapshots", id.Ref())}
	if _, err := st.Save(t.Context(), snapshot.Snapshot{Name: "refunded-order", PR: 482, SHA: "a3f91c2e4b7d", Scenario: "refunded"},
		snapshot.One(func(_ context.Context, w io.Writer) error { _, err := io.WriteString(w, "-- dump\n"); return err })); err != nil {
		t.Fatal(err)
	}

	if _, _, err := run(t, "snap", "promote", "refunded-order"); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(filepath.Join(root, config.FileName))
	if err != nil {
		t.Fatal(err)
	}
	if sc, _ := cfg.Scenario("refunded-order"); sc.Params["id"] != "1002" {
		t.Errorf("params = %v", sc.Params)
	}
}

func TestUpTakesAScenarioOrASnapshot(t *testing.T) {
	withManager(t)
	inRepo(t, promoteConfig)
	_, _, err := run(t, "482", "--scenario=standard", "--snapshot=voucher")
	if err == nil || !strings.Contains(err.Error(), "--scenario and --snapshot both say where the data comes from") {
		t.Errorf("err = %v", err)
	}
}

// A snapshot that does not exist is said before the pull request is
// even looked up.
func TestUpWithASnapshotThatDoesNotExist(t *testing.T) {
	m, _ := withManager(t)
	_, id := inRepo(t, promoteConfig)
	savedIn(t, m, id, "voucher", "-- dump\n")
	_, _, err := run(t, "482", "--snapshot=vocher")
	if err == nil || !strings.Contains(err.Error(), `there is no snapshot "vocher"`) || !strings.Contains(errs.Hint(err), "voucher") {
		t.Errorf("err = %v, hint %q", err, errs.Hint(err))
	}
}
