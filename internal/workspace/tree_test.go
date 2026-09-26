package workspace

import (
	"context"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/thannoz/pit/internal/proc"
)

// counting runs commands and remembers which, so a test can see how
// many processes reading a tree took.
type counting struct {
	mu   sync.Mutex
	runs []string
}

func (c *counting) Output(ctx context.Context, cmd proc.Command) ([]byte, error) {
	c.mu.Lock()
	c.runs = append(c.runs, strings.Join(cmd.Args, " "))
	c.mu.Unlock()
	return proc.Exec{}.Output(ctx, cmd)
}

// A commit read as a file system is the commit: the standard library's
// own checker agrees with it, and every file reads as git has it --
// awkward names included.
func TestTreeIsTheCommit(t *testing.T) {
	r := newTestRepo(t)
	r.awkwardPullRequest(7)
	d := changesOf(t, r, 7)

	for _, commit := range []string{d.Base, d.Head} {
		tree, err := Tree(t.Context(), proc.Exec{}, r.Repo, commit)
		if err != nil {
			t.Fatalf("Tree: %v", err)
		}
		listed := strings.Split(strings.TrimSuffix(r.run(r.Repo.Root, "ls-tree", "-r", "-z", "--name-only", commit), "\x00"), "\x00")
		if err := fstest.TestFS(tree, listed...); err != nil {
			t.Errorf("%s: %v", short(commit), err)
		}
		for _, name := range listed {
			got, err := fs.ReadFile(tree, name)
			if err != nil {
				t.Errorf("%s: %v", name, err)
				continue
			}
			if want := r.run(r.Repo.Root, "show", commit+":"+name); string(got) != want {
				t.Errorf("%s: content differs from git show", name)
			}
		}
	}

	head, _ := Tree(t.Context(), proc.Exec{}, r.Repo, d.Head)
	names := []string{"docs/with space.md", "docs/Übersicht.md", "src/renamed.go"}
	if tabsInNames {
		names = append(names, "docs/tab\there.md")
	}
	for _, name := range names {
		if _, err := fs.Stat(head, name); err != nil {
			t.Errorf("head: %v", err)
		}
	}
	base, _ := Tree(t.Context(), proc.Exec{}, r.Repo, d.Base)
	if _, err := fs.Stat(base, "src/gone.go"); err != nil {
		t.Errorf("base has lost a file the pull request deletes: %v", err)
	}
	if _, err := fs.Stat(head, "src/gone.go"); err == nil {
		t.Error("head still has a file the pull request deletes")
	}
}

// Reading every Go file of a tree is one git process, not one each.
func TestTreeReadsAKindOfFileAtOnce(t *testing.T) {
	r := newTestRepo(t)
	for i := range 30 {
		put(t, filepath.Join(r.Remote, "pkg", string(rune('a'+i%26))+strings.Repeat("x", i/26), "f.go"), "package f\n")
	}
	put(t, filepath.Join(r.Remote, "README.md"), "# readme\n")
	git(t, r.Remote, "add", "-A")
	git(t, r.Remote, "commit", "--quiet", "-m", "thirty packages")
	commit := r.rev(r.Remote, "HEAD")

	c := &counting{}
	tree, err := Tree(t.Context(), c, Repo{Root: r.Remote}, commit)
	if err != nil {
		t.Fatalf("Tree: %v", err)
	}
	var read int
	err = fs.WalkDir(tree, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(p) != ".go" {
			return err
		}
		read++
		_, err = fs.ReadFile(tree, p)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if read != 30 {
		t.Fatalf("read %d Go files, want 30", read)
	}
	var catFiles int
	for _, run := range c.runs {
		if strings.HasPrefix(run, "cat-file") {
			catFiles++
		}
	}
	if catFiles != 1 {
		t.Errorf("%d git cat-file runs for 30 files, want 1: %q", catFiles, c.runs)
	}
	// Nothing else was fetched: the README was never asked for.
	if slices.ContainsFunc(c.runs, func(s string) bool { return strings.Contains(s, "README") }) {
		t.Error("fetched more than was read")
	}
}

func TestTreeOfACommitThatIsNotThere(t *testing.T) {
	r := newTestRepo(t)
	if _, err := Tree(t.Context(), proc.Exec{}, r.Repo, strings.Repeat("0", 40)); err == nil {
		t.Error("no error for a commit that does not exist")
	}
}
