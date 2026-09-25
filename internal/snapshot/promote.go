package snapshot

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"unicode/utf8"

	"github.com/thannoz/pit/internal/config"
	"github.com/thannoz/pit/internal/errs"
)

// Promotion is a snapshot on its way into the repository: its data as
// files beside the code, and a scenario in .pit.yaml that loads them.
//
// A snapshot lives on one machine and goes when it is removed; a
// scenario is checked in and every reviewer has it. Promoting is how a
// state someone spent fifteen minutes clicking together stops being
// theirs alone.
type Promotion struct {
	// Scenario is the one to add, or the one whose files are replaced.
	Scenario config.Scenario
	// Files are where each part goes, relative to the directory of the
	// .pit.yaml, in the order of the snapshot's parts.
	Files []string
	// Replacing says the scenario exists already and loads a snapshot:
	// its files are replaced and .pit.yaml is left as it is.
	Replacing bool
	// Edited is the .pit.yaml with the scenario added. Nil when
	// replacing, and when the file cannot be edited: Manual then says
	// why, and the lines to add by hand are in ScenarioYAML.
	Edited []byte
	Manual error

	snap   Snapshot
	store  Store
	config string
	dir    string
	parts  []Part
}

// PromoteOptions are what a reviewer can choose.
type PromoteOptions struct {
	// Name is the scenario's.
	Name string
	// Description is what `pit scenarios` shows; empty for one saying
	// where the snapshot came from.
	Description string
	// Dir is where the files go, relative to the .pit.yaml.
	Dir string
	// Params are the example values of the scenario the snapshot was
	// taken on, from the configuration its sandbox was built with.
	// Nil means the ones in .pit.yaml, if it has that scenario.
	Params map[string]string
}

// DefaultPromoteDir is where promoted snapshots go unless told
// otherwise: beside the fixtures a repository usually has.
const DefaultPromoteDir = "fixtures"

