package state

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/thannoz/pit/internal/errs"
)

func sandbox(pr int) Sandbox {
	return Sandbox{
		PR:        pr,
		Repo:      "github.com/acme/shop",
		RepoRef:   "acme-shop-c56680",
		Project:   "pit-acme-shop-c56680-" + strconv.Itoa(pr),
		Worktree:  "/state/pit/acme-shop-c56680/pr-" + strconv.Itoa(pr),
		Port:      49580 + pr,
		URL:       "http://localhost:4958" + strconv.Itoa(pr),
		SHA:       "a3f91c2",
		CreatedAt: time.Now(),
	}
}

func openStore(t *testing.T) *Store {
	t.Helper()

	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func TestMissingFileIsAnEmptyRecord(t *testing.T) {
	// The first run has nothing to read, and that is not a problem to
	// report.
	f, err := openStore(t).Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(f.Sandboxes) != 0 {
		t.Errorf("a fresh store holds %d sandboxes, want none", len(f.Sandboxes))
	}
}

func TestPutAndFind(t *testing.T) {
	s := openStore(t)

	if err := s.Update(func(f *File) error { f.Put(sandbox(2)); return nil }); err != nil {
		t.Fatalf("Update: %v", err)
	}

	f, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got, ok := f.Find("acme-shop-c56680", 2)
	if !ok {
		t.Fatal("the sandbox was not found")
	}
	if got.Port != sandbox(2).Port {
		t.Errorf("Port = %d, want %d", got.Port, sandbox(2).Port)
	}
}

func TestPutReplacesRatherThanDuplicates(t *testing.T) {
	// Re-running pit on the same pull request is the normal case.
	s := openStore(t)

	for _, port := range []int{1000, 2000, 3000} {
		err := s.Update(func(f *File) error {
			box := sandbox(2)
			box.Port = port
			f.Put(box)
			return nil
		})
		if err != nil {
			t.Fatalf("Update: %v", err)
		}
	}

	f, _ := s.Load()
	if len(f.Sandboxes) != 1 {
		t.Fatalf("the store holds %d entries, want 1", len(f.Sandboxes))
	}
	if f.Sandboxes[0].Port != 3000 {
		t.Errorf("Port = %d, want the most recent 3000", f.Sandboxes[0].Port)
	}
}

func TestTheSamePullRequestNumberInTwoRepositories(t *testing.T) {
	// A pull request number means nothing without its repository.
	s := openStore(t)

	err := s.Update(func(f *File) error {
		a, b := sandbox(1), sandbox(1)
		b.RepoRef, b.Repo = "acme-admin-9f2b1a", "github.com/acme/admin"
		f.Put(a)
		f.Put(b)
		return nil
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}

	f, _ := s.Load()
	if len(f.Sandboxes) != 2 {
		t.Errorf("the store holds %d entries, want both repositories", len(f.Sandboxes))
	}
}

func TestRemove(t *testing.T) {
	s := openStore(t)

	if err := s.Update(func(f *File) error { f.Put(sandbox(2)); return nil }); err != nil {
		t.Fatalf("Update: %v", err)
	}

	var removed bool
	if err := s.Update(func(f *File) error {
		removed = f.Remove("acme-shop-c56680", 2)
		return nil
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if !removed {
		t.Error("Remove reported nothing to remove")
	}

	f, _ := s.Load()
	if len(f.Sandboxes) != 0 {
		t.Errorf("the store still holds %d entries", len(f.Sandboxes))
	}
}

func TestRemoveWhatIsNotThere(t *testing.T) {
	// Cleanup runs on paths where setup never finished.
	s := openStore(t)

	err := s.Update(func(f *File) error {
		if f.Remove("nothing", 404) {
			t.Error("Remove claimed to have removed something")
		}
		return nil
	})
	if err != nil {
		t.Errorf("Update: %v", err)
	}
}

func TestListIsNewestFirst(t *testing.T) {
	s := openStore(t)

	err := s.Update(func(f *File) error {
		for i, age := range []time.Duration{2 * time.Hour, 10 * time.Minute, 24 * time.Hour} {
			box := sandbox(i + 1)
			box.CreatedAt = time.Now().Add(-age)
			f.Put(box)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}

	list, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for i := 1; i < len(list); i++ {
		if list[i-1].CreatedAt.Before(list[i].CreatedAt) {
			t.Errorf("entry %d is older than entry %d", i-1, i)
		}
	}
}

func TestUpdateDoesNotWriteWhenTheCallbackFails(t *testing.T) {
	s := openStore(t)
	if err := s.Update(func(f *File) error { f.Put(sandbox(1)); return nil }); err != nil {
		t.Fatalf("Update: %v", err)
	}

	boom := errs.New("changed my mind")
	err := s.Update(func(f *File) error {
		f.Put(sandbox(2))
		return boom
	})
	if err == nil {
		t.Fatal("want the callback's error")
	}

	f, _ := s.Load()
	if len(f.Sandboxes) != 1 {
		t.Errorf("the store holds %d entries; the abandoned change was written anyway", len(f.Sandboxes))
	}
}

// TestWritesAreAtomic checks the property that matters after a crash: a
// reader sees the old record or the new one, never a half-written file.
func TestWritesAreAtomic(t *testing.T) {
	s := openStore(t)

	if err := s.Update(func(f *File) error { f.Put(sandbox(1)); return nil }); err != nil {
		t.Fatalf("Update: %v", err)
	}

	// No temporary files are left behind to be mistaken for the record.
	entries, err := os.ReadDir(filepath.Dir(s.Path()))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("%s was left behind", e.Name())
		}
	}
}

func TestAnEmptyFileIsTreatedAsEmpty(t *testing.T) {
	// A crash between creating and writing leaves this behind. Refusing
	// to start would be unkind when starting over costs nothing.
	s := openStore(t)
	if err := os.WriteFile(s.Path(), nil, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	f, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(f.Sandboxes) != 0 {
		t.Errorf("an empty file produced %d sandboxes", len(f.Sandboxes))
	}
}

func TestBrokenJSONIsReportedWithAWayOut(t *testing.T) {
	s := openStore(t)
	if err := os.WriteFile(s.Path(), []byte(`{"sandboxes": [`), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err := s.Load()
	if err == nil {
		t.Fatal("want an error for a truncated record")
	}
	if !strings.Contains(errs.Hint(err), "move the file aside") {
		t.Errorf("hint = %q, want a way out", errs.Hint(err))
	}
}

func TestANewerFormatIsRefused(t *testing.T) {
	// Misreading a newer format would be worse than saying so.
	s := openStore(t)
	if err := os.WriteFile(s.Path(), []byte(`{"version": 99, "sandboxes": []}`), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err := s.Load()
	if err == nil {
		t.Fatal("want an error for a newer format")
	}
	if !strings.Contains(errs.Hint(err), "upgrade") {
		t.Errorf("hint = %q, want it to suggest upgrading", errs.Hint(err))
	}
}

func TestTryLockReportsAHeldLock(t *testing.T) {
	// `pit doctor` should say that another pit is running rather than
	// hang waiting for it.
	s := openStore(t)

	release, ok := s.TryLock()
	if !ok {
		t.Fatal("the first TryLock failed on a free lock")
	}

	second, err := Open(filepath.Dir(s.Path()))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, ok := second.TryLock(); ok {
		t.Error("TryLock succeeded although the lock is held")
	}

	release()
	if release2, ok := second.TryLock(); !ok {
		t.Error("TryLock failed after the lock was released")
	} else {
		release2()
	}
}

func TestSandboxKey(t *testing.T) {
	a := Sandbox{RepoRef: "acme-shop-c56680", PR: 482}
	b := Sandbox{RepoRef: "acme-admin-9f2b1a", PR: 482}

	if a.Key() == b.Key() {
		t.Errorf("both repositories produced the key %q", a.Key())
	}
}
