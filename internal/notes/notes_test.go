package notes

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thannoz/pit/internal/inspect"
	"github.com/thannoz/pit/internal/state"
)

func newBook(t *testing.T) Book {
	t.Helper()
	dir := t.TempDir()
	store, err := state.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return Book{Dir: filepath.Join(dir, "notes", "acme-shop-c56680", "pr-482"), Repo: "github.com/acme/shop", PR: 482, Lock: store.Locked}
}

func TestNotesAreKeptInOrder(t *testing.T) {
	b := newBook(t)
	if list, err := b.List(); err != nil || len(list) != 0 {
		t.Fatalf("a new book: %v, %v", list, err)
	}
	if _, err := os.Stat(b.Dir); err == nil {
		t.Error("listing an empty book created its directory")
	}
	first, err := b.Add(Note{Text: "The refund total ignores the voucher", URL: "http://localhost:41234/orders/1001", SHA: "abc123",
		Problems: []inspect.Problem{{Kind: inspect.Exception, Level: "error", Text: "TypeError"}}}, []byte("\x89PNG one"))
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != 1 || first.Screenshot != filepath.Join(b.Dir, "1.png") {
		t.Errorf("first = %+v", first)
	}
	if got, err := os.ReadFile(first.Screenshot); err != nil || string(got) != "\x89PNG one" {
		t.Errorf("picture %q, %v", got, err)
	}
	if _, err := b.Add(Note{Text: "Totals overlap on a phone"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Add(Note{Text: "No way back from the order page", Uncaptured: "not asked to"}, nil); err != nil {
		t.Fatal(err)
	}

	list, err := b.List()
	if err != nil {
		t.Fatal(err)
	}
	var texts []string
	for i, n := range list {
		if n.ID != i+1 {
			t.Errorf("note %d has number %d", i+1, n.ID)
		}
		texts = append(texts, n.Text)
	}
	if strings.Join(texts, "|") != "The refund total ignores the voucher|Totals overlap on a phone|No way back from the order page" {
		t.Errorf("listed %q", texts)
	}
	if list[0].Screenshot != first.Screenshot || list[1].Screenshot != "" || len(list[0].Problems) != 1 {
		t.Errorf("listed %+v", list)
	}
	if !list[1].Captured() || list[2].Captured() {
		t.Error("Captured is wrong")
	}
}

// A removed note's number is not given to the next one: "note 2" in a
// conversation keeps meaning the same note.
func TestRemovedNumbersAreNotReused(t *testing.T) {
	b := newBook(t)
	for _, text := range []string{"one", "two"} {
		if _, err := b.Add(Note{Text: text}, []byte("png "+text)); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := b.Remove(2)
	if err != nil || len(removed) != 1 || removed[0].Text != "two" {
		t.Fatalf("removed %+v, %v", removed, err)
	}
	if _, err := os.Stat(removed[0].Screenshot); err == nil {
		t.Error("the removed note's picture is still there")
	}
	if list, _ := b.List(); len(list) != 1 || list[0].Text != "one" {
		t.Errorf("left %+v", list)
	}
	third, err := b.Add(Note{Text: "three"}, nil)
	if err != nil || third.ID != 3 {
		t.Errorf("third = %+v, %v", third, err)
	}
}

func TestRemovingANoteThatIsNotThere(t *testing.T) {
	b := newBook(t)
	if _, err := b.Add(Note{Text: "one"}, []byte("png")); err != nil {
		t.Fatal(err)
	}
	_, err := b.Remove(1, 7)
	if err == nil || !strings.Contains(err.Error(), "#482 has no note 7") {
		t.Errorf("err = %v", err)
	}
	if list, _ := b.List(); len(list) != 1 {
		t.Errorf("a failed removal removed something: %+v", list)
	}
}

// Two pits noting at once each get a number of their own.
func TestNotesTakenAtOnce(t *testing.T) {
	b := newBook(t)
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			if _, err := b.Add(Note{Text: "at once", At: time.Now()}, nil); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	list, err := b.List()
	if err != nil || len(list) != 10 {
		t.Fatalf("%d notes, %v", len(list), err)
	}
	seen := map[int]bool{}
	for _, n := range list {
		seen[n.ID] = true
	}
	if len(seen) != 10 {
		t.Errorf("numbers %v", seen)
	}
}

func TestAnUnreadableRecord(t *testing.T) {
	b := newBook(t)
	if err := os.MkdirAll(b.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(b.Dir, recordName), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := b.List(); err == nil || !strings.Contains(err.Error(), "is not readable") {
		t.Errorf("err = %v", err)
	}
}

func TestMarkPosted(t *testing.T) {
	b := newBook(t)
	for _, text := range []string{"one", "two", "three"} {
		if _, err := b.Add(Note{Text: text}, nil); err != nil {
			t.Fatal(err)
		}
	}
	const url = "https://github.com/acme/shop/pull/482#issuecomment-1"
	if err := b.MarkPosted([]int{1, 3}, url); err != nil {
		t.Fatal(err)
	}
	list, _ := b.List()
	if list[0].Posted != url || list[1].Posted != "" || list[2].Posted != url {
		t.Errorf("list = %+v", list)
	}
}
