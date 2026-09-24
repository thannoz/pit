package config

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/hooks"
)

func TestEngineOf(t *testing.T) {
	for image, want := range map[string]Engine{
		"postgres":                            Postgres,
		"postgres:17-alpine":                  Postgres,
		"docker.io/library/postgres:16":       Postgres,
		"postgis/postgis:16-3.4":              Postgres,
		"pgvector/pgvector:pg17":              Postgres,
		"timescale/timescaledb:latest-pg16":   Postgres,
		"postgres@sha256:0123abcd":            Postgres,
		"mysql:8.4":                           MySQL,
		"mariadb:11":                          MariaDB,
		"MariaDB:11":                          MariaDB,
		"mongo:8":                             MongoDB,
		"ghcr.io/acme/postgres-backup:1":      "",
		"redis:7":                             "",
		"bitnami/postgresql:17":               "",
		"":                                    "",
		"registry.example.com:5000/mysql:8.0": MySQL,
	} {
		got, ok := EngineOf(image)
		if got != want || ok != (want != "") {
			t.Errorf("EngineOf(%q) = %q, %v; want %q", image, got, ok, want)
		}
	}
}

func TestFindDatabase(t *testing.T) {
	services := []Database{{"web", ""}, {"cache", "redis:7"}, {"store", "postgres:17"}}

	if db, ok := FindDatabase("", services); !ok || db.Service != "store" {
		t.Errorf("by image: %v, %v; want store", db, ok)
	}
	// data.service decides, even over a service that looks more like
	// a database.
	if db, ok := FindDatabase("cache", services); !ok || db.Service != "cache" {
		t.Errorf("configured: %v, %v; want cache", db, ok)
	}
	// Named but not in the compose file: the name is still the
	// author's, only nothing is known about it.
	if db, ok := FindDatabase("elsewhere", services); !ok || db != (Database{Service: "elsewhere"}) {
		t.Errorf("configured, missing: %v, %v", db, ok)
	}
	// Two databases, and nothing says which: no guess.
	two := append(slices.Clone(services), Database{"analytics", "mysql:8.4"})
	if db, ok := FindDatabase("", two); ok {
		t.Errorf("two databases: got %v, want no guess", db)
	}
	if db, ok := FindDatabase("", services[:2]); ok {
		t.Errorf("no database: got %v", db)
	}
}

// The commands the hint suggests are the ones .pit.yaml ends up with
// when its lines are pasted in: the folded YAML keeps both kinds of
// quote as they are, and pit can run what it parses.
func TestSuggestedSnapshotRoundTrips(t *testing.T) {
	for _, e := range []Engine{Postgres, MySQL, MariaDB, MongoDB} {
		t.Run(string(e), func(t *testing.T) {
			want := SuggestSnapshot("db", e)
			if !want.Configured() {
				t.Fatalf("no commands for %s", e)
			}
			src := "version: 1\nweb:\n  service: web\n  port: 3000\n" + dedent(snapshotYAML(want)) + "\n"
			root := project(t, map[string]string{
				FileName:             src,
				"docker-compose.yml": "services:\n  web:\n    image: web\n  db:\n    image: db\n",
			})
			c, err := Load(filepath.Join(root, FileName))
			if err != nil {
				t.Fatalf("Load:\n%s\n%v", src, err)
			}
			if c.Data.Snapshot != want {
				t.Errorf("pasted\n got %#v\nwant %#v", c.Data.Snapshot, want)
			}
			for _, line := range []string{want.Save, want.Restore} {
				if err := hooks.Check(line); err != nil {
					t.Errorf("%q: %v", line, err)
				}
				cmd, err := hooks.Expand(line, hooks.Sandbox{Project: "p"})
				if err != nil {
					t.Fatal(err)
				}
				// Everything after sh -c is one argument: the script
				// the container's shell runs, variables and all.
				args := cmd.Args
				if len(args) < 2 || args[len(args)-2] != "-c" || !strings.HasPrefix(args[len(args)-1], "exec ") && !strings.HasPrefix(args[len(args)-1], "export ") {
					t.Errorf("%q expands to %q", line, args)
				}
			}
		})
	}
}

// dedent takes the two spaces off that the hint indents its lines by.
func dedent(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimPrefix(l, "  ")
	}
	return strings.Join(lines, "\n")
}

