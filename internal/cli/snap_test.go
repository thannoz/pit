package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/proc"
	"github.com/thannoz/pit/internal/sandbox"
	"github.com/thannoz/pit/internal/state"
)

// dumper stands in for the repository's save command: it writes a dump
// and remembers what it was asked to run.
type dumper struct {
	mu   sync.Mutex
	ran  []proc.Command
	dump string
	fail error
}

func (d *dumper) Stream(_ context.Context, c proc.Command, stdout, _ io.Writer) error {
	d.mu.Lock()
	d.ran = append(d.ran, c)
	d.mu.Unlock()
	if d.fail != nil {
		return d.fail
	}
	_, err := io.WriteString(stdout, d.dump)
	return err
}

const snapCompose = "services:\n  web:\n    image: nginx\n  db:\n    image: postgres:17-alpine\n"

// snapBox is a sandbox with a worktree and a repository on disk, each
// holding a .pit.yaml, as the snapshot commands are read from them.
func snapBox(t *testing.T, worktreeYAML, repoYAML string) state.Sandbox {
	t.Helper()
	box := recorded(482, "github.com/acme/shop", "acme-shop-c56680", "refunds", time.Minute)
	box.Worktree, box.RepoRoot = t.TempDir(), t.TempDir()
	box.SHA = "a3f91c2e4b7d"
	box.Scenario = "standard"
	for dir, yaml := range map[string]string{box.Worktree: worktreeYAML, box.RepoRoot: repoYAML} {
		files := map[string]string{"docker-compose.yml": snapCompose}
		if yaml != "" {
			files[".pit.yaml"] = yaml
		}
		for name, content := range files {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	box.ComposeFiles = []string{filepath.Join(box.Worktree, "docker-compose.yml")}
	return box
}

func snapManager(t *testing.T, box state.Sandbox, running bool) (*sandbox.Manager, *dumper) {
	t.Helper()
	m, fake := withManager(t, box)
	fake.Declared = []string{"web", "db"}
	if running {
		if err := fake.Up(t.Context(), sandbox.RuntimeSandbox(box), nil, io.Discard, io.Discard); err != nil {
			t.Fatal(err)
		}
	}
	d := &dumper{dump: "-- PostgreSQL database dump\nCOPY public.orders FROM stdin;\n"}
	m.Proc = d
	return m, d
}

const withSnapshot = `version: 1
web:
  service: web
  port: 80
data:
  snapshot:
    save: "compose exec -T db pg_dump -U app app"
    restore: "compose exec -T db psql -U app -d app"
`

const withoutSnapshot = "version: 1\nweb:\n  service: web\n  port: 80\n"

// TestSnapSaveMakesASnapshot is the acceptance criterion for T-702:
// the snapshot exists, and its size and the time it took are said.
func TestSnapSaveMakesASnapshot(t *testing.T) {
	box := snapBox(t, withSnapshot, "")
	m, d := snapManager(t, box, true)

	stdout, stderr, err := run(t, "snap", "save", "482", "cart-with-voucher")
	if err != nil {
		t.Fatalf("pit snap save: %v\n%s", err, stderr)
	}
	id := strings.TrimSpace(stdout)
	if !strings.HasPrefix(id, "sn_") {
		t.Fatalf("stdout = %q, want the ID alone", stdout)
	}
	for _, want := range []string{"✓ snapshot", "cart-with-voucher (" + id + ")", " B, ", "ms"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, stderr)
		}
	}

	list, err := m.Snapshots(box).List()
	if err != nil || len(list) != 1 {
		t.Fatalf("List = %v, %v", list, err)
	}
	s := list[0]
	if s.ID != id || s.Name != "cart-with-voucher" || s.PR != 482 || s.SHA != box.SHA || s.Scenario != "standard" || s.Service != "db" {
		t.Errorf("recorded %+v", s)
	}
	if s.Raw != int64(len(d.dump)) {
		t.Errorf("Raw = %d, want %d", s.Raw, len(d.dump))
	}

	// The command ran in the sandbox, through its own compose project.
	if len(d.ran) != 1 {
		t.Fatalf("ran %d commands", len(d.ran))
	}
	args := strings.Join(d.ran[0].Args, " ")
	if d.ran[0].Name != "docker" || !strings.Contains(args, "--project-name "+box.Project) || !strings.HasSuffix(args, "exec -T db pg_dump -U app app") {
		t.Errorf("ran %s %s", d.ran[0].Name, args)
	}
}

