package workspace

import (
	"os"
	"path/filepath"
	"strconv"
)

// stateDirEnv lets tests and users point pit's state somewhere else.
const stateDirEnv = "PIT_STATE_DIR"

// StateDir is where pit keeps everything it creates: worktrees,
// generated files and its own records.
//
// It sits outside the repository on purpose. A worktree inside it would
// be picked up by file watchers, linters, test runners and editor
// indexing -- the reviewer's tooling would start reporting problems in
// someone else's pull request.
func StateDir() (string, error) {
	if dir := os.Getenv(stateDirEnv); dir != "" {
		return dir, nil
	}

	// os.UserCacheDir is the closest thing Go offers on both platforms;
	// XDG_STATE_HOME is the more correct home for data that should
	// survive but is not precious, so it wins when it is set.
	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
		return filepath.Join(xdg, "pit"), nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "pit"), nil
}

// RepoDir is where everything belonging to one repository lives. The
// hash is part of the name because two clones of the same repository,
// or two repositories with the same slug, must not share a directory.
func (i Identity) RepoDir(stateDir string) string {
	return filepath.Join(stateDir, i.Ref())
}

// WorktreeDir is where pull request pr is checked out.
func (i Identity) WorktreeDir(stateDir string, pr int) string {
	return filepath.Join(i.RepoDir(stateDir), "pr-"+strconv.Itoa(pr))
}

// BaseWorktreeDir is where the commit pull request pr goes into is
// checked out, beside the pull request itself.
func (i Identity) BaseWorktreeDir(stateDir string, pr int) string {
	return i.WorktreeDir(stateDir, pr) + "-base"
}
