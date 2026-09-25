package snapshot

import (
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/thannoz/pit/internal/errs"
)

func store(t *testing.T) Store {
	t.Helper()
	clock := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	return Store{
		Dir: filepath.Join(t.TempDir(), "snapshots", "acme-shop-c56680"),
		// Each reading of the clock is a second later, so that Took
		// and CreatedAt have something to show.
		Now: func() time.Time { clock = clock.Add(time.Second); return clock },
	}
}

func writes(s string) func(context.Context, io.Writer) error {
	return func(_ context.Context, w io.Writer) error {
		_, err := io.WriteString(w, s)
		return err
	}
}

// files lists what is in the store's directory, hidden files included.
func files(t *testing.T, s Store) []string {
	t.Helper()
	entries, err := os.ReadDir(s.Dir)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func TestSaveKeepsWhatTheCommandWrote(t *testing.T) {
	s := store(t)
	dump := strings.Repeat("INSERT INTO orders VALUES (1001, 'Sencha');\n", 500)

	snap, err := s.Save(t.Context(), Snapshot{Name: "cart-with-voucher", PR: 482, SHA: "abc", Scenario: "standard", Service: "db"}, One(writes(dump)))
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if !strings.HasPrefix(snap.ID, "sn_") || len(snap.ID) != len("sn_7f3a1b") {
		t.Errorf("ID = %q", snap.ID)
	}
	if snap.Raw != int64(len(dump)) {
		t.Errorf("Raw = %d, want %d", snap.Raw, len(dump))
	}
	if snap.Size <= 0 || snap.Size >= snap.Raw {
		t.Errorf("Size = %d; a repetitive dump of %d bytes compresses", snap.Size, snap.Raw)
	}
	if snap.Took != time.Second {
		t.Errorf("Took = %v, want the one second between the clock's readings", snap.Took)
	}
	if snap.CreatedAt.IsZero() || snap.PR != 482 || snap.SHA != "abc" || snap.Scenario != "standard" || snap.Service != "db" {
		t.Errorf("record = %+v", snap)
	}

	// The data is the command's output, compressed.
	f, err := os.Open(s.DataPath(snap))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	z, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("not gzip: %v", err)
	}
	got, err := io.ReadAll(z)
	if err != nil || string(got) != dump {
		t.Errorf("data differs from what was written (%d bytes, %v)", len(got), err)
	}

	// And it is found again, as it was recorded.
	list, err := s.List()
	if err != nil || len(list) != 1 || !reflect.DeepEqual(list[0], snap) {
		t.Errorf("List = %+v, %v; want the one saved", list, err)
	}
	if want := []string{snap.ID + ".gz", snap.ID + ".json"}; !slices.Equal(files(t, s), want) {
		t.Errorf("files = %v, want %v", files(t, s), want)
	}
}

// A snapshot that exists is a whole one. Whatever stops the command --
// a failure, nothing written, the reviewer pressing ctrl-c -- leaves
// the directory as it was.
func TestSaveLeavesNothingBehind(t *testing.T) {
	boom := errors.New("pg_dump: connection refused")
	for name, dump := range map[string]func(context.Context, io.Writer) error{
		"failed": func(_ context.Context, w io.Writer) error {
			_, _ = io.WriteString(w, "-- half a dump\n")
			return boom
		},
		"wrote nothing": writes(""),
		"cancelled": func(ctx context.Context, w io.Writer) error {
			_, _ = io.WriteString(w, "-- half a dump\n")
			<-ctx.Done()
			return ctx.Err()
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := store(t)
			if _, err := s.Save(t.Context(), Snapshot{}, One(writes("-- an earlier one\n"))); err != nil {
				t.Fatal(err)
			}
			before := files(t, s)

			ctx, cancel := context.WithCancel(t.Context())
			if name == "cancelled" {
				cancel()
			}
			defer cancel()
			_, err := s.Save(ctx, Snapshot{Name: "kept"}, One(dump))
			if err == nil {
				t.Fatal("no error")
			}
			if after := files(t, s); !slices.Equal(after, before) {
				t.Errorf("files after = %v, want %v", after, before)
			}
			if name == "wrote nothing" && !strings.Contains(errs.Hint(err), "stdout") {
				t.Errorf("hint = %q; it should say where the dump goes", errs.Hint(err))
			}
			if name == "failed" && !errors.Is(err, boom) {
				t.Errorf("err = %v, want the command's own", err)
			}
		})
	}
}

