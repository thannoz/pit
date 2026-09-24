package diff

import (
	"bytes"
	"strconv"
	"strings"

	"github.com/thannoz/pit/internal/errs"
)

// ParseSummary reads the output of
//
//	git diff --raw --numstat -z --find-renames <base>...<head>
//
// which names every changed path and how it changed, and counts its
// lines. -z is the point: without it git quotes and escapes unusual
// paths, and a path with a tab or a newline in it is exactly the kind
// of thing a parser gets wrong once and nobody notices.
//
// The two formats arrive in one stream. Raw records come first, each
// starting with a colon; numstat records follow, one per path.
func ParseSummary(raw []byte) ([]File, error) {
	fields := bytes.Split(bytes.TrimSuffix(raw, []byte{0}), []byte{0})
	if len(fields) == 1 && len(fields[0]) == 0 {
		return nil, nil
	}

	var (
		files []File
		index = map[string]int{} // path as numstat names it -> position
	)

	for i := 0; i < len(fields); {
		field := string(fields[i])

		if strings.HasPrefix(field, ":") {
			f, used, err := rawRecord(fields[i:])
			if err != nil {
				return nil, err
			}
			index[f.Path] = len(files)
			files = append(files, f)
			i += used
			continue
		}

		path, added, deleted, binary, used, err := numstatRecord(fields[i:])
		if err != nil {
			return nil, err
		}
		pos, ok := index[path]
		if !ok {
			return nil, errs.New("git counted lines in %q but did not list it as changed", path)
		}
		files[pos].Added, files[pos].Deleted, files[pos].Binary = added, deleted, binary
		i += used
	}
	return files, nil
}

// rawRecord reads ":<modes> <shas> <status>" and the one or two paths
// after it, returning how many fields it consumed.
func rawRecord(fields [][]byte) (File, int, error) {
	parts := strings.Fields(string(fields[0]))
	if len(parts) != 5 {
		return File{}, 0, errs.New("cannot read git's summary line %q", fields[0])
	}
	status := parts[4]

	var f File
	switch status[0] {
	case 'A':
		f.Change = Added
	case 'M':
		f.Change = Modified
	case 'D':
		f.Change = Deleted
	case 'T':
		f.Change = TypeChanged
	case 'R':
		f.Change = Renamed
	case 'C':
		f.Change = Copied
	default:
		// U and X cannot appear between two commits; if they do, git
		// is telling us something pit does not understand.
		return File{}, 0, errs.New("git reported a change of kind %q, which pit does not know", status)
	}

	if f.Change == Renamed || f.Change == Copied {
		if len(fields) < 3 {
			return File{}, 0, errs.New("git's summary names a %s without both paths", f.Change)
		}
		f.OldPath, f.Path = string(fields[1]), string(fields[2])
		return f, 3, nil
	}

	if len(fields) < 2 {
		return File{}, 0, errs.New("git's summary names a change without a path")
	}
	f.Path = string(fields[1])
	return f, 2, nil
}

// numstatRecord reads "<added>\t<deleted>\t<path>", or for a rename
// "<added>\t<deleted>\t" followed by the old and the new path. Binary
// files are counted as "-".
func numstatRecord(fields [][]byte) (path string, added, deleted int, binary bool, used int, err error) {
	parts := strings.SplitN(string(fields[0]), "\t", 3)
	if len(parts) != 3 {
		return "", 0, 0, false, 0, errs.New("cannot read git's line count %q", fields[0])
	}

	if parts[0] == "-" && parts[1] == "-" {
		binary = true
	} else {
		if added, err = strconv.Atoi(parts[0]); err != nil {
			return "", 0, 0, false, 0, errs.Wrap(err, "cannot read git's line count %q", fields[0])
		}
		if deleted, err = strconv.Atoi(parts[1]); err != nil {
			return "", 0, 0, false, 0, errs.Wrap(err, "cannot read git's line count %q", fields[0])
		}
	}

	if parts[2] != "" {
		return parts[2], added, deleted, binary, 1, nil
	}
	// A rename: the path field is empty and the two paths follow.
	if len(fields) < 3 {
		return "", 0, 0, false, 0, errs.New("git's line count names a rename without both paths")
	}
	return string(fields[2]), added, deleted, binary, 3, nil
}

// AttachHunks reads the output of
//
//	git diff --unified=0 --find-renames <base>...<head>
//
// and adds each file's hunks to it. Only the lines that locate a file
// and the hunk headers are read; the changed text itself is not kept,
// because nothing downstream needs it and a large diff is large.
//
// Files without hunks -- a pure rename, a binary file, a changed mode
// -- simply get none.
func AttachHunks(files []File, patch []byte) error {
	index := map[string]int{}
	for i, f := range files {
		index[f.Path] = i
	}

	current := -1
	var oldPath string

	for _, line := range strings.Split(string(patch), "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			current, oldPath = -1, ""

		case strings.HasPrefix(line, "--- "):
			oldPath = pathOf(strings.TrimPrefix(line, "--- "), "a/")

		case strings.HasPrefix(line, "+++ "):
			path := pathOf(strings.TrimPrefix(line, "+++ "), "b/")
			if path == "" {
				// A deletion: the file is known by where it was.
				path = oldPath
			}
			pos, ok := index[path]
			if !ok {
				return errs.New("git's patch names %q, which its summary did not", path)
			}
			current = pos

		case strings.HasPrefix(line, "@@ ") && current >= 0:
			h, err := parseHunk(line)
			if err != nil {
				return err
			}
			files[current].Hunks = append(files[current].Hunks, h)
		}
	}
	return nil
}

// pathOf reads a path from a ---/+++ line, which git quotes when it
// contains anything unusual. /dev/null is the absence of a file.
//
// A path with a space in it gets a tab after it, so that patch(1) can
// tell where the name ends. It is a separator, not part of the name: a
// name that really ended in a tab would have been quoted, and the tab
// would be inside the quotes.
func pathOf(s, prefix string) string {
	s = strings.TrimSuffix(s, "\t")
	if s == "/dev/null" {
		return ""
	}
	if strings.HasPrefix(s, `"`) {
		// C-style quoting, octal escapes included, which is what Go's
		// own unquoting understands.
		if unquoted, err := strconv.Unquote(s); err == nil {
			s = unquoted
		}
	}
	return strings.TrimPrefix(s, prefix)
}

// parseHunk reads "@@ -a[,b] +c[,d] @@ ...". A missing count means
// one line, which is how git abbreviates the common case.
func parseHunk(line string) (Hunk, error) {
	fields := strings.Fields(line)
	if len(fields) < 3 || !strings.HasPrefix(fields[1], "-") || !strings.HasPrefix(fields[2], "+") {
		return Hunk{}, errs.New("cannot read git's hunk header %q", line)
	}

	old, err := parseRange(fields[1][1:])
	if err != nil {
		return Hunk{}, errs.Wrap(err, "cannot read git's hunk header %q", line)
	}
	next, err := parseRange(fields[2][1:])
	if err != nil {
		return Hunk{}, errs.Wrap(err, "cannot read git's hunk header %q", line)
	}
	return Hunk{Old: old, New: next}, nil
}

func parseRange(s string) (Range, error) {
	start, count, found := strings.Cut(s, ",")

	a, err := strconv.Atoi(start)
	if err != nil {
		return Range{}, err
	}
	if !found {
		return Range{Start: a, Count: 1}, nil
	}
	b, err := strconv.Atoi(count)
	if err != nil {
		return Range{}, err
	}
	return Range{Start: a, Count: b}, nil
}
