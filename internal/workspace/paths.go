package workspace

import (
	"os"
	"path/filepath"
	goruntime "runtime"
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
	return stateDir(goruntime.GOOS, os.Getenv, os.UserHomeDir)
}

func stateDir(goos string, getenv func(string) string, home func() (string, error)) (string, error) {
	if dir := getenv(stateDirEnv); dir != "" {
		return dir, nil
	}

	// XDG_STATE_HOME is the home for data that should survive but is
	// not precious, so it wins when it is set.
	if xdg := getenv("XDG_STATE_HOME"); xdg != "" {
		return filepath.Join(xdg, "pit"), nil
	}

	// Windows keeps such data per machine, not in a roaming profile
	// that would carry worktrees between computers.
	if goos == "windows" {
		if local := getenv("LOCALAPPDATA"); local != "" {
			return filepath.Join(local, "pit"), nil
		}
	}

	h, err := home()
	if err != nil {
		return "", err
	}
	return filepath.Join(h, ".local", "state", "pit"), nil
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
	return i.WorktreeDirIn(stateDir, pr, "base")
}

// WorktreeDirIn is where one of a pull request's sandboxes has its
// checkout.
func (i Identity) WorktreeDirIn(stateDir string, pr int, slot string) string {
	if slot == "" {
		return i.WorktreeDir(stateDir, pr)
	}
	return i.WorktreeDir(stateDir, pr) + "-" + slot
}
