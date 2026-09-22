package config

import (
	"os"
	"path/filepath"

	"github.com/thannoz/pit/internal/errs"
)

// FileName is what pit looks for in a repository.
const FileName = ".pit.yaml"

// Find walks up from startDir looking for .pit.yaml, stopping at the
// repository root. It searches the filesystem rather than asking git,
// so it works in a directory that was never a repository and needs no
// subprocess.
func Find(startDir string) (string, error) {
	dir, err := filepath.Abs(startDir)
	if err != nil {
		return "", errs.Wrap(err, "cannot resolve %s", startDir)
	}

	for {
		candidate := filepath.Join(dir, FileName)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}

		// A .git entry marks the top of the repository. Searching past
		// it would find a stranger's configuration -- a .pit.yaml in a
		// parent directory belongs to a different project.
		if isRepoRoot(dir) {
			return "", notFound(dir)
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return "", notFound(dir)
		}
		dir = parent
	}
}

// Load reads and validates the configuration at path.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, errs.Wrap(err, "cannot read %s", path).
			WithHint("check that the file exists and is readable")
	}

	c, node, err := parseStrict(data, path)
	if err != nil {
		return nil, err
	}

	if err := c.validate(node, filepath.Dir(path), path); err != nil {
		return nil, err
	}
	return c, nil
}

// LoadFrom finds the configuration from startDir and loads it, returning
// the path it used.
func LoadFrom(startDir string) (*Config, string, error) {
	path, err := Find(startDir)
	if err != nil {
		return nil, "", err
	}
	c, err := Load(path)
	return c, path, err
}

// isRepoRoot reports whether dir holds a .git entry. It is a file in a
// worktree and a directory in a normal clone, so only existence counts.
func isRepoRoot(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}

func notFound(dir string) error {
	return errs.New("no %s found in %s or any directory above it", FileName, dir).
		WithHint("run `pit init` in the repository root to create one")
}
