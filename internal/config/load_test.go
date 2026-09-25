package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thannoz/pit/internal/errs"
)

// project builds a directory that looks like a repository: a .git
// marker, a compose file, and whatever else the test writes into it.
func project(t *testing.T, files map[string]string) string {
	t.Helper()

	root := t.TempDir()
	write(t, filepath.Join(root, ".git"), "")
	write(t, filepath.Join(root, "docker-compose.yml"), "services:\n  web:\n    image: nginx\n")
	for name, content := range files {
		write(t, filepath.Join(root, name), content)
	}
	return root
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile(%q): %v", path, err)
	}
}

const minimal = "web:\n  service: web\n  port: 3000\n"

// TestFindFromAnySubdirectory is the acceptance criterion for T-202.
func TestFindFromAnySubdirectory(t *testing.T) {
	root := project(t, map[string]string{
		FileName:                    minimal,
		"internal/deep/nested/keep": "",
	})

	for _, from := range []string{".", "internal", "internal/deep", "internal/deep/nested"} {
		t.Run(from, func(t *testing.T) {
			got, err := Find(filepath.Join(root, from))
			if err != nil {
				t.Fatalf("Find: %v", err)
			}
			if filepath.Base(got) != FileName {
				t.Errorf("Find() = %q, want a %s", got, FileName)
			}
			if !strings.HasPrefix(got, root) {
				t.Errorf("Find() = %q, want it under %q", got, root)
			}
		})
	}
}

// TestFindStopsAtTheRepositoryRoot guards against picking up a
// stranger's configuration: a .pit.yaml above the repository belongs to
// a different project.
func TestFindStopsAtTheRepositoryRoot(t *testing.T) {
	outer := t.TempDir()
	write(t, filepath.Join(outer, FileName), minimal)

	inner := filepath.Join(outer, "repo")
	write(t, filepath.Join(inner, ".git"), "")
	write(t, filepath.Join(inner, "src", "keep"), "")

	_, err := Find(filepath.Join(inner, "src"))
	if err == nil {
		t.Fatal("Find walked past the repository root and used the outer file")
	}
	if errs.Hint(err) == "" {
		t.Error("the error carries no hint")
	}
}

func TestFindReportsWhenThereIsNoConfiguration(t *testing.T) {
	root := project(t, nil)

	_, err := Find(root)
	if err == nil {
		t.Fatal("want an error when there is no configuration")
	}
	if !strings.Contains(err.Error(), FileName) {
		t.Errorf("error = %q, want it to name the file it looked for", err)
	}
	if !strings.Contains(errs.Hint(err), "pit init") {
		t.Errorf("hint = %q, want it to point at pit init", errs.Hint(err))
	}
}

func TestLoadFromAppliesDefaults(t *testing.T) {
	root := project(t, map[string]string{FileName: minimal})

	c, path, err := LoadFrom(root)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if filepath.Base(path) != FileName {
		t.Errorf("path = %q", path)
	}
	if c.Healthcheck.ExpectStatus != DefaultExpectStatus {
		t.Errorf("defaults were not applied: %+v", c.Healthcheck)
	}
}

func TestLoadAcceptsTheCanonicalExample(t *testing.T) {
	// The documented example must not merely parse; it must also pass
	// validation, or the documentation is teaching a broken file.
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "config", "full.yaml"))
	if err != nil {
		t.Fatalf("reading the canonical example: %v", err)
	}
	root := project(t, map[string]string{
		FileName:                 string(data),
		"docker-compose.pit.yml": "services: {}\n",
		".env.pit.example":       "NODE_ENV=development\n",
	})

	if _, err := Load(filepath.Join(root, FileName)); err != nil {
		t.Fatalf("the documented example does not validate: %v", err)
	}
}

func TestLoadReportsAMissingFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), FileName))
	if err == nil {
		t.Fatal("want an error for a file that is not there")
	}
	if errs.Hint(err) == "" {
		t.Error("the error carries no hint")
	}
}

// Without compose.files, the file is the one Docker Compose would take:
// compose.yaml before docker-compose.yml, as Compose looks.
func TestTheComposeFileIsTheOneComposeWouldTake(t *testing.T) {
	for _, tc := range []struct {
		files []string
		want  string
	}{
		{[]string{"compose.yaml"}, "compose.yaml"},
		{[]string{"compose.yml", "docker-compose.yaml"}, "compose.yml"},
		{[]string{"docker-compose.yaml"}, "docker-compose.yaml"},
	} {
		root := t.TempDir()
		write(t, filepath.Join(root, ".git"), "")
		for _, f := range tc.files {
			write(t, filepath.Join(root, f), "services:\n  web:\n    image: nginx\n")
		}
		write(t, filepath.Join(root, FileName), "web:\n  service: web\n  port: 80\n")
		c, err := Load(filepath.Join(root, FileName))
		if err != nil {
			t.Fatalf("%v: %v", tc.files, err)
		}
		if len(c.Compose.Files) != 1 || c.Compose.Files[0] != tc.want {
			t.Errorf("%v: files %v, want %s", tc.files, c.Compose.Files, tc.want)
		}
	}
	// Named, it is what was named.
	root := project(t, map[string]string{"compose.yaml": "services:\n  web:\n    image: nginx\n",
		FileName: "compose:\n  files: [docker-compose.yml]\nweb:\n  service: web\n  port: 80\n"})
	if c, err := Load(filepath.Join(root, FileName)); err != nil || c.Compose.Files[0] != "docker-compose.yml" {
		t.Errorf("named: %v, %v", c, err)
	}
}