func TestSaveRefusesATakenName(t *testing.T) {
	s := store(t)
	first, err := s.Save(t.Context(), Snapshot{Name: "cart", PR: 7}, One(writes("a\n")))
	if err != nil {
		t.Fatal(err)
	}
	ran := false
	_, err = s.Save(t.Context(), Snapshot{Name: "cart"}, One(func(context.Context, io.Writer) error { ran = true; return nil }))
	if err == nil || !strings.Contains(err.Error(), first.ID) || !strings.Contains(err.Error(), "#7") {
		t.Errorf("err = %v; it should name the snapshot that has the name", err)
	}
	if ran {
		t.Error("the save command ran although the name was taken")
	}
	// Without a name there is nothing to collide with.
	if _, err := s.Save(t.Context(), Snapshot{}, One(writes("b\n"))); err != nil {
		t.Errorf("unnamed: %v", err)
	}
}

func TestCheckName(t *testing.T) {
	for _, ok := range []string{"cart", "cart-with-voucher", "v2.1", "refund_two_warehouses", "42"} {
		if err := CheckName(ok); err != nil {
			t.Errorf("CheckName(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"", "Cart", "-cart", "cart with voucher", "a/b", "sn_7f3a1b", "../x"} {
		if err := CheckName(bad); err == nil || errs.Hint(err) == "" {
			t.Errorf("CheckName(%q) = %v, want an error with a hint", bad, err)
		}
	}
}

func TestList(t *testing.T) {
	s := store(t)
	if list, err := s.List(); err != nil || len(list) != 0 {
		t.Errorf("before any: %v, %v", list, err)
	}
	var ids []string
	for range 3 {
		snap, err := s.Save(t.Context(), Snapshot{}, One(writes("x\n")))
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, snap.ID)
	}
	// Data without a record is not a snapshot: the record is written
	// last, so this is one whose saving did not finish.
	if err := os.WriteFile(filepath.Join(s.Dir, "sn_000000.gz"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	list, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, snap := range list {
		got = append(got, snap.ID)
	}
	if !slices.Equal(got, ids) {
		t.Errorf("List = %v, want %v, oldest first", got, ids)
	}

	if err := os.WriteFile(filepath.Join(s.Dir, ids[1]+".json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.List(); err == nil || !strings.Contains(errs.Hint(err), "damaged") {
		t.Errorf("a damaged record: %v", err)
	}
}

func TestLabel(t *testing.T) {
	if got := (Snapshot{ID: "sn_1", Name: "cart"}).Label(); got != "cart (sn_1)" {
		t.Errorf("named: %q", got)
	}
	if got := (Snapshot{ID: "sn_1"}).Label(); got != "sn_1" {
		t.Errorf("unnamed: %q", got)
	}
}

func TestFindByIDOrName(t *testing.T) {
	s := store(t)
	if _, err := s.Find("cart"); err == nil || !strings.Contains(errs.Hint(err), "pit snap save") {
		t.Errorf("none yet: %v, hint %q", err, errs.Hint(err))
	}
	named, err := s.Save(t.Context(), Snapshot{Name: "cart"}, One(writes("a\n")))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := s.Save(t.Context(), Snapshot{}, One(writes("b\n")))
	if err != nil {
		t.Fatal(err)
	}
	for ref, want := range map[string]string{"cart": named.ID, named.ID: named.ID, plain.ID: plain.ID} {
		if got, err := s.Find(ref); err != nil || got.ID != want {
			t.Errorf("Find(%q) = %v, %v; want %s", ref, got.ID, err, want)
		}
	}
	// An unnamed snapshot is not found by the empty name.
	if _, err := s.Find(""); err == nil {
		t.Error(`Find("") found something`)
	}
	_, err = s.Find("carts")
	if err == nil || !strings.Contains(errs.Hint(err), "cart ("+named.ID+")") {
		t.Errorf("missing: %v, hint %q; it should name the ones there are", err, errs.Hint(err))
	}
}

func TestOpenGivesBackWhatWasSaved(t *testing.T) {
	s := store(t)
	dump := "-- dump\nCOPY orders FROM stdin;\n1001\tSencha\n\\.\n"
	snap, err := s.Save(t.Context(), Snapshot{}, One(writes(dump)))
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.Open(snap, "")
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(r)
	if err != nil || string(got) != dump {
		t.Errorf("read %q, %v; want %q", got, err, dump)
	}
	if err := r.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}

	if err := os.Remove(s.DataPath(snap)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Open(snap, ""); err == nil || errs.Hint(err) == "" {
		t.Errorf("data gone: %v", err)
	}
}

func TestRemove(t *testing.T) {
	s := store(t)
	keep, err := s.Save(t.Context(), Snapshot{Name: "keep"}, One(writes("a\n")))
	if err != nil {
		t.Fatal(err)
	}
	gone, err := s.Save(t.Context(), Snapshot{Name: "gone"}, One(writes("b\n")))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Remove(gone); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if want := []string{keep.ID + ".gz", keep.ID + ".json"}; !slices.Equal(files(t, s), want) {
		t.Errorf("files = %v, want only %v", files(t, s), want)
	}

	// Data already gone is not a reason to keep the record.
	if err := os.Remove(s.DataPath(keep)); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove(keep); err != nil {
		t.Errorf("Remove without data: %v", err)
	}
	if list, _ := s.List(); len(list) != 0 {
		t.Errorf("List = %v", list)
	}
	if err := s.Remove(keep); err == nil {
		t.Error("removing a snapshot twice succeeded")
	}
}

func TestStores(t *testing.T) {
	root := filepath.Join(t.TempDir(), "snapshots")
	if stores, err := Stores(root); err != nil || len(stores) != 0 {
		t.Errorf("before any: %v, %v", stores, err)
	}
	for _, repo := range []string{"acme-shop-c56680", "acme-blog-0a1b2c"} {
		mkdirAll(t, filepath.Join(root, repo))
	}
	if err := os.WriteFile(filepath.Join(root, "stray"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	stores, err := Stores(root)
	if err != nil {
		t.Fatal(err)
	}
	var dirs []string
	for _, s := range stores {
		dirs = append(dirs, filepath.Base(s.Dir))
	}
	if want := []string{"acme-blog-0a1b2c", "acme-shop-c56680"}; !slices.Equal(dirs, want) {
		t.Errorf("stores = %v, want %v", dirs, want)
	}
}

func mkdirAll(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
}

// A snapshot of several databases is one file each, all or nothing.
func TestSaveSeveralParts(t *testing.T) {
	s := store(t)
	snap, err := s.Save(t.Context(), Snapshot{Name: "two"}, []Dump{
		{Service: "db", Write: writes("-- postgres\n")},
		{Service: "analytics", Write: writes("-- mysql, a little longer\n")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Parts) != 2 || snap.Parts[0].Service != "db" || snap.Parts[1].Service != "analytics" {
		t.Fatalf("Parts = %+v", snap.Parts)
	}
	if snap.Raw != int64(len("-- postgres\n")+len("-- mysql, a little longer\n")) || snap.Size != snap.Parts[0].Size+snap.Parts[1].Size {
		t.Errorf("Raw %d, Size %d, parts %+v", snap.Raw, snap.Size, snap.Parts)
	}
	for service, want := range map[string]string{"db": "-- postgres\n", "analytics": "-- mysql, a little longer\n"} {
		r, err := s.Open(snap, service)
		if err != nil {
			t.Fatal(err)
		}
		got, _ := io.ReadAll(r)
		_ = r.Close()
		if string(got) != want {
			t.Errorf("%s = %q, want %q", service, got, want)
		}
	}
	want := []string{snap.ID + ".analytics.gz", snap.ID + ".db.gz", snap.ID + ".json"}
	if got := files(t, s); !slices.Equal(got, want) {
		t.Errorf("files = %v, want %v", got, want)
	}

	if err := s.Remove(snap); err != nil {
		t.Fatal(err)
	}
	if got := files(t, s); len(got) != 0 {
		t.Errorf("after Remove: %v", got)
	}
}

// The second database failing leaves the first one's dump behind no
// more than its own.
func TestSaveSeveralPartsIsAllOrNothing(t *testing.T) {
	s := store(t)
	_, err := s.Save(t.Context(), Snapshot{}, []Dump{
		{Service: "db", Write: writes("-- postgres\n")},
		{Service: "analytics", Write: func(_ context.Context, w io.Writer) error {
			_, _ = io.WriteString(w, "-- half\n")
			return errors.New("mysqldump: access denied")
		}},
	})
	if err == nil {
		t.Fatal("no error")
	}
	if got := files(t, s); len(got) != 0 {
		t.Errorf("left behind: %v", got)
	}

	_, err = s.Save(t.Context(), Snapshot{}, []Dump{
		{Service: "db", Write: writes("-- postgres\n")},
		{Service: "analytics", Write: writes("")},
	})
	if err == nil || !strings.Contains(err.Error(), "analytics") {
		t.Errorf("an empty part: %v; it should name the service", err)
	}
	if got := files(t, s); len(got) != 0 {
		t.Errorf("left behind: %v", got)
	}
}

// A snapshot saved before parts were recorded is one part, its data the
// single file pit wrote then.
func TestASnapshotFromBeforePartsIsOnePart(t *testing.T) {
	s := store(t)
	mkdirAll(t, s.Dir)
	record := `{"id": "sn_0a1b2c", "pr": 7, "sha": "abc", "size": 30, "raw": 11, "took": 1000000, "createdAt": "2026-09-24T12:00:00Z"}`
	if err := os.WriteFile(filepath.Join(s.Dir, "sn_0a1b2c.json"), []byte(record), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(s.Dir, "sn_0a1b2c.gz"))
	if err != nil {
		t.Fatal(err)
	}
	z := gzip.NewWriter(f)
	_, _ = io.WriteString(z, "-- old dump\n")
	_ = z.Close()
	_ = f.Close()

	snap, err := s.Find("sn_0a1b2c")
	if err != nil {
		t.Fatal(err)
	}
	if pieces := snap.Pieces(); len(pieces) != 1 || pieces[0].Service != "" {
		t.Errorf("Pieces = %+v", pieces)
	}
	r, err := s.Open(snap, "")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(r)
	_ = r.Close()
	if string(got) != "-- old dump\n" {
		t.Errorf("read %q", got)
	}
	if err := s.Remove(snap); err != nil || len(files(t, s)) != 0 {
		t.Errorf("Remove: %v, left %v", err, files(t, s))
	}
}

// The limit is for the whole snapshot: a second part that takes it past
// stops it, and neither part is kept.
func TestSaveStopsAtTheLimit(t *testing.T) {
	s := store(t)
	s.Limit = 100
	_, err := s.Save(t.Context(), Snapshot{PR: 1, SHA: "abc"}, []Dump{
		{Service: "orders", Write: writes(strings.Repeat("o", 60))},
		{Service: "stock", Write: writes(strings.Repeat("s", 60))},
	})
	if err == nil || !strings.Contains(err.Error(), "the snapshot grew past 100 B and was stopped") {
		t.Fatalf("err = %v", err)
	}
	if left := files(t, s); len(left) != 0 {
		t.Errorf("left %v", left)
	}

	// Under it, as before.
	s.Limit = 1000
	if _, err := s.Save(t.Context(), Snapshot{PR: 1, SHA: "abc"}, One(writes(strings.Repeat("x", 900)))); err != nil {
		t.Errorf("under the limit: %v", err)
	}
}

// The command writing is stopped where it passes the limit: its writes
// fail and its context ends, so it is not left to write gigabytes that
// go nowhere.
func TestSaveStopsTheWriterAtTheLimit(t *testing.T) {
	s := store(t)
	s.Limit = 10_000
	var attempted int
	var cancelled bool
	_, err := s.Save(t.Context(), Snapshot{PR: 1, SHA: "abc"}, One(func(ctx context.Context, w io.Writer) error {
		chunk := []byte(strings.Repeat("x", 1000))
		for range 10_000 { // 10 MB, if nobody stops it
			attempted += len(chunk)
			if _, err := w.Write(chunk); err != nil {
				cancelled = ctx.Err() != nil
				return err
			}
		}
		return nil
	}))
	if err == nil {
		t.Fatal("saved past the limit")
	}
	if attempted > 11_000 || !cancelled {
		t.Errorf("wrote %d bytes before stopping, context ended: %v", attempted, cancelled)
	}
}
