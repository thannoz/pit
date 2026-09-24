// Package snapshot keeps the data states a reviewer froze: the output
// of a repository's own save command, compressed, with a record of
// where it came from.
//
// It knows nothing about databases. A snapshot is whatever the save
// command wrote to stdout; pit's part is keeping it safe, finding it
// again, and never leaving half of one behind.
package snapshot

import (
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/thannoz/pit/internal/errs"
)

// Snapshot is what is known about one saved state.
type Snapshot struct {
	// ID is how pit names it: sn_7f3a1b.
	ID string `json:"id"`
	// Name is the reviewer's own, optional.
	Name string `json:"name,omitempty"`
	// PR, SHA and Scenario say which sandbox it was taken from, and
	// what that sandbox had been loaded with.
	PR       int    `json:"pr"`
	SHA      string `json:"sha"`
	Scenario string `json:"scenario,omitempty"`
	// Service is the compose service it was saved from, when known.
	Service string `json:"service,omitempty"`
	// Size is what it takes on disk, Raw what the save command wrote.
	Size int64 `json:"size"`
	Raw  int64 `json:"raw"`
	// Took is how long saving took.
	Took time.Duration `json:"took"`
	// CreatedAt is when it was saved.
	CreatedAt time.Time `json:"createdAt"`
}

// Label is how a snapshot is shown: its name where it has one.
func (s Snapshot) Label() string {
	if s.Name != "" {
		return s.Name + " (" + s.ID + ")"
	}
	return s.ID
}

// Store is the snapshots of one repository.
type Store struct {
	// Dir holds them: a data file and a record for each.
	Dir string
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

const (
	dataSuffix   = ".gz"
	recordSuffix = ".json"
	idPrefix     = "sn_"
)

// namePattern is what a snapshot may be called: a word a shell does not
// need quoting for, and that cannot be taken for an ID.
var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// CheckName reports whether a name can be given to a snapshot.
func CheckName(name string) error {
	switch {
	case !namePattern.MatchString(name):
		return errs.New("%q cannot be a snapshot's name", name).
			WithHint("use lower-case letters, digits, dots, dashes and underscores, starting with a letter or digit: cart-with-voucher")
	case strings.HasPrefix(name, idPrefix):
		return errs.New("%q looks like a snapshot ID", name).
			WithHint("names that start with %s are how pit names snapshots itself; pick another", idPrefix)
	}
	return nil
}

// List returns the snapshots, oldest first. A directory that does not
// exist yet holds none.
func (s Store) List() ([]Snapshot, error) {
	entries, err := os.ReadDir(s.Dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, errs.Wrap(err, "cannot read the snapshots in %s", s.Dir)
	}
	var out []Snapshot
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), idPrefix) || !strings.HasSuffix(e.Name(), recordSuffix) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.Dir, e.Name()))
		if err != nil {
			return nil, errs.Wrap(err, "cannot read %s", e.Name())
		}
		var snap Snapshot
		if err := json.Unmarshal(data, &snap); err != nil {
			return nil, errs.Wrap(err, "cannot read %s", filepath.Join(s.Dir, e.Name())).
				WithHint("the file is damaged; removing it and its %s file removes that snapshot", dataSuffix)
		}
		out = append(out, snap)
	}
	slices.SortFunc(out, func(a, b Snapshot) int { return a.CreatedAt.Compare(b.CreatedAt) })
	return out, nil
}

