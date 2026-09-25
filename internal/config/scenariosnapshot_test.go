package config

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const oneDatabase = `version: 1
web:
  service: web
  port: 3000
data:
  migrate: ["compose exec -T db migrate"]
  snapshot:
    save: "compose exec -T db pg_dump"
    restore: "compose exec -T db psql"
  scenarios:
`

const twoDatabases = `version: 1
web:
  service: web
  port: 3000
data:
  snapshot:
    - {service: orders, save: "a", restore: "restore orders"}
    - {service: stock, save: "c", restore: "restore stock"}
  scenarios:
`

func loadWith(t *testing.T, src string, files ...string) (*Config, error) {
	t.Helper()
	all := map[string]string{"docker-compose.yml": "services:\n  web:\n    image: web\n", FileName: src}
	for _, f := range files {
		all[f] = "-- dump\n"
	}
	root := project(t, all)
	return Load(filepath.Join(root, FileName))
}

func TestScenarioSnapshotForms(t *testing.T) {
	_, err := loadWith(t, twoDatabases+`    - name: plain
      snapshot: fixtures/plain.sql
    - name: split
      snapshot:
        stock: fixtures/split.stock.sql
        orders: fixtures/split.orders.sql
    - name: none
`, "fixtures/plain.sql", "fixtures/split.stock.sql", "fixtures/split.orders.sql")
	if err == nil || !strings.Contains(err.Error(), "loads one file, but data.snapshot restores 2 databases") {
		t.Fatalf("a single file for two databases: err = %v", err)
	}

	c, err := loadWith(t, twoDatabases+`    - name: split
      snapshot:
        stock: fixtures/split.stock.sql
        orders: fixtures/split.orders.sql
    - name: none
`, "fixtures/split.stock.sql", "fixtures/split.orders.sql")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	split, _ := c.Scenario("split")
	// In the order written, which is the order they are loaded in.
	want := []SnapshotFile{{"stock", "fixtures/split.stock.sql"}, {"orders", "fixtures/split.orders.sql"}}
	if !reflect.DeepEqual(split.Snapshot.Each(), want) {
		t.Errorf("Each() = %+v", split.Snapshot.Each())
	}
	restores, err := c.Restores(split)
	if err != nil {
		t.Fatal(err)
	}
	if len(restores) != 2 || restores[0].Command != "restore stock" || restores[1].Command != "restore orders" {
		t.Errorf("restores = %+v", restores)
	}
	// Relative to the file, wherever pit was run from.
	if !filepath.IsAbs(restores[0].File) || restores[0].File != filepath.Join(c.Dir, "fixtures", "split.stock.sql") {
		t.Errorf("file = %s, dir = %s", restores[0].File, c.Dir)
	}
	none, _ := c.Scenario("none")
	if r, err := c.Restores(none); r != nil || err != nil {
		t.Errorf("a scenario without a snapshot restores %v, %v", r, err)
	}

	// Written back as it was read, so that comparing two files
	// compares what their authors wrote.
	out, err := yaml.Marshal(c.Data.Scenarios)
	if err != nil || !strings.Contains(string(out), "snapshot:\n    stock: fixtures/split.stock.sql\n    orders:") {
		t.Errorf("marshalled:\n%s%v", out, err)
	}
	if d := Differences(c, c); len(d) != 0 {
		t.Errorf("a file differs from itself in %v", d)
	}
}

func TestScenarioSnapshotOfOneDatabase(t *testing.T) {
	c, err := loadWith(t, oneDatabase+"    - name: voucher\n      snapshot: fixtures/voucher.sql\n", "fixtures/voucher.sql")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	sc, _ := c.Scenario("voucher")
	restores, err := c.Restores(sc)
	if err != nil || len(restores) != 1 || restores[0].Command != "compose exec -T db psql" || restores[0].Service != "" {
		t.Errorf("restores = %+v, %v", restores, err)
	}
}

func TestScenarioSnapshotProblems(t *testing.T) {
	for name, tc := range map[string]struct {
		src   string
		files []string
		want  string
	}{
		"missing file": {oneDatabase + "    - name: v\n      snapshot: fixtures/v.sql\n", nil,
			"data.scenarios[0].snapshot: names fixtures/v.sql, which does not exist"},
		"absolute": {oneDatabase + "    - name: v\n      snapshot: /tmp/v.sql\n", nil, "an absolute path"},
		"outside":  {oneDatabase + "    - name: v\n      snapshot: ../v.sql\n", nil, "outside the repository"},
		"empty":    {oneDatabase + "    - name: v\n      snapshot: {db: \"\"}\n", nil, "names an empty path"},
		"extends": {oneDatabase + "    - name: base\n    - name: v\n      extends: base\n      snapshot: v.sql\n", []string{"v.sql"},
			"data.scenarios[1].extends: is set, but a snapshot replaces all the data"},
		"no commands": {"version: 1\nweb: {service: web, port: 80}\ndata:\n  scenarios:\n    - name: v\n      snapshot: v.sql\n", []string{"v.sql"},
			"data.snapshot has no restore command"},
		"unknown service": {twoDatabases + "    - name: v\n      snapshot: {orders: o.sql, audit: a.sql}\n", []string{"o.sql", "a.sql"},
			"loads a file into audit, which data.snapshot has no restore command for"},
		"a list":         {oneDatabase + "    - name: v\n      snapshot: [v.sql]\n", nil, "a path, or a mapping"},
		"a nested value": {twoDatabases + "    - name: v\n      snapshot: {orders: [o.sql]}\n", nil, `the snapshot of "orders" is not a path`},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := loadWith(t, tc.src, tc.files...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want one saying %s", err, tc.want)
			}
		})
	}
}

// A scenario that builds on a promoted one is how a snapshot is
// refined; only the other way round is refused.
func TestAScenarioMayExtendAPromotedOne(t *testing.T) {
	_, err := loadWith(t, oneDatabase+"    - name: v\n      snapshot: v.sql\n    - name: more\n      extends: v\n      apply: [\"compose exec -T db psql -c 'select 1'\"]\n", "v.sql")
	if err != nil {
		t.Errorf("Load: %v", err)
	}
}
