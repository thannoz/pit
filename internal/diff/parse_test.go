package diff

import (
	"slices"
	"testing"
)

func TestParseSummaryReadsBothFormatsInOneStream(t *testing.T) {
	// What git prints for --raw --numstat -z: raw records first, then
	// the counts, every field NUL-terminated.
	raw := ":000000 100644 0000000 1111111 A\x00docs/new.md\x00" +
		":100644 100644 2222222 3333333 R087\x00src/old.go\x00src/new.go\x00" +
		":100644 000000 4444444 0000000 D\x00gone.txt\x00" +
		"3\t0\tdocs/new.md\x00" +
		"2\t1\t\x00src/old.go\x00src/new.go\x00" +
		"0\t5\tgone.txt\x00"

	files, err := ParseSummary([]byte(raw))
	if err != nil {
		t.Fatalf("ParseSummary: %v", err)
	}

	want := []File{
		{Path: "docs/new.md", Change: Added, Added: 3},
		{Path: "src/new.go", OldPath: "src/old.go", Change: Renamed, Added: 2, Deleted: 1},
		{Path: "gone.txt", Change: Deleted, Deleted: 5},
	}
	if !slices.EqualFunc(files, want, sameFile) {
		t.Errorf("files =\n%+v\nwant\n%+v", files, want)
	}
}

func TestParseSummaryMarksBinaryFiles(t *testing.T) {
	raw := ":000000 100644 0000000 1111111 A\x00logo.png\x00-\t-\tlogo.png\x00"

	files, err := ParseSummary([]byte(raw))
	if err != nil {
		t.Fatalf("ParseSummary: %v", err)
	}
	if len(files) != 1 || !files[0].Binary {
		t.Errorf("files = %+v, want one binary file", files)
	}
}

func TestParseSummaryOfNothing(t *testing.T) {
	// Two commits with the same tree: git prints nothing at all.
	files, err := ParseSummary(nil)
	if err != nil || len(files) != 0 {
		t.Errorf("ParseSummary(nil) = %v, %v; want nothing and no error", files, err)
	}
}

func TestParseSummaryRejectsWhatItDoesNotUnderstand(t *testing.T) {
	// U cannot appear between two commits. If it does, guessing would
	// be worse than saying so.
	raw := ":100644 100644 1111111 2222222 U\x00conflict.txt\x00"

	if _, err := ParseSummary([]byte(raw)); err == nil {
		t.Error("an unknown kind of change was accepted")
	}
}

func TestAttachHunksReadsQuotedPaths(t *testing.T) {
	// git quotes a path with anything unusual in it, with octal escapes
	// for bytes beyond ASCII. "Ü" is \303\234.
	files := []File{{Path: "docs/Übersicht.md", Change: Added}}
	patch := "diff --git \"a/docs/\\303\\234bersicht.md\" \"b/docs/\\303\\234bersicht.md\"\n" +
		"new file mode 100644\n" +
		"--- /dev/null\n" +
		"+++ \"b/docs/\\303\\234bersicht.md\"\n" +
		"@@ -0,0 +1,2 @@\n" +
		"+one\n+two\n"

	if err := AttachHunks(files, []byte(patch)); err != nil {
		t.Fatalf("AttachHunks: %v", err)
	}
	want := []Hunk{{Old: Range{0, 0}, New: Range{1, 2}}}
	if !slices.Equal(files[0].Hunks, want) {
		t.Errorf("hunks = %+v, want %+v", files[0].Hunks, want)
	}
}

func TestAttachHunksIgnoresTheTabAfterAPathWithASpace(t *testing.T) {
	// git appends a tab so that patch(1) can find the end of the name.
	// It is not part of the name.
	files := []File{{Path: "with space.md", Change: Modified}}
	patch := "--- a/with space.md\t\n+++ b/with space.md\t\n@@ -1 +1 @@\n-a\n+b\n"

	if err := AttachHunks(files, []byte(patch)); err != nil {
		t.Fatalf("AttachHunks: %v", err)
	}
	if len(files[0].Hunks) != 1 {
		t.Errorf("hunks = %+v, want one", files[0].Hunks)
	}
}

func TestAttachHunksKnowsADeletionByWhereItWas(t *testing.T) {
	files := []File{{Path: "gone.go", Change: Deleted}}
	patch := "--- a/gone.go\n+++ /dev/null\n@@ -1,3 +0,0 @@\n-a\n-b\n-c\n"

	if err := AttachHunks(files, []byte(patch)); err != nil {
		t.Fatalf("AttachHunks: %v", err)
	}
	want := []Hunk{{Old: Range{1, 3}, New: Range{0, 0}}}
	if !slices.Equal(files[0].Hunks, want) {
		t.Errorf("hunks = %+v, want %+v", files[0].Hunks, want)
	}
}

func TestParseHunkReadsTheAbbreviatedForm(t *testing.T) {
	// A missing count is git's shorthand for one line.
	tests := map[string]Hunk{
		"@@ -3 +3 @@":                     {Old: Range{3, 1}, New: Range{3, 1}},
		"@@ -10,0 +11,4 @@ func main() {": {Old: Range{10, 0}, New: Range{11, 4}},
		"@@ -7,2 +6,0 @@":                 {Old: Range{7, 2}, New: Range{6, 0}},
	}
	for header, want := range tests {
		t.Run(header, func(t *testing.T) {
			got, err := parseHunk(header)
			if err != nil {
				t.Fatalf("parseHunk: %v", err)
			}
			if got != want {
				t.Errorf("parseHunk = %+v, want %+v", got, want)
			}
		})
	}
}

func TestRangeEnd(t *testing.T) {
	if got := (Range{Start: 36, Count: 1}).End(); got != 36 {
		t.Errorf("End of one line = %d, want 36", got)
	}
	if got := (Range{Start: 11, Count: 4}).End(); got != 14 {
		t.Errorf("End of four lines = %d, want 14", got)
	}
	if got := (Range{Start: 6, Count: 0}).End(); got != 6 {
		t.Errorf("End of a position = %d, want 6", got)
	}
}

func sameFile(a, b File) bool {
	return a.Path == b.Path && a.OldPath == b.OldPath && a.Change == b.Change &&
		a.Binary == b.Binary && a.Added == b.Added && a.Deleted == b.Deleted
}