// Plan works out what promoting a snapshot into the repository whose
// .pit.yaml is at configPath would write, and writes nothing.
//
// The snapshot is loaded back through data.snapshot's restore commands,
// so the file has to have them, for each database the snapshot holds.
// A scenario of the same name is replaced only when it is a promoted
// snapshot itself: one that loads with commands of its own was written
// by someone, and is not pit's to overwrite.
func (s Store) Plan(snap Snapshot, configPath string, o PromoteOptions) (Promotion, error) {
	// A name that also makes a file name, and one a shell takes as it
	// is in --scenario=<name>.
	if !namePattern.MatchString(o.Name) {
		return Promotion{}, errs.New("%q cannot be the name of a promoted scenario", o.Name).
			WithHint("use lower-case letters, digits, dots, dashes and underscores, starting with a letter or digit: cart-with-voucher")
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return Promotion{}, err
	}
	commands := cfg.Data.Snapshot.Each()
	if len(commands) == 0 {
		return Promotion{}, errs.New("%s has no data.snapshot, so a promoted snapshot could not be loaded", config.FileName).
			WithHint("the scenario loads it with data.snapshot's restore command; add the commands it was saved with")
	}
	dir := path.Clean(filepath.ToSlash(o.Dir))
	if dir == "" || dir == "." {
		dir = DefaultPromoteDir
	}
	if !filepath.IsLocal(filepath.FromSlash(dir)) {
		return Promotion{}, errs.New("%s is outside the repository", o.Dir).
			WithHint("a scenario is shared, so its files have to be in the repository: --dir=fixtures")
	}

	p := Promotion{snap: snap, store: s, config: configPath, dir: cfg.Dir, parts: snap.Pieces()}

	if existing, ok := cfg.Scenario(o.Name); ok {
		if existing.Snapshot.IsZero() {
			return Promotion{}, errs.New("scenario %q exists already and loads its data with commands of its own", o.Name).
				WithHint("promote it under another name with --as")
		}
		p.Scenario, p.Replacing = existing, true
		p.Files, err = replacing(existing, p.parts)
		if err != nil {
			return Promotion{}, err
		}
		return p, nil
	}

	sc := config.Scenario{Name: o.Name, Description: o.Description}
	if sc.Description == "" {
		sc.Description = fmt.Sprintf("Saved in #%d at %s", snap.PR, shortSHA(snap.SHA))
	}
	// The example values of the scenario the snapshot was taken on
	// still name what is in it: the order it began with is there,
	// whatever was clicked on top.
	params := o.Params
	if params == nil && snap.Scenario != "" {
		params, _ = cfg.Params(snap.Scenario)
	}
	if len(params) > 0 {
		sc.Params = params
	}

	single := len(p.parts) == 1 && len(commands) == 1
	for _, part := range p.parts {
		ext, err := s.extension(snap, part.Service)
		if err != nil {
			return Promotion{}, err
		}
		if single {
			file := path.Join(dir, o.Name+ext)
			sc.Snapshot = config.ScenarioSnapshot{File: file}
			p.Files = append(p.Files, file)
			break
		}
		if part.Service == "" {
			return Promotion{}, errs.New("%s holds one database without a service name, but data.snapshot restores %d", snap.Label(), len(commands)).
				WithHint("it was saved with other snapshot commands than the ones %s has now", config.FileName)
		}
		file := path.Join(dir, o.Name+"."+part.Service+ext)
		sc.Snapshot.Files = append(sc.Snapshot.Files, config.SnapshotFile{Service: part.Service, File: file})
		p.Files = append(p.Files, file)
	}
	if _, err := cfg.Restores(sc); err != nil {
		return Promotion{}, errs.Wrap(err, "%s cannot be loaded with the snapshot commands %s has now", snap.Label(), config.FileName)
	}
	for _, f := range p.Files {
		if _, err := os.Stat(filepath.Join(cfg.Dir, filepath.FromSlash(f))); err == nil {
			return Promotion{}, errs.New("%s exists already, and no scenario of that name loads it", f).
				WithHint("promote under another name with --as, or into another directory with --dir")
		}
	}
	p.Scenario = sc

	src, err := os.ReadFile(configPath)
	if err != nil {
		return Promotion{}, errs.Wrap(err, "cannot read %s", configPath)
	}
	p.Edited, p.Manual = config.AddScenario(src, sc)
	return p, nil
}

// replacing matches the files an existing scenario loads to the parts
// of a snapshot: one to one, by service where there are several.
func replacing(sc config.Scenario, parts []Part) ([]string, error) {
	files := sc.Snapshot.Each()
	mismatch := errs.New("scenario %q loads %d %s, but the snapshot holds %d", sc.Name,
		len(files), pick(len(files), "file", "files"), len(parts)).
		WithHint("promote it under another name with --as")
	if len(files) != len(parts) {
		return nil, mismatch
	}
	if len(files) == 1 {
		return []string{files[0].File}, nil
	}
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		i := slices.IndexFunc(files, func(f config.SnapshotFile) bool { return f.Service == part.Service })
		if i < 0 {
			return nil, errs.New("scenario %q loads no file into %s, which the snapshot holds", sc.Name, part.Service).
				WithHint("promote it under another name with --as")
		}
		out = append(out, files[i].File)
	}
	return out, nil
}

// extension names a dump by what it starts with. pit does not know the
// database; the dump says what it is well enough for a file name,
// which is all the name is for -- the restore command reads it either
// way.
func (s Store) extension(snap Snapshot, service string) (string, error) {
	r, err := s.Open(snap, service)
	if err != nil {
		return "", err
	}
	defer func() { _ = r.Close() }()
	head, err := bufio.NewReaderSize(r, 4096).Peek(4096)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, bufio.ErrBufferFull) {
		return "", errs.Wrap(err, "snapshot %s is damaged", snap.ID)
	}
	return extensionOf(head), nil
}

