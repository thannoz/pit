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
	// Repo is the repository it belongs to, as pit ls names it:
	// github.com/acme/shop. Empty for snapshots saved before pit
	// recorded it; the directory they are in says the same.
	Repo string `json:"repo,omitempty"`
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
	// Parts are the databases it holds, one file each, when it was
	// saved from several. A snapshot of one database has one part, and
	// one saved before there were parts has none recorded: its data is
	// the single file pit always wrote.
	Parts []Part `json:"parts,omitempty"`
	// CreatedAt is when it was saved.
	CreatedAt time.Time `json:"createdAt"`
}

// Part is the data of one database in a snapshot.
type Part struct {
	// Service is the compose service it was saved from; empty for the
	// one database of a project that does not name it.
	Service string `json:"service,omitempty"`
	Size    int64  `json:"size"`
	Raw     int64  `json:"raw"`
}

// Pieces are the parts of a snapshot, including the one implied by a
// snapshot saved before parts were recorded.
func (s Snapshot) Pieces() []Part {
	if len(s.Parts) > 0 {
		return s.Parts
	}
	return []Part{{Size: s.Size, Raw: s.Raw}}
}

// Dump writes one part of a snapshot: what the save command for Service
// prints.
type Dump struct {
	Service string
	Write   func(ctx context.Context, w io.Writer) error
}

// One is the dump of a project with a single, unnamed database.
func One(write func(ctx context.Context, w io.Writer) error) []Dump {
	return []Dump{{Write: write}}
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

// Save runs the save commands, through dumps, into a new snapshot:
// one part for each.
//
// The data is written to temporary files and only takes its place when
// every command has finished and written something: a snapshot that
// exists is a complete one. Failing, being cancelled or writing nothing
// leaves nothing behind.
func (s Store) Save(ctx context.Context, meta Snapshot, dumps []Dump) (Snapshot, error) {
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

	start := s.now()
	var temps []string
	defer func() {
		for _, t := range temps {
			_ = os.Remove(t) // gone once renamed; a leftover otherwise
		}
	}()
	meta.Parts, meta.Size, meta.Raw = nil, 0, 0
	for _, d := range dumps {
		tmp, raw, err := s.dump(ctx, meta.ID, d)
		if tmp != "" {
			temps = append(temps, tmp)
		}
		if err != nil {
			return Snapshot{}, err
		}
		meta.Parts = append(meta.Parts, Part{Service: d.Service, Raw: raw})
	}
	meta.Took = s.now().Sub(start)
	meta.CreatedAt = s.now().UTC()

	var placed []string
	for i, part := range meta.Parts {
		data := s.partPath(meta.ID, part.Service)
		if err := os.Rename(temps[i], data); err != nil {
			removeAll(placed)
			return Snapshot{}, errs.Wrap(err, "cannot store the snapshot")
		}
		placed = append(placed, data)
		info, err := os.Stat(data)
		if err != nil {
			removeAll(placed)
			return Snapshot{}, errs.Wrap(err, "cannot store the snapshot")
		}
		meta.Parts[i].Size = info.Size()
		meta.Size += info.Size()
		meta.Raw += part.Raw
	}

	// The record last: data without one is not listed, and a record
	// never points at data that is not there.
	if err := writeRecord(s.Dir, meta); err != nil {
		removeAll(placed)
		return Snapshot{}, err
	}
	return meta, nil
}

// dump runs one save command into a temporary file, compressed, and
// says how much it wrote.
func (s Store) dump(ctx context.Context, id string, d Dump) (tmp string, raw int64, err error) {
	f, err := os.CreateTemp(s.Dir, "."+id+"-*")
	if err != nil {
		return "", 0, errs.Wrap(err, "cannot write in %s", s.Dir)
	}
	counted := &counter{}
	z := gzip.NewWriter(f)
	err = d.Write(ctx, io.MultiWriter(z, counted))
	if cerr := z.Close(); err == nil {
		err = cerr
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return f.Name(), 0, err
	}
	if counted.n == 0 {
		what := "the save command"
		if d.Service != "" {
			what = "the save command for " + d.Service
		}
		return f.Name(), 0, errs.New("%s wrote nothing", what).
			WithHint("data.snapshot's save has to write the dump to stdout; check it by running it yourself")
	}
	return f.Name(), counted.n, nil
}

// partPath is where one part's data is: sn_7f3a1b.db.gz, or
// sn_7f3a1b.gz for the one database of a project that does not name it.
func (s Store) partPath(id, service string) string {
	if service == "" {
		return filepath.Join(s.Dir, id+dataSuffix)
	}
	return filepath.Join(s.Dir, id+"."+service+dataSuffix)
}

func removeAll(paths []string) {
	for _, p := range paths {
		_ = os.Remove(p)
	}
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

// Open reads one part of a snapshot back, as its save command wrote it.
func (s Store) Open(snap Snapshot, service string) (io.ReadCloser, error) {
	f, err := os.Open(s.partPath(snap.ID, service))
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

// Remove deletes a snapshot. The record goes first: from then on the
// snapshot is not listed, and data left behind by a failure is not
// mistaken for one.
func (s Store) Remove(snap Snapshot) error {
	if err := os.Remove(filepath.Join(s.Dir, snap.ID+recordSuffix)); err != nil {
		return errs.Wrap(err, "cannot remove snapshot %s", snap.ID)
	}
	for _, part := range snap.Pieces() {
		data := s.partPath(snap.ID, part.Service)
		if err := os.Remove(data); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return errs.Wrap(err, "cannot remove the data of snapshot %s", snap.ID).
				WithHint("it is no longer listed; the file %s can be deleted by hand", data)
		}
	}
	return nil
}

// Stores are the snapshot stores under root, one per repository.
func Stores(root string) ([]Store, error) {
	entries, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, errs.Wrap(err, "cannot read %s", root)
	}
	var out []Store
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, Store{Dir: filepath.Join(root, e.Name())})
		}
	}
	return out, nil
}

// DataPath is where a snapshot's data is, the first part's where it has
// several.
func (s Store) DataPath(snap Snapshot) string {
	return s.partPath(snap.ID, snap.Pieces()[0].Service)
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
