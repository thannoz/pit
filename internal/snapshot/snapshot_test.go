package snapshot

import (
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
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

	snap, err := s.Save(t.Context(), Snapshot{Name: "cart-with-voucher", PR: 482, SHA: "abc", Scenario: "standard", Service: "db"}, writes(dump))
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
	if err != nil || len(list) != 1 || list[0] != snap {
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
			if _, err := s.Save(t.Context(), Snapshot{}, writes("-- an earlier one\n")); err != nil {
				t.Fatal(err)
			}
			before := files(t, s)

			ctx, cancel := context.WithCancel(t.Context())
			if name == "cancelled" {
				cancel()
			}
			defer cancel()
			_, err := s.Save(ctx, Snapshot{Name: "kept"}, dump)
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
	first, err := s.Save(t.Context(), Snapshot{Name: "cart", PR: 7}, writes("a\n"))
	if err != nil {
		t.Fatal(err)
	}
	ran := false
	_, err = s.Save(t.Context(), Snapshot{Name: "cart"}, func(context.Context, io.Writer) error { ran = true; return nil })
	if err == nil || !strings.Contains(err.Error(), first.ID) || !strings.Contains(err.Error(), "#7") {
		t.Errorf("err = %v; it should name the snapshot that has the name", err)
	}
	if ran {
		t.Error("the save command ran although the name was taken")
	}
	// Without a name there is nothing to collide with.
	if _, err := s.Save(t.Context(), Snapshot{}, writes("b\n")); err != nil {
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
		snap, err := s.Save(t.Context(), Snapshot{}, writes("x\n"))
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
	named, err := s.Save(t.Context(), Snapshot{Name: "cart"}, writes("a\n"))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := s.Save(t.Context(), Snapshot{}, writes("b\n"))
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
	snap, err := s.Save(t.Context(), Snapshot{}, writes(dump))
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.Open(snap)
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
	if _, err := s.Open(snap); err == nil || errs.Hint(err) == "" {
		t.Errorf("data gone: %v", err)
	}
}
