package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// dataExample loads the block mirrored from docs/03-datenkonzept.md.
func dataExample(t *testing.T) []byte {
	t.Helper()

	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "config", "data.yaml"))
	if err != nil {
		t.Fatalf("reading the data example: %v", err)
	}
	return data
}

// TestDataExampleParsesAndValidates is the acceptance criterion for
// T-401: what the data concept promises has to be what the code takes.
func TestDataExampleParsesAndValidates(t *testing.T) {
	root := project(t, map[string]string{FileName: string(dataExample(t))})

	c, err := Load(filepath.Join(root, FileName))
	if err != nil {
		t.Fatalf("the documented data block does not validate: %v", err)
	}

	t.Run("service", func(t *testing.T) {
		if c.Data.Service != "db" {
			t.Errorf("Service = %q, want db", c.Data.Service)
		}
	})

	t.Run("scenarios in the author's order", func(t *testing.T) {
		want := []string{"leer", "standard", "teilerstattung"}
		if got := c.ScenarioNames(); !slices.Equal(got, want) {
			t.Errorf("ScenarioNames() = %v, want %v", got, want)
		}
	})

	t.Run("a scenario with nothing to apply", func(t *testing.T) {
		// "leer" is migrations and no data. It is a scenario even
		// though it does nothing, and refusing it would mean there is
		// no way to ask for an empty database.
		s, ok := c.Scenario("leer")
		if !ok {
			t.Fatal("the scenario is missing")
		}
		if len(s.Apply) != 0 {
			t.Errorf("Apply = %v, want nothing", s.Apply)
		}
		if s.Description == "" {
			t.Error("Description is empty")
		}
	})

	t.Run("inheritance", func(t *testing.T) {
		s, _ := c.Scenario("teilerstattung")
		if s.Extends != "standard" {
			t.Errorf("Extends = %q, want standard", s.Extends)
		}
	})

	t.Run("default", func(t *testing.T) {
		if c.Data.Default != "standard" {
			t.Errorf("Default = %q, want standard", c.Data.Default)
		}
	})

	t.Run("snapshot", func(t *testing.T) {
		// Two commands are all pit needs for any database: one that
		// writes a dump to stdout, one that reads it from stdin.
		if c.Data.Snapshot.Save == "" || c.Data.Snapshot.Restore == "" {
			t.Errorf("Snapshot = %+v, want both commands", c.Data.Snapshot)
		}
	})

	t.Run("production-like dump", func(t *testing.T) {
		if c.Data.ProductionLike.Fetch == "" {
			t.Error("Fetch is empty")
		}
		if c.Data.ProductionLike.TTL.Duration() != 24*time.Hour {
			t.Errorf("TTL = %v, want 24h", c.Data.ProductionLike.TTL)
		}
	})
}

func TestADataBlockIsOptional(t *testing.T) {
	// Most projects start without one, and pit has to work for them.
	root := project(t, map[string]string{FileName: minimal})

	c, err := Load(filepath.Join(root, FileName))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(c.Data.Scenarios) != 0 {
		t.Errorf("Scenarios = %v, want none", c.Data.Scenarios)
	}
}

func TestScenarioLookupIsExact(t *testing.T) {
	c, err := Parse(dataExample(t))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if _, ok := c.Scenario("stand"); ok {
		t.Error("a prefix matched a scenario name")
	}
	if _, ok := c.Scenario("STANDARD"); ok {
		t.Error("scenario names are matched without regard to case")
	}
}

// TestABrokenCommandIsCaughtWhileReadingTheFile records why config
// asks hooks whether a line can be run: an unbalanced quote is valid
// YAML, and finding it halfway through a setup means a worktree is
// already made and containers are already started.
func TestABrokenCommandIsCaughtWhileReadingTheFile(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want string
	}{
		{
			name: "an unbalanced quote in a seed",
			yaml: "web:\n  service: web\n  port: 3000\ndata:\n  scenarios:\n    - name: standard\n      apply: [\"psql -c \\\"select 1\"]\n",
			want: "unbalanced",
		},
		{
			name: "an empty seed command",
			yaml: "web:\n  service: web\n  port: 3000\ndata:\n  scenarios:\n    - name: standard\n      apply: [\"\"]\n",
			want: "nothing to run",
		},
		{
			name: "an unbalanced quote in a hook",
			yaml: "web:\n  service: web\n  port: 3000\nhooks:\n  after_up:\n    - \"npm run 'migrate\"\n",
			want: "unbalanced",
		},
		{
			name: "an empty hook",
			yaml: "web:\n  service: web\n  port: 3000\nhooks:\n  after_up:\n    - \"\"\n",
			want: "nothing to run",
		},
		{
			name: "a broken snapshot command",
			yaml: "web:\n  service: web\n  port: 3000\ndata:\n  snapshot:\n    save: \"pg_dump 'app\"\n    restore: \"psql\"\n",
			want: "unbalanced",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := loadBroken(t, tt.yaml, nil)
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("message does not explain the problem:\n%s\nwant it to contain %q", err, tt.want)
			}
			if !hasLineNumber.MatchString(err.Error()) {
				t.Errorf("message has no line number:\n%s", err)
			}
		})
	}
}

// TestARetentionTimeWithNothingToRetain covers a setting that does
// nothing, which is worse than a missing one: the author believes they
// configured something.
func TestARetentionTimeWithNothingToRetain(t *testing.T) {
	err := loadBroken(t, "web:\n  service: web\n  port: 3000\ndata:\n  production_like:\n    ttl: 24h\n", nil)

	if !strings.Contains(err.Error(), "no fetch command") {
		t.Errorf("message = %q, want it to say what is missing", err)
	}
}

func TestAFetchWithoutATTLIsFine(t *testing.T) {
	// The other way round has a sensible default, so it is not a
	// problem to report.
	root := project(t, map[string]string{
		FileName: "web:\n  service: web\n  port: 3000\ndata:\n  production_like:\n    fetch: \"cat dump.sql\"\n",
	})

	if _, err := Load(filepath.Join(root, FileName)); err != nil {
		t.Errorf("a fetch without a ttl was refused: %v", err)
	}
}

func TestTheDocumentedCommandsAllParse(t *testing.T) {
	// Every command in the data concept has to survive the check that
	// now runs over it.
	root := project(t, map[string]string{FileName: string(dataExample(t))})

	if _, err := Load(filepath.Join(root, FileName)); err != nil {
		t.Errorf("a documented command does not pass the check: %v", err)
	}
}
