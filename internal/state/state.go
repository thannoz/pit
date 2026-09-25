// Package state remembers which sandboxes exist, so pit survives a
// crash, a reboot and a second pit running at the same time.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/thannoz/pit/internal/errs"
)

// FileName is the record pit keeps.
const FileName = "state.json"

// lockName is a separate file, because the lock has to be held while
// the record itself is replaced by a rename.
const lockName = "state.lock"

// Version is the format of the file on disk. An older pit reading a
// newer file should say so rather than misread it.
const Version = 1

// Sandbox is one pull request pit has set up.
type Sandbox struct {
	// PR is the pull request number.
	PR int `json:"pr"`
	// Repo is the canonical repository identity, e.g.
	// "github.com/acme/shop".
	Repo string `json:"repo"`
	// RepoRef is the slug-and-hash used for directory and project
	// names.
	RepoRef string `json:"repoRef"`
	// RepoRoot is the working copy the sandbox was created from. It is
	// recorded because tearing one down has to run `git worktree
	// prune` there, and `pit down --all` may be run from anywhere.
	RepoRoot string `json:"repoRoot"`
	// Project is the Docker Compose project name.
	Project string `json:"project"`
	// ComposeFiles are the files the sandbox was brought up with,
	// recorded because taking it down needs the same set and the
	// configuration may have changed since.
	ComposeFiles []string `json:"composeFiles"`
	// Worktree is where the pull request is checked out.
	Worktree string `json:"worktree"`
	// WebService is the compose service a reviewer opens. It is
	// recorded so that `pit logs` and `pit shell` work from anywhere,
	// without finding and reading the repository's configuration.
	WebService string `json:"webService"`
	// Port is the host port the sandbox is published on.
	Port int `json:"port"`
	// URL is what the reviewer opens.
	URL string `json:"url"`
	// SHA is the commit that was checked out.
	SHA string `json:"sha"`
	// Branch, Title and Author are cached so that `pit ls` works
	// without a network.
	Branch string `json:"branch,omitempty"`
	// BaseBranch is where the pull request is headed; empty when the
	// hosting service did not say, and then it is the remote's default.
	BaseBranch string `json:"baseBranch,omitempty"`
	Title      string `json:"title,omitempty"`
	Author     string `json:"author,omitempty"`
	// Scenario records where the data came from, so two people can
	// talk about the same thing. See docs/03-datenkonzept.md.
	Scenario string `json:"scenario,omitempty"`
	// Snapshot is the snapshot the data was restored from, when it
	// was. Scenario then names the one that snapshot was taken on,
	// which is where example values for addresses still come from.
	Snapshot string `json:"snapshot,omitempty"`
	// Writes are the databases' counts of writes when the data was
	// loaded, one for each writes command. pit ls counts again and
	// compares, which is how it knows the data was changed since.
	Writes []int64 `json:"writes,omitempty"`
	// Edited is data written to before Writes was counted: an update
	// kept it, and its migrations were not to count as the reviewer's
	// writes, so the count began again after them.
	Edited bool `json:"edited,omitempty"`
	// CreatedAt is when the sandbox was set up.
	CreatedAt time.Time `json:"createdAt"`
	// Steps is how long each part of the setup took, in the order it
	// ran. Recorded because the answer to "why does this take so
	// long" is worth more than a stopwatch held once by hand.
	Steps []Step `json:"steps,omitempty"`
	// SetupMillis is the wall clock of the whole setup. It is more
	// than the steps add up to: reserving a port, writing the
	// override and recording the result all happen between them.
	SetupMillis int64 `json:"setupMs,omitempty"`
	// ProbedAt is when pit last asked the sandbox whether it answers.
	// Its own requests are not a reviewer's visits.
	ProbedAt time.Time `json:"probedAt,omitempty"`
	// Checked are the addresses of `pit what` the reviewer has looked
	// at. Kept across updates of the sandbox: a new commit makes a
	// check stale only where it touches what led to the address.
	Checked []Check `json:"checked,omitempty"`
}

