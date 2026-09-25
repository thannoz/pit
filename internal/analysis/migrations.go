package analysis

import (
	"slices"
	"strings"

	"github.com/thannoz/pit/internal/diff"
)

// Migrations are what a pull request does to a project's migrations.
type Migrations struct {
	// New are the migrations it adds, in the order they run: a
	// database that has run everything before the pull request runs
	// these, and only these, when it is merged.
	New []File
	// Changed are existing migrations it edits. A database that ran
	// one already will not run it again.
	Changed []File
	// Removed are migrations it deletes.
	Removed []File
}

// Empty reports whether the pull request touches no migrations.
func (m Migrations) Empty() bool {
	return len(m.New) == 0 && len(m.Changed) == 0 && len(m.Removed) == 0
}

// MigrationsOf finds the migrations among a pull request's files. The
// model a tool makes migrations from, a Prisma schema, is not one: it
// does not run.
func MigrationsOf(files []File) Migrations {
	var m Migrations
	for _, f := range files {
		if f.Kind != Migration || strings.HasSuffix(f.Path, ".prisma") {
			continue
		}
		switch f.Change {
		case diff.Added, diff.Copied:
			m.New = append(m.New, f)
		case diff.Deleted:
			m.Removed = append(m.Removed, f)
		case diff.Renamed:
			// Renamed to a name that sorts elsewhere, it runs again
			// where a database has not seen that name.
			m.New = append(m.New, f)
		default:
			m.Changed = append(m.Changed, f)
		}
	}
	for _, list := range [][]File{m.New, m.Changed, m.Removed} {
		slices.SortStableFunc(list, func(a, b File) int { return naturally(a.Path, b.Path) })
	}
	return m
}

// naturally compares paths the way migration tools order their files:
// the numbers in them as numbers, so that 2_add.sql comes before
// 10_drop.sql, and the rest as text.
func naturally(a, b string) int {
	for a != "" && b != "" {
		da, db := digits(a), digits(b)
		if da > 0 && db > 0 {
			na, nb := strings.TrimLeft(a[:da], "0"), strings.TrimLeft(b[:db], "0")
			if c := len(na) - len(nb); c != 0 {
				return c
			}
			if c := strings.Compare(na, nb); c != 0 {
				return c
			}
			a, b = a[da:], b[db:]
			continue
		}
		if a[0] != b[0] {
			return int(a[0]) - int(b[0])
		}
		a, b = a[1:], b[1:]
	}
	return len(a) - len(b)
}

func digits(s string) int {
	n := 0
	for n < len(s) && s[n] >= '0' && s[n] <= '9' {
		n++
	}
	return n
}
