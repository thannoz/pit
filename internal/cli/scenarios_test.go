package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thannoz/pit/internal/proc"
)

// atConfiguredRepo makes the commands believe they were run in a
// repository that has the given .pit.yaml. It is enough for the
// commands that only read the file; anything that builds a sandbox
// needs a real repository.
func atConfiguredRepo(t *testing.T, pitYAML string) {
	t.Helper()

	root := t.TempDir()
	write(t, filepath.Join(root, "docker-compose.yml"), "services:\n  web:\n    image: nginx\n")
	write(t, filepath.Join(root, ".pit.yaml"), pitYAML)
	t.Chdir(root)
}

// TestScenariosListsNameDescriptionAndInheritance is the acceptance
// criterion for T-406.
func TestScenariosListsNameDescriptionAndInheritance(t *testing.T) {
	atConfiguredRepo(t, withScenarios)

	out, _, err := run(t, "scenarios")
	if err != nil {
		t.Fatalf("pit scenarios: %v", err)
	}

	for _, want := range []string{
		"NAME", "EXTENDS", "DESCRIPTION",
		"leer", "standard", "teilerstattung",
		"an order with a partial refund",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("listing is missing %q:\n%s", want, out)
		}
	}

	// Inheritance is the point of the EXTENDS column: teilerstattung
	// builds on standard, and the line has to say so.
	if !lineWith(out, "teilerstattung", "standard") {
		t.Errorf("the line for teilerstattung does not name what it extends:\n%s", out)
	}
}

func TestScenariosMarksTheDefault(t *testing.T) {
	// Which one a plain `pit <nr>` loads is the first thing a reader
	// wants from this table.
	atConfiguredRepo(t, withScenarios)

	out, _, err := run(t, "scenarios")
	if err != nil {
		t.Fatalf("pit scenarios: %v", err)
	}

	if !lineWith(out, "* ", "standard") {
		t.Errorf("the default is not marked:\n%s", out)
	}
	if lineWith(out, "* ", "teilerstattung") {
		t.Errorf("a scenario that is not the default is marked:\n%s", out)
	}
	if !strings.Contains(out, `loads "standard"`) {
		t.Errorf("the marker is never explained:\n%s", out)
	}
}

func TestScenariosOmitsTheExtendsColumnWhenNothingInherits(t *testing.T) {
	// The control for the column: with a flat list it would be a dash
	// on every line.
	atConfiguredRepo(t, `version: 1
web:
  service: web
  port: 3000
data:
  scenarios:
    - name: leer
      description: "nothing but the empty schema"
  default: leer
`)

	out, _, err := run(t, "scenarios")
	if err != nil {
		t.Fatalf("pit scenarios: %v", err)
	}
	if strings.Contains(out, "EXTENDS") {
		t.Errorf("the EXTENDS column is there although nothing inherits:\n%s", out)
	}
	if !strings.Contains(out, "leer") {
		t.Errorf("listing is missing the scenario:\n%s", out)
	}
}

func TestScenariosWithoutAny(t *testing.T) {
	atConfiguredRepo(t, "version: 1\nweb:\n  service: web\n  port: 3000\n")

	out, _, err := run(t, "scenarios")
	if err != nil {
		t.Fatalf("pit scenarios: %v", err)
	}
	if !strings.Contains(out, "No scenarios") {
		t.Errorf("output does not say there are none:\n%s", out)
	}
	if !strings.Contains(out, "data.scenarios") {
		t.Errorf("output does not say where they would go:\n%s", out)
	}
}

func TestScenariosWithoutADefault(t *testing.T) {
	// Saying nothing would leave the reader to conclude that the first
	// one wins, which is not true.
	atConfiguredRepo(t, `version: 1
web:
  service: web
  port: 3000
data:
  scenarios:
    - name: leer
    - name: standard
`)

	out, _, err := run(t, "scenarios")
	if err != nil {
		t.Fatalf("pit scenarios: %v", err)
	}
	if !strings.Contains(out, "No default is set") {
		t.Errorf("output does not say that no default is set:\n%s", out)
	}
}

func TestScenariosJSON(t *testing.T) {
	atConfiguredRepo(t, withScenarios)

	out, _, err := run(t, "scenarios", "--json")
	if err != nil {
		t.Fatalf("pit scenarios --json: %v", err)
	}

	var rows []struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Extends     string   `json:"extends"`
		Apply       []string `json:"apply"`
		Default     bool     `json:"default"`
	}
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}

	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3", len(rows))
	}
	if rows[2].Name != "teilerstattung" || rows[2].Extends != "standard" {
		t.Errorf("row 3 = %+v, want teilerstattung extending standard", rows[2])
	}
	if len(rows[2].Apply) != 1 {
		t.Errorf("row 3 carries %d commands, want the one it declares", len(rows[2].Apply))
	}
	if !rows[1].Default {
		t.Errorf("standard is not marked as the default: %+v", rows[1])
	}
}

// lineWith reports whether one line contains all the given parts,
// which is what an assertion about a table row is actually about.
func lineWith(out string, parts ...string) bool {
	for _, line := range strings.Split(out, "\n") {
		found := true
		for _, p := range parts {
			if !strings.Contains(line, p) {
				found = false
				break
			}
		}
		if found {
			return true
		}
	}
	return false
}

func TestScenariosFromASubdirectory(t *testing.T) {
	// A repository's scenarios belong to the repository, not to the
	// directory someone happens to stand in. The .git entry is what
	// stops the search, so the repository is part of what is tested.
	if testing.Short() {
		t.Skip("skipping: talks to the git binary")
	}
	root := t.TempDir()

	write(t, filepath.Join(root, "docker-compose.yml"), "services:\n  web:\n    image: nginx\n")
	write(t, filepath.Join(root, ".pit.yaml"), withScenarios)
	if err := os.MkdirAll(filepath.Join(root, "src", "web"), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	gitInit(t, root)
	t.Chdir(filepath.Join(root, "src", "web"))

	out, _, err := run(t, "scenarios")
	if err != nil {
		t.Fatalf("pit scenarios: %v", err)
	}
	if !strings.Contains(out, "teilerstattung") {
		t.Errorf("listing from a subdirectory is missing the scenarios:\n%s", out)
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func gitInit(t *testing.T, dir string) {
	t.Helper()

	_, err := proc.Exec{}.Output(t.Context(), proc.Command{
		Name: "git", Args: []string{"init", "-q"}, Dir: dir,
	})
	if err != nil {
		t.Fatalf("git init: %v", err)
	}
}