// Check is one address a reviewer has looked at.
type Check struct {
	// Address is the method and path, as the checklist writes them:
	// "GET /orders/{id}". Not the number: numbers move when the list
	// does, an address does not.
	Address string `json:"address"`
	// SHA is the commit the sandbox ran when it was looked at.
	SHA string `json:"sha"`
	// At is when.
	At time.Time `json:"at"`
	// Visited says pit saw the request in the web service's log,
	// rather than being told.
	Visited bool `json:"visited,omitempty"`
	// Undone marks a check taken back: a visit before it does not
	// count again.
	Undone bool `json:"undone,omitempty"`
}

// Step is one part of a setup and how long it took.
//
// The duration is milliseconds rather than Go's nanoseconds so that
// the state file stays readable by whoever opens it, and because
// nothing here is decided at a finer resolution than that.
type Step struct {
	Name   string `json:"name"`
	Millis int64  `json:"ms"`
}

// Took is the duration in a form Go can compute with.
func (s Step) Took() time.Duration { return time.Duration(s.Millis) * time.Millisecond }

// SetupTook is how long the whole setup ran.
func (s Sandbox) SetupTook() time.Duration { return time.Duration(s.SetupMillis) * time.Millisecond }

// Millis truncates a duration for recording.
//
// Truncating rather than rounding is what keeps the parts from adding
// up to more than the whole: half a dozen steps each rounded up can
// exceed a total that was rounded down, and a table whose rows sum to
// more than its total is a table nobody believes. The cost is that a
// step under a millisecond is recorded as no time at all, which for a
// tool measured in seconds is the truth.
func Millis(d time.Duration) int64 {
	if d <= 0 {
		return 0
	}
	return d.Milliseconds()
}

// Key identifies a sandbox: a pull request number means nothing without
// the repository it belongs to.
func (s Sandbox) Key() string {
	return s.RepoRef + "#" + fmt.Sprint(s.PR)
}

// Age is how long the sandbox has been up.
func (s Sandbox) Age() time.Duration { return time.Since(s.CreatedAt) }

// File is the whole record.
type File struct {
	Version   int       `json:"version"`
	Sandboxes []Sandbox `json:"sandboxes"`
}

// Find returns the sandbox for a pull request in a repository.
func (f *File) Find(repoRef string, pr int) (Sandbox, bool) {
	for _, s := range f.Sandboxes {
		if s.RepoRef == repoRef && s.PR == pr {
			return s, true
		}
	}
	return Sandbox{}, false
}

// Put adds a sandbox or replaces the one it supersedes.
func (f *File) Put(s Sandbox) {
	for i, existing := range f.Sandboxes {
		if existing.RepoRef == s.RepoRef && existing.PR == s.PR {
			f.Sandboxes[i] = s
			return
		}
	}
	f.Sandboxes = append(f.Sandboxes, s)
}

// Remove drops a sandbox and reports whether there was one.
func (f *File) Remove(repoRef string, pr int) bool {
	before := len(f.Sandboxes)
	f.Sandboxes = slices.DeleteFunc(f.Sandboxes, func(s Sandbox) bool {
		return s.RepoRef == repoRef && s.PR == pr
	})
	return len(f.Sandboxes) != before
}

// Store reads and writes the record.
type Store struct {
	dir string
}

// Open prepares the store in dir, creating the directory if needed.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, errs.Wrap(err, "cannot create %s", dir).
			WithHint("check that the directory is writable")
	}
	return &Store{dir: dir}, nil
}

// Path is where the record lives.
func (s *Store) Path() string { return filepath.Join(s.dir, FileName) }

// Load reads the record. A missing file is an empty record, not an
// error: the first run has nothing to read.
func (s *Store) Load() (*File, error) {
	release, err := s.lock()
	if err != nil {
		return nil, err
	}
	defer release()

	return s.read()
}

// TryLoad reads the record only if the lock is free, and reports
// whether it managed to.
//
// It exists for `pit doctor`, which must never block: it is run
// precisely when something is stuck, and a diagnosis that hangs on the
// problem it is meant to diagnose is worse than no diagnosis.
func (s *Store) TryLoad() (*File, bool) {
	release, ok := s.TryLock()
	if !ok {
		return nil, false
	}
	defer release()

	f, err := s.read()
	if err != nil {
		return nil, false
	}
	return f, true
}

