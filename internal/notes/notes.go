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
	// GIF shows the last seconds of the recording that led here.
	GIF string `json:"gif,omitempty"`
	// Uncaptured says why the page was not loaded, when it was not:
	// the sandbox was not running, there was no browser.
	Uncaptured string `json:"uncaptured,omitempty"`
	// Logs are what the services wrote around the time of the note:
	// while pit loaded the page, and a little before.
	Logs []Log `json:"logs,omitempty"`
	// Recording is what the reviewer did before the note, when they
	// recorded it: the way to the state the note is about.
	Recording *Recording `json:"recording,omitempty"`
	// Posted is the comment the note went into, once it did.
	Posted string `json:"posted,omitempty"`
}

// Recording is what a reviewer did in a browser pit watched, and what
// it started from.
type Recording struct {
	SHA      string `json:"sha"`
	Scenario string `json:"scenario,omitempty"`
	// Edited says the data had been changed by hand before the
	// recording began: the scenario and the steps do not lead to it.
	Edited bool           `json:"edited,omitempty"`
	At     time.Time      `json:"at"`
	Steps  []inspect.Step `json:"steps"`
}

// Log is what one service wrote around the time of a note.
type Log struct {
	Service string    `json:"service"`
	Lines   []LogLine `json:"lines"`
	// Skipped counts the earlier lines in the window left out, when
	// there were more than a comment should carry.
	Skipped int `json:"skipped,omitempty"`
}

// LogLine is one line a service wrote, and when.
type LogLine struct {
	At   time.Time `json:"at"`
	Text string    `json:"text"`
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

const (
	recordName    = "notes.json"
	recordingName = "recording.json"
	recordingGIF  = "recording.gif"
)

// record is the file on disk.
type record struct {
	Repo  string `json:"repo"`
	PR    int    `json:"pr"`
	Last  int    `json:"last"`
	Notes []Note `json:"notes"`
}

// Files are what a note keeps beside its record.
type Files struct {
	Screenshot []byte
	GIF        []byte
}

// Add keeps a note, and its pictures when there are some, and returns
// it numbered.
func (b Book) Add(n Note, f Files) (Note, error) {
	err := b.Lock(func() error {
		r, err := b.read()
		if err != nil {
			return err
		}
		r.Last++
		n.ID = r.Last
		n.Screenshot, n.GIF = "", ""
		if err := os.MkdirAll(b.Dir, 0o700); err != nil {
			return errs.Wrap(err, "cannot create %s", b.Dir)
		}
		for _, file := range []struct {
			data []byte
			ext  string
			name *string
		}{{f.Screenshot, ".png", &n.Screenshot}, {f.GIF, ".gif", &n.GIF}} {
			if len(file.data) == 0 {
				continue
			}
			name := strconv.Itoa(n.ID) + file.ext
			if err := writeFile(filepath.Join(b.Dir, name), file.data); err != nil {
				return err
			}
			*file.name = name
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
			for _, file := range []string{n.Screenshot, n.GIF} {
				if file != "" {
					_ = os.Remove(file)
				}
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

// KeepRecording keeps a recording, and its GIF when there is one, until
// the next note takes them. A newer one replaces it: it is the one that
// led to what the reviewer is about to note.
func (b Book) KeepRecording(r Recording, gif []byte) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return errs.Wrap(err, "cannot write the recording")
	}
	return b.Lock(func() error {
		if err := os.MkdirAll(b.Dir, 0o700); err != nil {
			return errs.Wrap(err, "cannot create %s", b.Dir)
		}
		_ = os.Remove(filepath.Join(b.Dir, recordingGIF))
		if len(gif) > 0 {
			if err := writeFile(filepath.Join(b.Dir, recordingGIF), gif); err != nil {
				return err
			}
		}
		return writeFile(filepath.Join(b.Dir, recordingName), append(data, '\n'))
	})
}

// TakeRecording hands over the recording kept and its GIF, if there is
// one, and forgets them: they belong to one note.
func (b Book) TakeRecording() (*Recording, []byte, error) {
	var out *Recording
	var gif []byte
	err := b.Lock(func() error {
		path := filepath.Join(b.Dir, recordingName)
		data, err := os.ReadFile(path)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return errs.Wrap(err, "cannot read %s", path)
		}
		var r Recording
		if err := json.Unmarshal(data, &r); err != nil {
			return errs.Wrap(err, "%s is not readable", path).WithHint("remove it to go on without it")
		}
		out = &r
		gifPath := filepath.Join(b.Dir, recordingGIF)
		if gif, err = os.ReadFile(gifPath); err == nil {
			_ = os.Remove(gifPath)
		}
		return os.Remove(path)
	})
	return out, gif, err
}

// resolve turns the picture's name into where it is.
func (b Book) resolve(n Note) Note {
	if n.Screenshot != "" {
		n.Screenshot = filepath.Join(b.Dir, n.Screenshot)
	}
	if n.GIF != "" {
		n.GIF = filepath.Join(b.Dir, n.GIF)
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