func extensionOf(head []byte) string {
	switch {
	case bytes.HasPrefix(head, []byte("PGDMP")):
		return ".dump" // pg_dump's own format
	case bytes.HasPrefix(head, []byte{0x6d, 0xe2, 0x99, 0x81}):
		return ".archive" // mongodump --archive
	case bytes.HasPrefix(head, []byte{0x1f, 0x8b}):
		return ".gz"
	case text(head):
		return ".sql"
	}
	return ".dump"
}

// text reports whether the start of a dump reads as text: no NUL, and
// valid UTF-8 up to a character the cut may have split.
func text(head []byte) bool {
	if bytes.IndexByte(head, 0) >= 0 {
		return false
	}
	for cut := 0; cut < utf8.UTFMax && cut <= len(head); cut++ {
		if utf8.Valid(head[:len(head)-cut]) {
			return true
		}
	}
	return false
}

// Write puts the files in place and then the edited .pit.yaml. The
// files come first: a .pit.yaml naming a file that is not there yet is
// a broken repository, a file nothing names yet is only a file.
//
// Each file is written beside its destination and renamed when all of
// them are complete, so a failure leaves the repository as it was --
// including a promoted scenario whose files were being replaced.
func (p Promotion) Write() (int64, error) {
	var (
		temps []string
		size  int64
	)
	defer func() { removeAll(temps) }()
	for i, part := range p.parts {
		dest := filepath.Join(p.dir, filepath.FromSlash(p.Files[i]))
		tmp, n, err := p.copyPart(part.Service, dest)
		if tmp != "" {
			temps = append(temps, tmp)
		}
		if err != nil {
			return 0, err
		}
		size += n
	}

	var created []string
	for i, tmp := range temps {
		dest := filepath.Join(p.dir, filepath.FromSlash(p.Files[i]))
		if _, err := os.Stat(dest); err != nil {
			created = append(created, dest)
		}
		if err := os.Rename(tmp, dest); err != nil {
			removeAll(created)
			return 0, errs.Wrap(err, "cannot write %s", p.Files[i])
		}
	}
	temps = nil

	if p.Edited != nil {
		if err := replaceFile(p.config, p.Edited); err != nil {
			removeAll(created)
			return 0, err
		}
	}
	return size, nil
}

func (p Promotion) copyPart(service, dest string) (string, int64, error) {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", 0, errs.Wrap(err, "cannot create %s", filepath.Dir(dest))
	}
	r, err := p.store.Open(p.snap, service)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = r.Close() }()
	f, err := os.CreateTemp(filepath.Dir(dest), "."+filepath.Base(dest)+".*")
	if err != nil {
		return "", 0, errs.Wrap(err, "cannot write into %s", filepath.Dir(dest))
	}
	n, err := io.Copy(f, r)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return f.Name(), 0, errs.Wrap(err, "cannot write %s", dest)
	}
	// Created with 0600; a file for the repository is read by others.
	if err := os.Chmod(f.Name(), 0o644); err != nil {
		return f.Name(), 0, errs.Wrap(err, "cannot write %s", dest)
	}
	return f.Name(), n, nil
}

// replaceFile writes a file anew by renaming a complete copy over it,
// keeping its permissions.
func replaceFile(name string, data []byte) error {
	info, err := os.Stat(name)
	if err != nil {
		return errs.Wrap(err, "cannot write %s", name)
	}
	f, err := os.CreateTemp(filepath.Dir(name), "."+filepath.Base(name)+".*")
	if err != nil {
		return errs.Wrap(err, "cannot write %s", name)
	}
	tmp := f.Name()
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp, info.Mode().Perm())
	}
	if err == nil {
		err = os.Rename(tmp, name)
	}
	if err != nil {
		_ = os.Remove(tmp)
		return errs.Wrap(err, "cannot write %s", name)
	}
	return nil
}

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

func pick(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