// List returns the sandboxes, most recently created first.
func (s *Store) List() ([]Sandbox, error) {
	f, err := s.Load()
	if err != nil {
		return nil, err
	}

	out := slices.Clone(f.Sandboxes)
	slices.SortFunc(out, func(a, b Sandbox) int {
		return b.CreatedAt.Compare(a.CreatedAt)
	})
	return out, nil
}

// Update reads the record, hands it to fn, and writes the result back.
//
// The lock is held across the whole of that, which is the point: two
// pit processes starting sandboxes at the same time would otherwise
// each read the file, each add their own entry, and the second write
// would erase the first.
func (s *Store) Update(fn func(*File) error) error {
	release, err := s.lock()
	if err != nil {
		return err
	}
	defer release()

	f, err := s.read()
	if err != nil {
		return err
	}
	if err := fn(f); err != nil {
		return err
	}
	return s.write(f)
}

// read loads the file without locking; callers hold the lock.
func (s *Store) read() (*File, error) {
	data, err := os.ReadFile(s.Path())
	if errors.Is(err, fs.ErrNotExist) {
		return &File{Version: Version}, nil
	}
	if err != nil {
		return nil, errs.Wrap(err, "cannot read %s", s.Path())
	}

	// An empty file is what a crash between create and write leaves
	// behind. Treating it as an empty record is kinder than refusing
	// to start.
	if len(strings.TrimSpace(string(data))) == 0 {
		return &File{Version: Version}, nil
	}

	var f File
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, errs.Wrap(err, "%s is not readable", s.Path()).
			WithHint("this should not happen; move the file aside and pit will start over")
	}
	if f.Version > Version {
		return nil, errs.New("%s was written by a newer pit (format %d, this build understands %d)",
			s.Path(), f.Version, Version).
			WithHint("upgrade pit")
	}
	f.Version = Version
	return &f, nil
}

// write replaces the file atomically: a reader either sees the old
// record or the new one, never half of either.
func (s *Store) write(f *File) error {
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return errs.Wrap(err, "cannot write the state")
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(s.dir, FileName+".tmp-*")
	if err != nil {
		return errs.Wrap(err, "cannot create a temporary file in %s", s.dir)
	}
	tmpName := tmp.Name()
	// If anything below fails, the half-written file must not survive.
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return errs.Wrap(err, "cannot write %s", tmpName)
	}
	// Without the sync the rename can land before the contents do, and
	// a power cut leaves an empty file where the record used to be.
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return errs.Wrap(err, "cannot flush %s", tmpName)
	}
	if err := tmp.Close(); err != nil {
		return errs.Wrap(err, "cannot close %s", tmpName)
	}

	if err := os.Rename(tmpName, s.Path()); err != nil {
		return errs.Wrap(err, "cannot replace %s", s.Path())
	}
	return nil
}

// lock takes the exclusive lock and returns the function that releases
// it. It blocks until the lock is free: a second pit should wait its
// turn rather than fail.
func (s *Store) lock() (func(), error) {
	path := filepath.Join(s.dir, lockName)

	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, errs.Wrap(err, "cannot open %s", path).
			WithHint("check that %s is writable", s.dir)
	}
	if err := lockFile(f); err != nil {
		_ = f.Close()
		return nil, errs.Wrap(err, "cannot lock %s", path)
	}

	return func() {
		_ = unlockFile(f)
		_ = f.Close()
	}, nil
}

// TryLock takes the lock only if it is free. It exists for `pit doctor`,
// which should report that another pit is running rather than hang
// waiting for it.
func (s *Store) TryLock() (func(), bool) {
	f, err := os.OpenFile(filepath.Join(s.dir, lockName), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false
	}
	if err := tryLockFile(f); err != nil {
		_ = f.Close()
		return nil, false
	}
	return func() {
		_ = unlockFile(f)
		_ = f.Close()
	}, true
}