func TestSnapSaveWithoutCommandsSaysWhatToAdd(t *testing.T) {
	box := snapBox(t, withoutSnapshot, withoutSnapshot)
	_, d := snapManager(t, box, true)

	_, _, err := run(t, "snap", "save", "482")
	if err == nil {
		t.Fatal("no error")
	}
	hint := errs.Hint(err)
	for _, want := range []string{"db is PostgreSQL", "pg_dump", "save: >-"} {
		if !strings.Contains(hint, want) {
			t.Errorf("hint lacks %q:\n%s", want, hint)
		}
	}
	if len(d.ran) != 0 {
		t.Errorf("ran %v", d.ran)
	}
}

// The pull request's .pit.yaml governs its sandbox, but when it says
// nothing about snapshots the reviewer's own file is asked: that is
// where the lines pit suggests get added.
func TestSnapSaveFallsBackToTheReviewersCommands(t *testing.T) {
	box := snapBox(t, withoutSnapshot, withSnapshot)
	_, d := snapManager(t, box, true)

	if _, stderr, err := run(t, "snap", "save", "482"); err != nil {
		t.Fatalf("pit snap save: %v\n%s", err, stderr)
	}
	if len(d.ran) != 1 || !strings.Contains(strings.Join(d.ran[0].Args, " "), "pg_dump -U app app") {
		t.Errorf("ran %v", d.ran)
	}
}

func TestSnapSaveNeedsARunningSandbox(t *testing.T) {
	box := snapBox(t, withSnapshot, "")
	_, d := snapManager(t, box, false)

	_, _, err := run(t, "snap", "save", "482")
	if err == nil || !strings.Contains(err.Error(), "nothing is running") {
		t.Errorf("err = %v", err)
	}
	if len(d.ran) != 0 {
		t.Errorf("ran %v", d.ran)
	}
}

func TestSnapSaveChecksTheNameFirst(t *testing.T) {
	box := snapBox(t, withSnapshot, "")
	_, d := snapManager(t, box, true)

	_, _, err := run(t, "snap", "save", "482", "Cart With Voucher")
	if err == nil || errs.Hint(err) == "" {
		t.Errorf("err = %v, want one with a hint", err)
	}
	if len(d.ran) != 0 {
		t.Errorf("ran %v", d.ran)
	}
}

func TestSnapSaveJSON(t *testing.T) {
	box := snapBox(t, withSnapshot, "")
	snapManager(t, box, true)

	stdout, _, err := run(t, "--json", "snap", "save", "482")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"id": "sn_`, `"pr": 482`, `"size": `, `"raw": `, `"tookMs": `, `"createdAt": `} {
		if !strings.Contains(stdout, want) {
			t.Errorf("JSON lacks %q:\n%s", want, stdout)
		}
	}
}

// The pull request's own commands come first: its containers were
// brought up with its file.
func TestSnapSavePrefersThePullRequestsCommands(t *testing.T) {
	theirs := strings.ReplaceAll(withSnapshot, "pg_dump -U app app", "pg_dump -U app shop_v2")
	box := snapBox(t, theirs, withSnapshot)
	_, d := snapManager(t, box, true)

	if _, stderr, err := run(t, "snap", "save", "482"); err != nil {
		t.Fatalf("pit snap save: %v\n%s", err, stderr)
	}
	if len(d.ran) != 1 || !strings.Contains(strings.Join(d.ran[0].Args, " "), "shop_v2") {
		t.Errorf("ran %v, want the pull request's command", d.ran)
	}
}

// A save command that fails is the repository's, run in containers pit
// cannot see into from here; the hint says where to look.
func TestSnapSaveThatFailsSaysWhereToLook(t *testing.T) {
	box := snapBox(t, withSnapshot, "")
	m, d := snapManager(t, box, true)
	d.fail = errors.New(`service "db" is not running`)

	_, _, err := run(t, "snap", "save", "482")
	if err == nil {
		t.Fatal("no error")
	}
	if hint := errs.Hint(err); !strings.Contains(hint, "pit ls") || !strings.Contains(hint, "pit logs 482") {
		t.Errorf("hint = %q", hint)
	}
	if list, _ := m.Snapshots(box).List(); len(list) != 0 {
		t.Errorf("a failed save left %v", list)
	}
}
