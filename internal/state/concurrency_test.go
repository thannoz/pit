package state_test

import (
	"os"
	"os/exec"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/thannoz/pit/internal/state"
)

// The subprocess half of TestTwoProcessesDoNotCorruptTheState. When the
// environment variable is set, the test binary re-runs this one test as
// a separate process, which is the only honest way to exercise a file
// lock meant to hold between processes.
const (
	writerEnv    = "PIT_STATE_TEST_WRITER"
	writerDirEnv = "PIT_STATE_TEST_DIR"
	writersCount = 8
	perWriter    = 25
)

func TestStateWriterHelper(t *testing.T) {
	id := os.Getenv(writerEnv)
	if id == "" {
		t.Skip("not the subprocess")
	}

	store, err := state.Open(os.Getenv(writerDirEnv))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	n, err := strconv.Atoi(id)
	if err != nil {
		t.Fatalf("bad writer id %q", id)
	}

	for i := range perWriter {
		err := store.Update(func(f *state.File) error {
			f.Put(state.Sandbox{
				PR:        n*1000 + i,
				RepoRef:   "acme-shop-c56680",
				Repo:      "github.com/acme/shop",
				CreatedAt: time.Now(),
			})
			return nil
		})
		if err != nil {
			t.Fatalf("Update: %v", err)
		}
	}
}

// TestTwoProcessesDoNotCorruptTheState is the acceptance criterion for
// T-307. Eight processes each add 25 entries to the same file; all 200
// have to survive, and the file has to stay readable.
func TestTwoProcessesDoNotCorruptTheState(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping: starts subprocesses")
	}

	dir := t.TempDir()
	store, err := state.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	var wg sync.WaitGroup
	errCh := make(chan error, writersCount)

	for w := range writersCount {
		wg.Add(1)
		go func() {
			defer wg.Done()

			cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=TestStateWriterHelper") //nolint:gosec // the binary is this test
			cmd.Env = append(os.Environ(),
				writerEnv+"="+strconv.Itoa(w),
				writerDirEnv+"="+dir,
			)
			if out, err := cmd.CombinedOutput(); err != nil {
				errCh <- &writerError{w: w, err: err, out: string(out)}
			}
		}()
	}

	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Errorf("%v", err)
	}

	f, err := store.Load()
	if err != nil {
		t.Fatalf("the state is unreadable after concurrent writes: %v", err)
	}

	want := writersCount * perWriter
	if len(f.Sandboxes) != want {
		t.Errorf("the state holds %d sandboxes, want %d -- entries were lost to a lost update",
			len(f.Sandboxes), want)
	}

	// Every writer's entries must all be there, not just some.
	seen := map[int]int{}
	for _, s := range f.Sandboxes {
		seen[s.PR/1000]++
	}
	for w := range writersCount {
		if seen[w] != perWriter {
			t.Errorf("writer %d has %d of its %d entries", w, seen[w], perWriter)
		}
	}
}

type writerError struct {
	w   int
	err error
	out string
}

func (e *writerError) Error() string {
	return "writer " + strconv.Itoa(e.w) + " failed: " + e.err.Error() + "\n" + e.out
}

// TestConcurrentUpdatesInOneProcess covers the same hazard within a
// single pit: flock is per open file description, so two Stores in one
// process exclude each other just as two processes do.
func TestConcurrentUpdatesInOneProcess(t *testing.T) {
	dir := t.TempDir()

	var wg sync.WaitGroup
	for w := range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()

			store, err := state.Open(dir)
			if err != nil {
				t.Errorf("Open: %v", err)
				return
			}
			for i := range 20 {
				err := store.Update(func(f *state.File) error {
					f.Put(state.Sandbox{PR: w*100 + i, RepoRef: "r", CreatedAt: time.Now()})
					return nil
				})
				if err != nil {
					t.Errorf("Update: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()

	store, err := state.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	f, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(f.Sandboxes) != 200 {
		t.Errorf("the state holds %d sandboxes, want 200", len(f.Sandboxes))
	}
}
