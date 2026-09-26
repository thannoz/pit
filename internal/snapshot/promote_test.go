package snapshot

import (
	"os"
	"path/filepath"
	goruntime "runtime"
	"slices"
	"strings"
	"testing"

	"github.com/thannoz/pit/internal/config"
)

const promoteYAML = `version: 1

web:
  service: web
  port: 8080

data:
  migrate: ["compose exec -T db migrate"]
  scenarios:
    - name: standard
      description: "Three orders"   # the usual
      params:
        id: "1001"
  default: standard

  # save prints the dump, restore reads it back.
  snapshot:
    save: >-
      compose exec -T db pg_dump
    restore: compose exec -T db psql
`

// repository is a checkout with a .pit.yaml, and its path.
func repository(t *testing.T, yaml string, files map[string]string) (string, string) {
	t.Helper()
	root := t.TempDir()
	all := map[string]string{".git": "", "docker-compose.yml": "services:\n  web:\n    image: nginx\n", config.FileName: yaml}
	for k, v := range files {
		all[k] = v
	}
	for name, content := range all {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		// .pit.yaml writable by the group, to see that it stays so.
		mode := os.FileMode(0o644)
		if name == config.FileName {
			mode = 0o664
		}
		if err := os.WriteFile(path, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil { // past the umask
			t.Fatal(err)
		}
	}
	return root, filepath.Join(root, config.FileName)
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestPromotedSnapshotIsAScenario is the acceptance criterion for
// T-706, as far as files go: the dump is in the repository, the
// scenario that loads it is in .pit.yaml, and the file reads back as
// what it was plus that scenario.
func TestPromotedSnapshotIsAScenario(t *testing.T) {
	s := store(t)
	dump := "-- PostgreSQL database dump\nCOPY public.orders FROM stdin;\n"
	snap, err := s.Save(t.Context(), Snapshot{Name: "voucher", PR: 482, SHA: "1a2b3c4d5e", Scenario: "standard"}, One(writes(dump)))
	if err != nil {
		t.Fatal(err)
	}
	root, path := repository(t, promoteYAML, nil)

	plan, err := s.Plan(snap, path, PromoteOptions{Name: "voucher"})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Replacing || plan.Manual != nil || !slices.Equal(plan.Files, []string{"fixtures/voucher.sql"}) {
		t.Fatalf("plan = %+v", plan)
	}
	if _, err := os.Stat(filepath.Join(root, "fixtures")); !os.IsNotExist(err) {
		t.Error("Plan wrote something")
	}
	size, err := plan.Write()
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got := read(t, filepath.Join(root, "fixtures", "voucher.sql")); got != dump || size != int64(len(dump)) {
		t.Errorf("wrote %q, %d bytes", got, size)
	}
	info, err := os.Stat(filepath.Join(root, "fixtures", "voucher.sql"))
	if err != nil || (unixModes && info.Mode().Perm() != 0o644) {
		t.Errorf("mode = %v, %v", info.Mode(), err)
	}

	want := strings.Replace(promoteYAML, `        id: "1001"
`, `        id: "1001"
    - name: voucher
      description: "Saved in #482 at 1a2b3c4"
      snapshot: fixtures/voucher.sql
      params:
        id: "1001"
`, 1)
	if got := read(t, path); got != want {
		t.Errorf(".pit.yaml is now:\n%s\nwant:\n%s", got, want)
	}
	if info, err := os.Stat(path); err != nil || (unixModes && info.Mode().Perm() != 0o664) {
		t.Errorf(".pit.yaml's mode = %v, %v", info.Mode(), err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("the edited file does not load: %v", err)
	}
	sc, _ := cfg.Scenario("voucher")
	if restores, err := cfg.Restores(sc); err != nil || len(restores) != 1 {
		t.Errorf("restores = %+v, %v", restores, err)
	}
	if leftovers, _ := filepath.Glob(filepath.Join(root, "*", ".*")); len(leftovers) > 0 {
		t.Errorf("left behind %v", leftovers)
	}
}

func TestPromoteSeveralDatabases(t *testing.T) {
	s := store(t)
	snap, err := s.Save(t.Context(), Snapshot{Name: "two", PR: 7, SHA: "abc"}, []Dump{
		{Service: "orders", Write: writes("-- orders\n")},
		{Service: "stock", Write: writes("\x00\x01binary")},
	})
	if err != nil {
		t.Fatal(err)
	}
	yaml := `version: 1
web: {service: web, port: 80}
data:
  snapshot:
    - {service: orders, save: a, restore: b}
    - {service: stock, save: c, restore: d}
`
	root, path := repository(t, yaml, nil)
	plan, err := s.Plan(snap, path, PromoteOptions{Name: "two-warehouses", Dir: "db/states", Description: "Two warehouses"})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	want := []string{"db/states/two-warehouses.orders.sql", "db/states/two-warehouses.stock.dump"}
	if !slices.Equal(plan.Files, want) {
		t.Errorf("files = %v, want %v", plan.Files, want)
	}
	if _, err := plan.Write(); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(root, "db", "states", "two-warehouses.stock.dump")); got != "\x00\x01binary" {
		t.Errorf("stock = %q", got)
	}
	if got := read(t, path); !strings.HasSuffix(got, `  scenarios:
    - name: two-warehouses
      description: "Two warehouses"
      snapshot:
        orders: db/states/two-warehouses.orders.sql
        stock: db/states/two-warehouses.stock.dump
`) {
		t.Errorf(".pit.yaml is now:\n%s", got)
	}
	if _, err := config.Load(path); err != nil {
		t.Errorf("the edited file does not load: %v", err)
	}
}

// Promoting again is how a scenario that went stale is renewed: the
// files are replaced, .pit.yaml is not touched.
func TestPromoteAgainReplacesTheFiles(t *testing.T) {
	s := store(t)
	first, _ := s.Save(t.Context(), Snapshot{Name: "voucher", PR: 482, SHA: "abc"}, One(writes("-- first\n")))
	root, path := repository(t, promoteYAML, nil)
	plan, err := s.Plan(first, path, PromoteOptions{Name: "voucher"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plan.Write(); err != nil {
		t.Fatal(err)
	}
	edited := read(t, path)

	second, _ := s.Save(t.Context(), Snapshot{PR: 519, SHA: "def"}, One(writes("-- second\n")))
	plan, err = s.Plan(second, path, PromoteOptions{Name: "voucher"})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if !plan.Replacing || plan.Edited != nil || !slices.Equal(plan.Files, []string{"fixtures/voucher.sql"}) {
		t.Fatalf("plan = %+v", plan)
	}
	if _, err := plan.Write(); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(root, "fixtures", "voucher.sql")); got != "-- second\n" {
		t.Errorf("file = %q", got)
	}
	if read(t, path) != edited {
		t.Error(".pit.yaml changed")
	}
}

func TestPromoteRefuses(t *testing.T) {
	s := store(t)
	snap, _ := s.Save(t.Context(), Snapshot{Name: "voucher", PR: 482, SHA: "abc"}, One(writes("-- dump\n")))
	two, _ := s.Save(t.Context(), Snapshot{Name: "two", PR: 482, SHA: "abc"}, []Dump{
		{Service: "db", Write: writes("a")}, {Service: "other", Write: writes("b")},
	})
	withoutCommands := "version: 1\nweb: {service: web, port: 80}\n"
	for name, tc := range map[string]struct {
		yaml  string
		files map[string]string
		snap  Snapshot
		o     PromoteOptions
		want  string
	}{
		"a scenario written by someone": {promoteYAML, nil, snap, PromoteOptions{Name: "standard"},
			`scenario "standard" exists already and loads its data with commands of its own`},
		"a file in the way": {promoteYAML, map[string]string{"fixtures/voucher.sql": "mine"}, snap, PromoteOptions{Name: "voucher"},
			"fixtures/voucher.sql exists already"},
		"no snapshot commands":   {withoutCommands, nil, snap, PromoteOptions{Name: "voucher"}, "has no data.snapshot"},
		"a name that is no name": {promoteYAML, nil, snap, PromoteOptions{Name: "Voucher Case"}, "cannot be the name of a promoted scenario"},
		"outside":                {promoteYAML, nil, snap, PromoteOptions{Name: "voucher", Dir: "../elsewhere"}, "outside the repository"},
		"two databases, one command": {promoteYAML, nil, two, PromoteOptions{Name: "two"},
			"cannot be loaded with the snapshot commands"},
	} {
		t.Run(name, func(t *testing.T) {
			root, path := repository(t, tc.yaml, tc.files)
			before := read(t, path)
			_, err := s.Plan(tc.snap, path, tc.o)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want one saying %s", err, tc.want)
			}
			if read(t, path) != before {
				t.Error(".pit.yaml changed")
			}
			if tc.files == nil {
				if _, err := os.Stat(filepath.Join(root, "fixtures")); !os.IsNotExist(err) {
					t.Error("something was written")
				}
			}
		})
	}
}

func TestPromoteAgainWithOtherParts(t *testing.T) {
	s := store(t)
	two, _ := s.Save(t.Context(), Snapshot{Name: "two", PR: 482, SHA: "abc"}, []Dump{
		{Service: "db", Write: writes("a")}, {Service: "other", Write: writes("b")},
	})
	yaml := `version: 1
web: {service: web, port: 80}
data:
  snapshot:
    - {service: db, save: a, restore: b}
    - {service: other, save: c, restore: d}
    - {service: third, save: e, restore: f}
  scenarios:
    - {name: one, snapshot: {db: a.sql}}
    - {name: two, snapshot: {db: a.sql, third: c.sql}}
`
	_, path := repository(t, yaml, map[string]string{"a.sql": "", "c.sql": ""})
	if _, err := s.Plan(two, path, PromoteOptions{Name: "one"}); err == nil || !strings.Contains(err.Error(), "loads 1 file, but the snapshot holds 2") {
		t.Errorf("err = %v", err)
	}
	if _, err := s.Plan(two, path, PromoteOptions{Name: "two"}); err == nil || !strings.Contains(err.Error(), "loads no file into other") {
		t.Errorf("err = %v", err)
	}
}

// A .pit.yaml pit cannot edit safely still gets the files, and the
// lines to add by hand.
func TestPromoteIntoAFileItCannotEdit(t *testing.T) {
	s := store(t)
	snap, _ := s.Save(t.Context(), Snapshot{Name: "voucher", PR: 482, SHA: "abc"}, One(writes("-- dump\n")))
	yaml := `{version: 1, web: {service: web, port: 80}, data: {snapshot: {save: a, restore: b}}}` + "\n"
	root, path := repository(t, yaml, nil)
	plan, err := s.Plan(snap, path, PromoteOptions{Name: "voucher"})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Manual == nil || plan.Edited != nil {
		t.Fatalf("plan = %+v", plan)
	}
	if _, err := plan.Write(); err != nil {
		t.Fatal(err)
	}
	if read(t, path) != yaml {
		t.Error(".pit.yaml changed")
	}
	if _, err := os.Stat(filepath.Join(root, "fixtures", "voucher.sql")); err != nil {
		t.Error(err)
	}
}

// Nothing is put in place until every part is complete.
func TestPromoteWritesAllOrNothing(t *testing.T) {
	s := store(t)
	snap, _ := s.Save(t.Context(), Snapshot{Name: "two", PR: 7, SHA: "abc"}, []Dump{
		{Service: "orders", Write: writes("-- orders\n")},
		{Service: "stock", Write: writes("-- stock\n")},
	})
	yaml := "version: 1\nweb: {service: web, port: 80}\ndata:\n  snapshot:\n    - {service: orders, save: a, restore: b}\n    - {service: stock, save: c, restore: d}\n"
	root, path := repository(t, yaml, nil)
	plan, err := s.Plan(snap, path, PromoteOptions{Name: "two"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(s.partPath(snap.ID, "stock")); err != nil {
		t.Fatal(err)
	}
	if _, err := plan.Write(); err == nil {
		t.Fatal("wrote a snapshot with a part missing")
	}
	entries, _ := os.ReadDir(filepath.Join(root, "fixtures"))
	if len(entries) != 0 {
		t.Errorf("left %v", entries)
	}
	if read(t, path) != yaml {
		t.Error(".pit.yaml changed")
	}
}

func TestExtensionOf(t *testing.T) {
	for head, want := range map[string]string{
		"-- PostgreSQL database dump\n": ".sql",
		"PGDMP\x01\x0e\x01":             ".dump",
		"\x6d\xe2\x99\x81\x01\x00":      ".archive",
		"\x1f\x8b\x08\x00":              ".gz",
		"":                              ".sql",
		"\x00\x00binary":                ".dump",
		"INSERT 'Gr\xc3":                ".sql", // the cut went through an ü
		"\xff\xfe not text":             ".dump",
	} {
		if got := extensionOf([]byte(head)); got != want {
			t.Errorf("extensionOf(%q) = %s, want %s", head, got, want)
		}
	}
}

// The values given win over the file's: they come from the file the
// snapshot's sandbox was built with.
func TestPromoteTakesTheParamsItIsGiven(t *testing.T) {
	s := store(t)
	snap, _ := s.Save(t.Context(), Snapshot{Name: "refunded-order", PR: 7, SHA: "abc", Scenario: "standard"}, One(writes("-- dump\n")))
	_, path := repository(t, promoteYAML, nil)
	plan, err := s.Plan(snap, path, PromoteOptions{Name: "refunded-order", Params: map[string]string{"id": "1002"}})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Scenario.Params["id"] != "1002" {
		t.Errorf("params = %v", plan.Scenario.Params)
	}
}

// unixModes says the system keeps a file's permission bits; Windows
// knows read-only and nothing else.
var unixModes = goruntime.GOOS != "windows"
