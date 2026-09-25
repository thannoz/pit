package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/thannoz/pit/internal/inspect"
	"github.com/thannoz/pit/internal/notes"
	"github.com/thannoz/pit/internal/state"
)

// withPage answers every page with the problems given, a picture when
// one is asked for, and notes what was loaded and which picture.
func withPage(t *testing.T, problems []inspect.Problem, fail error) *[]string {
	t.Helper()
	var loaded []string
	previous := capture
	capture = func(_ context.Context, page string, shot inspect.Shot) (inspect.Report, error) {
		loaded = append(loaded, page)
		if shot == inspect.FullPage {
			loaded = append(loaded, "full page")
		}
		if fail != nil {
			return inspect.Report{}, fail
		}
		r := inspect.Report{URL: page, Problems: problems}
		if shot != inspect.NoShot {
			r.Screenshot = &inspect.Screenshot{PNG: []byte("\x89PNG " + page), Width: 1280, Height: 800, FullPage: shot == inspect.FullPage}
		}
		return r, nil
	}
	t.Cleanup(func() { capture = previous })
	return &loaded
}

// TestNotesAreCollectedAndListed is the acceptance criterion for T-803:
// three notes, each with its page, commit, data, time and what went
// wrong, are collected and listed.
func TestNotesAreCollectedAndListed(t *testing.T) {
	m, box := runningBox(t)
	if err := m.Store.Update(func(f *state.File) error {
		box.SHA, box.Scenario = "c56680aa11", "refunded"
		f.Put(box)
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	loaded := withPage(t, broken.Problems, nil)
	out, _, err := run(t, "note", "482", "The refund total ignores the voucher", "--page", "/orders/1001")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Noted on #482 as note 1.",
		"1  The refund total ignores the voucher",
		"/orders/1001 · c56680a · refunded\n",
		"✗ ReferenceError: applyVoucher is not defined",
		"✗ GET http://cdn.example/logo.png  failed: net::ERR_NAME_NOT_RESOLVED",
		"screenshot ",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if len(*loaded) != 1 || (*loaded)[0] != "http://localhost:41234/orders/1001" {
		t.Errorf("loaded %v", *loaded)
	}

	withPage(t, nil, nil)
	if _, _, err := run(t, "note", "482", "Totals", "overlap", "on", "a", "phone"); err != nil {
		t.Fatal(err)
	}
	loaded = withPage(t, nil, nil)
	if _, _, err := run(t, "note", "482", "No way back from the order page", "--page", "/orders/1001?tab=items", "--no-capture"); err != nil {
		t.Fatal(err)
	}
	if len(*loaded) != 0 {
		t.Errorf("--no-capture loaded %v", *loaded)
	}

	out, _, err = run(t, "note", "482")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`3 notes on #482 "Some change":`,
		"1  The refund total ignores the voucher",
		"     … 1 more",
		"2  Totals overlap on a phone",
		"/ · c56680a · refunded · 0s ago",
		"nothing went wrong on the page",
		"3  No way back from the order page",
		"/orders/1001?tab=items · c56680a",
		"page not loaded: not asked to",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("listing lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "net::ERR_NAME_NOT_RESOLVED") {
		t.Errorf("a listing shows every problem:\n%s", out)
	}

	list, err := m.Notes(box.Repo, box.RepoRef, 482).List()
	if err != nil || len(list) != 3 {
		t.Fatalf("%d notes, %v", len(list), err)
	}
	first := list[0]
	if first.SHA != "c56680aa11" || first.Scenario != "refunded" || first.URL != "http://localhost:41234/orders/1001" ||
		len(first.Problems) != 4 || time.Since(first.At) > time.Minute {
		t.Errorf("first = %+v", first)
	}
	if got, err := os.ReadFile(first.Screenshot); err != nil || string(got) != "\x89PNG http://localhost:41234/orders/1001" {
		t.Errorf("picture %q, %v", got, err)
	}
	if list[2].Screenshot != "" || list[2].Captured() {
		t.Errorf("third = %+v", list[2])
	}

	// What pit loaded for the notes is not what the reviewer looked at.
	f, _ := m.Store.Load()
	if got, _ := f.Find(box.RepoRef, box.PR); len(got.Browsed) != 2 {
		t.Errorf("Browsed = %+v", got.Browsed)
	}
}

func TestNoteOnEditedData(t *testing.T) {
	m, box := runningBox(t)
	if err := m.Store.Update(func(f *state.File) error {
		box.Snapshot, box.Edited = "sn_7f3a1b", true
		f.Put(box)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	loaded := withPage(t, nil, nil)
	out, _, err := run(t, "note", "482", "Voucher applied twice", "--full-page")
	if err != nil || !strings.Contains(out, "snapshot sn_7f3a1b +edited") {
		t.Errorf("%v:\n%s", err, out)
	}
	if strings.Join(*loaded, "|") != "http://localhost:41234/|full page" {
		t.Errorf("loaded %v", *loaded)
	}
}

// A note is kept when the page cannot be loaded; why is kept with it.
func TestNoteWithoutAPage(t *testing.T) {
	t.Run("stopped", func(t *testing.T) {
		withManager(t, recorded(482, "github.com/acme/shop", "acme-shop-c56680", "refunds", time.Minute))
		loaded := withPage(t, nil, nil)
		out, _, err := run(t, "note", "482", "Checkout button does nothing")
		if err != nil || !strings.Contains(out, "page not loaded: the sandbox was not running") || len(*loaded) != 0 {
			t.Errorf("%v, loaded %v:\n%s", err, *loaded, out)
		}
	})
	t.Run("no browser", func(t *testing.T) {
		runningBox(t)
		withPage(t, nil, errors.New("no Chrome or Chromium found\nmore"))
		out, _, err := run(t, "note", "482", "Checkout button does nothing")
		if err != nil || !strings.Contains(out, "page not loaded: no Chrome or Chromium found\n") || strings.Contains(out, "more") {
			t.Errorf("%v:\n%s", err, out)
		}
	})
}

// Notes outlive the sandbox: they are what goes into the comment, and
// that may be written after it was taken down.
func TestNotesOutliveTheSandbox(t *testing.T) {
	id := atRepo(t, "github.com", "acme", "shop")
	box := recorded(482, id.String(), id.Ref(), "refunds", time.Minute)
	m, _ := withManager(t, box)
	withPage(t, nil, nil)
	if _, _, err := run(t, "note", "482", "Checkout button does nothing"); err != nil {
		t.Fatal(err)
	}
	if err := m.Store.Update(func(f *state.File) error { f.Remove(box.RepoRef, box.PR); return nil }); err != nil {
		t.Fatal(err)
	}
	out, _, err := run(t, "note", "482")
	if err != nil || !strings.Contains(out, "1 note on #482:") || !strings.Contains(out, "Checkout button does nothing") {
		t.Errorf("%v:\n%s", err, out)
	}
	if _, _, err := run(t, "note", "482", "Another"); err == nil || !strings.Contains(err.Error(), "there is no sandbox for #482") {
		t.Errorf("noting without a sandbox: %v", err)
	}
}

func TestRemovingNotes(t *testing.T) {
	m, box := runningBox(t)
	withPage(t, nil, nil)
	for _, text := range []string{"one", "two", "three"} {
		if _, _, err := run(t, "note", "482", text); err != nil {
			t.Fatal(err)
		}
	}
	out, _, err := run(t, "note", "482", "--remove", "1,3")
	if err != nil || !strings.Contains(out, "Removed note 1: one") || !strings.Contains(out, "Removed note 3: three") {
		t.Errorf("%v:\n%s", err, out)
	}
	if list, _ := m.Notes(box.Repo, box.RepoRef, 482).List(); len(list) != 1 || list[0].Text != "two" {
		t.Errorf("left %+v", list)
	}
	if _, _, err := run(t, "note", "482", "--remove", "9"); err == nil || !strings.Contains(err.Error(), "has no note 9") {
		t.Errorf("err = %v", err)
	}
}

func TestNoNotes(t *testing.T) {
	runningBox(t)
	out, _, err := run(t, "note", "482")
	if err != nil || !strings.Contains(out, "No notes on #482.") {
		t.Errorf("%v:\n%s", err, out)
	}
}

func TestNoteFlagsThatDoNotFit(t *testing.T) {
	m, box := runningBox(t)
	withPage(t, nil, nil)
	if _, _, err := run(t, "note", "482", "kept"); err != nil {
		t.Fatal(err)
	}
	loaded := withPage(t, nil, nil)
	for _, args := range [][]string{
		{"--page", "/orders"},
		{"--full-page"},
		{"--no-capture"},
		{"text", "--remove", "1"},
		{"--remove", "1", "--page", "/orders"},
	} {
		if _, _, err := run(t, append([]string{"note", "482"}, args...)...); err == nil {
			t.Errorf("%v was accepted", args)
		}
	}
	if len(*loaded) != 0 {
		t.Errorf("loaded %v", *loaded)
	}
	if list, _ := m.Notes(box.Repo, box.RepoRef, 482).List(); len(list) != 1 {
		t.Errorf("notes = %+v", list)
	}
}

func TestNoteJSON(t *testing.T) {
	runningBox(t)
	withPage(t, broken.Problems, nil)
	out, _, err := run(t, "note", "482", "The refund total ignores the voucher", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var n notes.Note
	if err := json.Unmarshal([]byte(out), &n); err != nil || n.ID != 1 || len(n.Problems) != 4 || n.Screenshot == "" {
		t.Errorf("%v:\n%s", err, out)
	}
	out, _, err = run(t, "note", "482", "--json")
	var list []notes.Note
	if err != nil || json.Unmarshal([]byte(out), &list) != nil || len(list) != 1 || len(list[0].Problems) != 4 {
		t.Errorf("%v:\n%s", err, out)
	}
}
