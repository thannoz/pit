// Package diff describes what a pull request changed: which files, in
// what way, and where inside them. It knows nothing about git beyond
// the format of git's output; running git is someone else's job.
package diff

// Change is what happened to a file.
type Change string

// The kinds of change git reports between two commits.
const (
	Added    Change = "added"
	Modified Change = "modified"
	Deleted  Change = "deleted"
	Renamed  Change = "renamed"
	Copied   Change = "copied"
	// TypeChanged is a path that became something else -- a file
	// turned into a symlink, or the other way round.
	TypeChanged Change = "type-changed"
)

// Diff is everything that differs between two commits.
type Diff struct {
	// Base is the commit the change is measured from: where the pull
	// request's branch left its target, not the target's tip.
	Base string
	// Head is the commit under review.
	Head string
	// Files are the changed paths, in the order git reports them.
	Files []File
}

// File is one changed path.
type File struct {
	// Path is where the file is now. For a deletion it is where it
	// was, because there is no "now".
	Path string
	// OldPath is where it came from, for a rename or a copy.
	OldPath string
	// Change is what happened to it.
	Change Change
	// Binary files have no lines, so no counts and no hunks.
	Binary bool
	// Added and Deleted count lines.
	Added, Deleted int
	// Hunks are the places inside the file that changed.
	Hunks []Hunk
}

// Hunk is one contiguous change inside a file.
type Hunk struct {
	// Old is what was replaced, in the file as it was.
	Old Range
	// New is what replaced it, in the file as it is.
	New Range
}

// Range is a run of lines. A Count of zero is a position, not a run:
// a pure insertion has no old lines, a pure deletion no new ones.
type Range struct {
	Start, Count int
}

// End is the last line of the range, or Start when it is empty.
func (r Range) End() int {
	if r.Count == 0 {
		return r.Start
	}
	return r.Start + r.Count - 1
}