func TestSnapshotCommands(t *testing.T) {
	t.Run("configured", func(t *testing.T) {
		c := &Config{Data: Data{Snapshot: Snapshot{Save: "a", Restore: "b"}}}
		got, err := c.SnapshotCommands(nil)
		if err != nil || got != c.Data.Snapshot {
			t.Errorf("= %v, %v", got, err)
		}
	})

	// The acceptance criterion of T-701: missing commands are a hint,
	// not a crash, and the hint is the lines to add.
	t.Run("missing, database recognised", func(t *testing.T) {
		c := &Config{}
		_, err := c.SnapshotCommands([]Database{{"web", ""}, {"pg", "postgres:17-alpine"}})
		if err == nil {
			t.Fatal("no error")
		}
		if !strings.Contains(err.Error(), ".pit.yaml does not say how to save") {
			t.Errorf("error = %q", err)
		}
		hint := errs.Hint(err)
		for _, want := range []string{"pg is PostgreSQL", "compose exec -T pg sh -c", "pg_dump", "psql", "save: >-", "restore: >-"} {
			if !strings.Contains(hint, want) {
				t.Errorf("hint lacks %q:\n%s", want, hint)
			}
		}
	})

	t.Run("missing, database unknown", func(t *testing.T) {
		c := &Config{Data: Data{Service: "store"}}
		_, err := c.SnapshotCommands([]Database{{"store", ""}})
		hint := errs.Hint(err)
		for _, want := range []string{"stdout", "stdin", "For PostgreSQL", "compose exec -T store"} {
			if !strings.Contains(hint, want) {
				t.Errorf("hint lacks %q:\n%s", want, hint)
			}
		}
		if strings.Contains(hint, "store is") {
			t.Errorf("hint claims to know the database:\n%s", hint)
		}
	})
}

// The example configuration still parses with its snapshot block, and
// says it is configured.
func TestTheExampleSnapshotIsConfigured(t *testing.T) {
	root := project(t, map[string]string{FileName: string(dataExample(t))})
	c, err := Load(filepath.Join(root, FileName))
	if err != nil {
		t.Fatal(err)
	}
	if !c.Data.Snapshot.Configured() {
		t.Errorf("Snapshot = %+v", c.Data.Snapshot)
	}
}

// MySQL 9 records GTIDs by default, and a dump that carries them cannot
// be read back into the server it came from: ERROR 3546, found by
// restoring into a real mysql:9 container. mysql:8.4 did not show it.
func TestMySQLDumpsLeaveGTIDsOut(t *testing.T) {
	if s := SuggestSnapshot("db", MySQL); !strings.Contains(s.Save, "--set-gtid-purged=OFF") {
		t.Errorf("save = %q", s.Save)
	}
}

// Half a configuration is none; validation refuses it, and a Config
// that did not come through validation must not slip past either.
func TestHalfASnapshotIsNotConfigured(t *testing.T) {
	for _, s := range []Snapshot{{Save: "x"}, {Restore: "y"}, {}} {
		if s.Configured() {
			t.Errorf("%+v counts as configured", s)
		}
	}
}

// Each command works whether or not the compose file sets the image's
// variables: unset, they fall back to what the image itself does. The
// live round trips covered postgres with only a password set, mariadb
// with the MYSQL_ spellings, and mongo with and without a root user.
func TestSuggestedCommandsFallBackToTheImageDefaults(t *testing.T) {
	for e, want := range map[Engine][]string{
		Postgres: {`"${POSTGRES_USER:-postgres}"`, `"${POSTGRES_DB:-${POSTGRES_USER:-postgres}}"`},
		MariaDB:  {`${MARIADB_ROOT_PASSWORD:-$MYSQL_ROOT_PASSWORD}`, `${MARIADB_DATABASE:-$MYSQL_DATABASE}`},
		MongoDB:  {`${MONGO_INITDB_ROOT_USERNAME:+--username`},
	} {
		s := SuggestSnapshot("db", e)
		for _, w := range want {
			if !strings.Contains(s.Save, w) {
				t.Errorf("%s: save %q lacks %s", e, s.Save, w)
			}
		}
		// Restore logs in the same way; it needs no database name
		// where the dump names its own.
		if !strings.Contains(s.Restore, want[0]) {
			t.Errorf("%s: restore %q lacks %s", e, s.Restore, want[0])
		}
	}
}
