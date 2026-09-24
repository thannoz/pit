package workspace

import (
	"bytes"
	"context"
	"io"
	"io/fs"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/proc"
)

// Tree returns a commit's files as a file system, read from git's
// object store: the state before a pull request, without a checkout.
//
// Contents are fetched when they are first read, and in bulk: reading
// one .go file fetches every .go file of the commit in a single git
// process. The heuristics that read one file of a kind read them all,
// and one process per file would be most of the time spent.
func Tree(ctx context.Context, r Runner, repo Repo, commit string) (fs.FS, error) {
	out, err := r.Output(ctx, proc.Command{
		Name: "git", Args: []string{"ls-tree", "-r", "-z", "--long", "--full-tree", commit}, Dir: repo.Root,
	})
	if err != nil {
		return nil, errs.Wrap(err, "listing the files of %s", short(commit))
	}
	t := &tree{ctx: ctx, r: r, repo: repo, blobs: map[string]string{}, sizes: map[string]int64{},
		data: map[string][]byte{}, dirs: map[string][]string{".": nil}}
	for _, entry := range strings.Split(string(out), "\x00") {
		// <mode> SP <type> SP <object> SP <size> TAB <path>
		meta, name, ok := strings.Cut(entry, "\t")
		if !ok {
			continue
		}
		fields := strings.Fields(meta)
		if len(fields) != 4 || fields[1] != "blob" {
			continue // submodules are someone else's files
		}
		size, err := strconv.ParseInt(fields[3], 10, 64)
		if err != nil {
			continue
		}
		t.blobs[name], t.sizes[name] = fields[2], size
		t.add(name)
	}
	for dir := range t.dirs {
		slices.Sort(t.dirs[dir])
		t.dirs[dir] = slices.Compact(t.dirs[dir])
	}
	return t, nil
}

type tree struct {
	ctx  context.Context
	r    Runner
	repo Repo

	blobs map[string]string // file -> object name
	sizes map[string]int64
	dirs  map[string][]string // directory -> the names in it

	mu   sync.Mutex
	data map[string][]byte
}

// add records a file and the directories above it. A directory that
// is already known is already in its parent, and so are those above.
func (t *tree) add(name string) {
	for child := name; ; {
		dir := path.Dir(child)
		_, known := t.dirs[dir]
		t.dirs[dir] = append(t.dirs[dir], path.Base(child))
		if known || dir == "." {
			return
		}
		child = dir
	}
}

// ReadFile implements fs.ReadFileFS.
func (t *tree) ReadFile(name string) ([]byte, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "read", Path: name, Err: fs.ErrInvalid}
	}
	if _, ok := t.blobs[name]; !ok {
		return nil, &fs.PathError{Op: "read", Path: name, Err: fs.ErrNotExist}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if data, ok := t.data[name]; ok {
		return slices.Clone(data), nil
	}
	if err := t.fetch(name); err != nil {
		return nil, &fs.PathError{Op: "read", Path: name, Err: err}
	}
	return slices.Clone(t.data[name]), nil
}

// fetch reads name and every file not yet read that shares its
// extension -- or its name, for package.json and go.mod -- in one run
// of git cat-file.
func (t *tree) fetch(name string) error {
	kind := path.Ext(name)
	if kind == "" {
		kind = path.Base(name)
	}
	var names []string
	for n := range t.blobs {
		if _, done := t.data[n]; done {
			continue
		}
		if n == name || path.Ext(n) == kind || path.Base(n) == kind {
			names = append(names, n)
		}
	}
	slices.Sort(names)

	var in bytes.Buffer
	for _, n := range names {
		in.WriteString(t.blobs[n])
		in.WriteByte('\n')
	}
	out, err := t.r.Output(t.ctx, proc.Command{
		Name: "git", Args: []string{"cat-file", "--batch"}, Dir: t.repo.Root, Stdin: &in,
	})
	if err != nil {
		return err
	}
	// Each object: "<name> <type> <size>\n", the content, "\n".
	for _, n := range names {
		header, rest, ok := bytes.Cut(out, []byte("\n"))
		if !ok {
			return io.ErrUnexpectedEOF
		}
		fields := strings.Fields(string(header))
		if len(fields) != 3 {
			return errs.New("git cat-file answered %q for %s", header, n)
		}
		size, err := strconv.Atoi(fields[2])
		if err != nil || size+1 > len(rest) {
			return io.ErrUnexpectedEOF
		}
		t.data[n] = rest[:size]
		out = rest[size+1:]
	}
	return nil
}

// Open implements fs.FS.
func (t *tree) Open(name string) (fs.File, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	if entries, ok := t.dirs[name]; ok {
		return &treeDir{t: t, name: name, entries: entries}, nil
	}
	data, err := t.ReadFile(name)
	if err != nil {
		return nil, err
	}
	return &treeFile{info: info{name: path.Base(name), size: int64(len(data))}, r: bytes.NewReader(data)}, nil
}

// ReadDir implements fs.ReadDirFS.
func (t *tree) ReadDir(name string) ([]fs.DirEntry, error) {
	entries, ok := t.dirs[name]
	if !ok {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrNotExist}
	}
	out := make([]fs.DirEntry, 0, len(entries))
	for _, e := range entries {
		full := path.Join(name, e)
		_, dir := t.dirs[full]
		out = append(out, fs.FileInfoToDirEntry(info{name: e, dir: dir, size: t.sizes[full]}))
	}
	return out, nil
}

// Stat implements fs.StatFS without reading the file.
func (t *tree) Stat(name string) (fs.FileInfo, error) {
	if _, ok := t.dirs[name]; ok {
		return info{name: path.Base(name), dir: true}, nil
	}
	if _, ok := t.blobs[name]; ok {
		return info{name: path.Base(name), size: t.sizes[name]}, nil
	}
	return nil, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrNotExist}
}

type info struct {
	name string
	size int64
	dir  bool
}

func (i info) Name() string { return i.name }
func (i info) Size() int64  { return i.size }
func (i info) Mode() fs.FileMode {
	if i.dir {
		return fs.ModeDir | 0o555
	}
	return 0o444
}
func (i info) ModTime() time.Time { return time.Time{} }
func (i info) IsDir() bool        { return i.dir }
func (i info) Sys() any           { return nil }

type treeFile struct {
	info info
	r    *bytes.Reader
}

func (f *treeFile) Stat() (fs.FileInfo, error) { return f.info, nil }
func (f *treeFile) Read(p []byte) (int, error) { return f.r.Read(p) }
func (f *treeFile) Close() error               { return nil }

type treeDir struct {
	t       *tree
	name    string
	entries []string
	read    int
}

func (d *treeDir) Stat() (fs.FileInfo, error) { return info{name: path.Base(d.name), dir: true}, nil }
func (d *treeDir) Read([]byte) (int, error) {
	return 0, &fs.PathError{Op: "read", Path: d.name, Err: fs.ErrInvalid}
}
func (d *treeDir) Close() error { return nil }

func (d *treeDir) ReadDir(n int) ([]fs.DirEntry, error) {
	all, err := d.t.ReadDir(d.name)
	if err != nil {
		return nil, err
	}
	rest := all[d.read:]
	if n <= 0 {
		d.read = len(all)
		return rest, nil
	}
	if len(rest) == 0 {
		return nil, io.EOF
	}
	rest = rest[:min(n, len(rest))]
	d.read += len(rest)
	return rest, nil
}
