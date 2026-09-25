// Package notes keeps what a reviewer found in a pull request's
// sandbox, until it goes into a comment on the pull request.
//
// A note is kept apart from the sandbox's record, by repository and
// pull request: taking the sandbox down does not take what was found
// in it with it.
package notes

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/inspect"
)

// Note is one thing the reviewer found, with what it takes to see it
// again: the page, the commit, the data, and what the page did wrong.
type Note struct {
	// ID counts a pull request's notes from 1; a removed note's number
	// is not given to another.
	ID   int    `json:"id"`
	Text string `json:"text"`
	// URL is the page the note is about.
	URL string `json:"url"`
	// SHA is the commit the sandbox ran.
	SHA string `json:"sha"`
	// Scenario and Snapshot are where the data came from; Edited says
	// it was changed by hand since.
	Scenario string    `json:"scenario,omitempty"`
	Snapshot string    `json:"snapshot,omitempty"`
	Edited   bool      `json:"edited,omitempty"`
	At       time.Time `json:"at"`
	// Problems and Pending are what loading the page turned up; see
	// inspect.Report.
	Problems []inspect.Problem `json:"problems,omitempty"`
	Pending  []string          `json:"pending,omitempty"`
	// Screenshot is the picture of the page, a PNG.
	Screenshot string `json:"screenshot,omitempty"`
	// Uncaptured says why the page was not loaded, when it was not:
	// the sandbox was not running, there was no browser.
	Uncaptured string `json:"uncaptured,omitempty"`
	// Posted is the comment the note went into, once it did.
	Posted string `json:"posted,omitempty"`
}

// Captured reports whether the page was loaded for the note.
func (n Note) Captured() bool { return n.Uncaptured == "" }

// Book is the notes on one pull request.
type Book struct {
	// Dir holds them: a record and a picture for each note that has
	// one.
	Dir  string
	Repo string
	PR   int
	// Lock runs a function while no other pit changes the notes.
	Lock func(func() error) error
}

const recordName = "notes.json"

// record is the file on disk.
type record struct {
	Repo  string `json:"repo"`
	PR    int    `json:"pr"`
	Last  int    `json:"last"`
	Notes []Note `json:"notes"`
}

// Add keeps a note, and its picture when there is one, and returns it
// numbered.
func (b Book) Add(n Note, png []byte) (Note, error) {
	err := b.Lock(func() error {
		r, err := b.read()
		if err != nil {
			return err
		}
		r.Last++
		n.ID = r.Last
		n.Screenshot = ""
		if err := os.MkdirAll(b.Dir, 0o700); err != nil {
			return errs.Wrap(err, "cannot create %s", b.Dir)
		}
		if len(png) > 0 {
			name := strconv.Itoa(n.ID) + ".png"
			if err := writeFile(filepath.Join(b.Dir, name), png); err != nil {
				return err
			}
			n.Screenshot = name
		}
		r.Notes = append(r.Notes, n)
		return b.write(r)
	})
	if err != nil {
		return Note{}, err
	}
	return b.resolve(n), nil
}

// List is the notes, oldest first.
func (b Book) List() ([]Note, error) {
	r, err := b.read()
	if err != nil {
		return nil, err
	}
	out := make([]Note, len(r.Notes))
	for i, n := range r.Notes {
		out[i] = b.resolve(n)
	}
	return out, nil
}

// Remove forgets notes and their pictures. Every number has to be one
// of a note there is, or nothing is removed.
func (b Book) Remove(ids ...int) ([]Note, error) {
	var removed []Note
	err := b.Lock(func() error {
		r, err := b.read()
		if err != nil {
			return err
		}
		var unknown []string
		for _, id := range ids {
			if !slices.ContainsFunc(r.Notes, func(n Note) bool { return n.ID == id }) {
				unknown = append(unknown, strconv.Itoa(id))
			}
		}
		if len(unknown) > 0 {
			return errs.New("#%d has no note %s", b.PR, strings.Join(unknown, ", ")).
				WithHint("`pit note %d` lists the notes there are", b.PR)
		}
		r.Notes = slices.DeleteFunc(r.Notes, func(n Note) bool {
			if slices.Contains(ids, n.ID) {
				removed = append(removed, b.resolve(n))
				return true
			}
			return false
		})
		if err := b.write(r); err != nil {
			return err
		}
		for _, n := range removed {
			if n.Screenshot != "" {
				_ = os.Remove(n.Screenshot)
			}
		}
		return nil
	})
	return removed, err
}

// MarkPosted records that notes went into a comment, so that the next
// one does not tell them again.
func (b Book) MarkPosted(ids []int, comment string) error {
	return b.Lock(func() error {
		r, err := b.read()
		if err != nil {
			return err
		}
		for i := range r.Notes {
			if slices.Contains(ids, r.Notes[i].ID) {
				r.Notes[i].Posted = comment
			}
		}
		return b.write(r)
	})
}

// resolve turns the picture's name into where it is.
func (b Book) resolve(n Note) Note {
	if n.Screenshot != "" {
		n.Screenshot = filepath.Join(b.Dir, n.Screenshot)
	}
	return n
}

func (b Book) read() (record, error) {
	path := filepath.Join(b.Dir, recordName)
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return record{Repo: b.Repo, PR: b.PR}, nil
	}
	if err != nil {
		return record{}, errs.Wrap(err, "cannot read %s", path)
	}
	var r record
	if err := json.Unmarshal(data, &r); err != nil {
		return record{}, errs.Wrap(err, "%s is not readable", path).
			WithHint("move it aside to start the notes on #%d over", b.PR)
	}
	return r, nil
}

func (b Book) write(r record) error {
	r.Repo, r.PR = b.Repo, b.PR
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return errs.Wrap(err, "cannot write the notes")
	}
	return writeFile(filepath.Join(b.Dir, recordName), append(data, '\n'))
}

// writeFile replaces a file whole: a reader sees the old one or the new
// one, never half.
func writeFile(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return errs.Wrap(err, "cannot write in %s", filepath.Dir(path))
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return errs.Wrap(err, "cannot write %s", path)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return errs.Wrap(err, "cannot write %s", path)
	}
	if err := tmp.Close(); err != nil {
		return errs.Wrap(err, "cannot write %s", path)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return errs.Wrap(err, "cannot write %s", path)
	}
	return nil
}