// Save runs a save command, through dump, into a new snapshot.
//
// The data is written to a temporary file and only takes its place when
// the command has finished and written something: a snapshot that
// exists is a complete one. Failing, being cancelled or writing nothing
// leaves nothing behind.
func (s Store) Save(ctx context.Context, meta Snapshot, dump func(ctx context.Context, w io.Writer) error) (Snapshot, error) {
	if meta.Name != "" {
		if err := CheckName(meta.Name); err != nil {
			return Snapshot{}, err
		}
	}
	existing, err := s.List()
	if err != nil {
		return Snapshot{}, err
	}
	if meta.Name != "" {
		for _, e := range existing {
			if e.Name == meta.Name {
				return Snapshot{}, errs.New("a snapshot called %q exists already, %s from #%d", meta.Name, e.ID, e.PR).
					WithHint("pick another name, or leave it out and use the ID pit gives it")
			}
		}
	}
	if err := os.MkdirAll(s.Dir, 0o750); err != nil {
		return Snapshot{}, errs.Wrap(err, "cannot create %s", s.Dir)
	}

	meta.ID, err = newID(existing)
	if err != nil {
		return Snapshot{}, err
	}
	tmp, err := os.CreateTemp(s.Dir, "."+meta.ID+"-*")
	if err != nil {
		return Snapshot{}, errs.Wrap(err, "cannot write in %s", s.Dir)
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // gone once renamed; a leftover otherwise

	start := s.now()
	counted := &counter{}
	z := gzip.NewWriter(tmp)
	err = dump(ctx, io.MultiWriter(z, counted))
	if cerr := z.Close(); err == nil {
		err = cerr
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return Snapshot{}, err
	}
	if counted.n == 0 {
		return Snapshot{}, errs.New("the save command wrote nothing").
			WithHint("data.snapshot.save has to write the dump to stdout; check it by running it yourself")
	}
	meta.Took = s.now().Sub(start)
	meta.Raw = counted.n
	meta.CreatedAt = s.now().UTC()

	data := filepath.Join(s.Dir, meta.ID+dataSuffix)
	if err := os.Rename(tmp.Name(), data); err != nil {
		return Snapshot{}, errs.Wrap(err, "cannot store the snapshot")
	}
	info, err := os.Stat(data)
	if err != nil {
		return Snapshot{}, errs.Wrap(err, "cannot store the snapshot")
	}
	meta.Size = info.Size()

	// The record last: a data file without one is not listed, and a
	// record never points at data that is not there.
	if err := writeRecord(s.Dir, meta); err != nil {
		_ = os.Remove(data)
		return Snapshot{}, err
	}
	return meta, nil
}

// Find looks a snapshot up by its ID or its name.
func (s Store) Find(ref string) (Snapshot, error) {
	all, err := s.List()
	if err != nil {
		return Snapshot{}, err
	}
	for _, snap := range all {
		if snap.ID == ref || (snap.Name != "" && snap.Name == ref) {
			return snap, nil
		}
	}
	err = errs.New("there is no snapshot %q", ref)
	if len(all) == 0 {
		return Snapshot{}, errs.Hinted(err, "this repository has none yet; `pit snap save <pull request number>` makes one")
	}
	var known []string
	for _, snap := range all[max(0, len(all)-5):] {
		known = append(known, snap.Label())
	}
	return Snapshot{}, errs.Hinted(err, "the latest are %s", strings.Join(known, ", "))
}

// Open reads a snapshot's data back, as the save command wrote it.
func (s Store) Open(snap Snapshot) (io.ReadCloser, error) {
	f, err := os.Open(s.DataPath(snap))
	if err != nil {
		return nil, errs.Wrap(err, "cannot read snapshot %s", snap.ID).
			WithHint("its record is there, its data is not; the snapshot cannot be restored")
	}
	z, err := gzip.NewReader(f)
	if err != nil {
		_ = f.Close()
		return nil, errs.Wrap(err, "snapshot %s is damaged", snap.ID)
	}
	return readCloser{Reader: z, close: func() error { return errors.Join(z.Close(), f.Close()) }}, nil
}

type readCloser struct {
	io.Reader
	close func() error
}

func (r readCloser) Close() error { return r.close() }

// DataPath is where a snapshot's data is.
func (s Store) DataPath(snap Snapshot) string {
	return filepath.Join(s.Dir, snap.ID+dataSuffix)
}

func writeRecord(dir string, meta Snapshot) error {
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return errs.Wrap(err, "cannot record the snapshot")
	}
	tmp, err := os.CreateTemp(dir, "."+meta.ID+"-record-*")
	if err != nil {
		return errs.Wrap(err, "cannot record the snapshot")
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		return errs.Wrap(err, "cannot record the snapshot")
	}
	if err := tmp.Close(); err != nil {
		return errs.Wrap(err, "cannot record the snapshot")
	}
	if err := os.Rename(tmp.Name(), filepath.Join(dir, meta.ID+recordSuffix)); err != nil {
		return errs.Wrap(err, "cannot record the snapshot")
	}
	return nil
}

// newID makes an ID no snapshot of this repository has: short enough to
// type, which six hex digits are; random rather than counted, because
// an ID that is also a position would change meaning when one is
// removed.
func newID(existing []Snapshot) (string, error) {
	for range 16 {
		b := make([]byte, 3)
		if _, err := rand.Read(b); err != nil {
			return "", errs.Wrap(err, "cannot make a snapshot ID")
		}
		id := idPrefix + hex.EncodeToString(b)
		if !slices.ContainsFunc(existing, func(s Snapshot) bool { return s.ID == id }) {
			return id, nil
		}
	}
	return "", errs.New("cannot find a free snapshot ID")
}

func (s Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

type counter struct{ n int64 }

func (c *counter) Write(p []byte) (int, error) {
	c.n += int64(len(p))
	return len(p), nil
}
