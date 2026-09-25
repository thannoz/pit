package sandbox

import (
	"path/filepath"
	"strconv"

	"github.com/thannoz/pit/internal/notes"
)

// Notes is what the reviewer noted on a pull request. It is kept by
// repository and pull request rather than with the sandbox, which can
// be taken down before the notes go into a comment.
func (m *Manager) Notes(repo, repoRef string, pr int) notes.Book {
	return notes.Book{
		Dir:  filepath.Join(m.StateDir, "notes", repoRef, "pr-"+strconv.Itoa(pr)),
		Repo: repo,
		PR:   pr,
		Lock: m.Store.Locked,
	}
}
