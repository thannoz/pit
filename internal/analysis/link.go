package analysis

import (
	"context"
	"io/fs"

	"github.com/thannoz/pit/internal/diff"
)

// Linker knows how the files of one language use each other: which
// file imports which, which function calls which.
//
// It is a seam of its own, next to Analyzer, because the two vary
// independently. How a TypeScript file imports another is the same in
// Next.js and SvelteKit; which files are pages is not.
type Linker interface {
	// Name is the language, as a reader would write it.
	Name() string
	// Links lists every use of one file by another in the project.
	Links(ctx context.Context, fsys fs.FS) ([]Link, error)
}

// Link is one file using another.
//
// Either side can be narrowed to lines. A TypeScript import is the whole
// file using the whole file -- a component module is usually one
// component. A Go call is one line using one function, and without the
// lines a changed helper would reach every route of its package.
type Link struct {
	// From is the file that uses, and At the lines where; zero is the
	// whole file.
	From string
	At   diff.Range
	// To is the file used, and Target what in it; zero is the whole
	// file.
	To     string
	Target diff.Range
}
