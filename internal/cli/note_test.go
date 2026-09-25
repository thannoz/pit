package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/thannoz/pit/internal/inspect"
	"github.com/thannoz/pit/internal/notes"
	"github.com/thannoz/pit/internal/runtime"
	"github.com/thannoz/pit/internal/runtime/runtimetest"
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

// TestNotesKeepTheLogsAroundThem: what the services wrote while pit
// loaded the page, and in the half minute before, is kept with the
// note -- not what came long before, and not the whole log.
func TestNotesKeepTheLogsAroundThem(t *testing.T) {
	m, box := runningBox(t)
	if err := m.Store.Update(func(f *state.File) error {
		box.WebService = "web"
		f.Put(box)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	fake := m.Runtime.(*runtimetest.Fake)
	fake.Declared = []string{"db", "cache", "web"}
	now := time.Now()
	web := []runtime.LogLine{
		{At: now.Add(-2 * time.Minute), Text: "GET / 200 (long before)"},
	}
	for i := range 35 {
		web = append(web, runtime.LogLine{At: now.Add(-20*time.Second + time.Duration(i)*100*time.Millisecond), Text: fmt.Sprintf("GET /orders/%d 200", i)})
	}
	web = append(web, runtime.LogLine{At: now.Add(time.Hour), Text: "GET / 200 (long after)"})
	fake.ServiceLines = map[string][]runtime.LogLine{
		"web":   web,
		"db":    {{At: now.Add(-5 * time.Second), Text: `ERROR:  column "refunded_cents" does not exist`}},
		"cache": {{At: now.Add(-10 * time.Minute), Text: "Ready to accept connections"}},
	}
	fake.Lines = nil

	withPage(t, nil, nil)
	out, _, err := run(t, "note", "482", "The refund total ignores the voucher")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "logs web (30), db (1)") {
		t.Errorf("output:\n%s", out)
	}
	list, _ := m.Notes("github.com/acme/shop", "acme-shop-c56680", 482).List()
	logs := list[0].Logs
	if len(logs) != 2 || logs[0].Service != "web" || logs[1].Service != "db" {
		t.Fatalf("logs = %+v", logs)
	}
	if logs[0].Skipped != 5 || len(logs[0].Lines) != 30 || logs[0].Lines[0].Text != "GET /orders/5 200" || logs[0].Lines[29].Text != "GET /orders/34 200" {
		t.Errorf("web kept %d, skipped %d, from %q", len(logs[0].Lines), logs[0].Skipped, logs[0].Lines[0].Text)
	}
	for _, l := range logs[0].Lines {
		if strings.Contains(l.Text, "long") {
			t.Errorf("kept %q", l.Text)
		}
	}

	// Without loading the page, the half minute before the note.
	if _, _, err := run(t, "note", "482", "Totals overlap", "--no-capture"); err != nil {
		t.Fatal(err)
	}
	list, _ = m.Notes("github.com/acme/shop", "acme-shop-c56680", 482).List()
	if len(list[1].Logs) != 2 {
		t.Errorf("--no-capture kept %+v", list[1].Logs)
	}

	// A log that cannot be read is left out, and the note is kept.
	fake.Fail = map[string]error{"LogsSince": errors.New("docker is gone")}
	if _, _, err := run(t, "note", "482", "Checkout button does nothing"); err != nil {
		t.Fatal(err)
	}
	list, _ = m.Notes("github.com/acme/shop", "acme-shop-c56680", 482).List()
	if len(list) != 3 || len(list[2].Logs) != 0 {
		t.Errorf("third = %+v", list[2])
	}
}

// A sandbox that is not running has no logs to keep.
func TestNoLogsFromAStoppedSandbox(t *testing.T) {
	m, fake := withManager(t, recorded(482, "github.com/acme/shop", "acme-shop-c56680", "refunds", time.Minute))
	fake.Lines = []runtime.LogLine{{At: time.Now(), Text: "GET / 200"}}
	withPage(t, nil, nil)
	if _, _, err := run(t, "note", "482", "Checkout button does nothing"); err != nil {
		t.Fatal(err)
	}
	if list, _ := m.Notes("github.com/acme/shop", "acme-shop-c56680", 482).List(); len(list[0].Logs) != 0 {
		t.Errorf("logs = %+v", list[0].Logs)
	}
}

// The logs of a note that took a recording reach back to where the
// recording began: what the reviewer did is where the server answered.
func TestNoteLogsReachBackToTheRecording(t *testing.T) {
	m, box := runningBox(t)
	fake := m.Runtime.(*runtimetest.Fake)
	fake.Declared = []string{"web"}
	now := time.Now()
	fake.ServiceLines = map[string][]runtime.LogLine{"web": {
		{At: now.Add(-10 * time.Minute), Text: "GET / 200 (before the recording)"},
		{At: now.Add(-90 * time.Second), Text: "POST /orders 500 (while recording)"},
	}}
	b := m.Notes(box.Repo, box.RepoRef, box.PR)
	if err := b.KeepRecording(notes.Recording{At: now.Add(-2 * time.Minute), Steps: orderSteps}, nil); err != nil {
		t.Fatal(err)
	}
	withPage(t, nil, nil)
	if _, _, err := run(t, "note", "482", "Ordering fails"); err != nil {
		t.Fatal(err)
	}
	list, _ := b.List()
	if logs := list[0].Logs; len(logs) != 1 || len(logs[0].Lines) != 1 || !strings.Contains(logs[0].Lines[0].Text, "while recording") {
		t.Errorf("logs = %+v", logs)
	}
}

// A note that cannot be taken leaves the recording for the next one.
func TestARecordingOutlivesAFailedNote(t *testing.T) {
	m, box := runningBox(t)
	b := m.Notes(box.Repo, box.RepoRef, box.PR)
	if err := b.KeepRecording(notes.Recording{At: time.Now(), Steps: orderSteps}, []byte("GIF89a")); err != nil {
		t.Fatal(err)
	}
	withPage(t, nil, context.Canceled)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	cmd := newRootCmd()
	cmd.SetArgs([]string{"note", "482", "Ordering fails"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetContext(ctx)
	if err := cmd.Execute(); err == nil {
		t.Fatal("a note taken with the command cancelled")
	}
	if r, gif, _ := b.TakeRecording(); r == nil || string(gif) != "GIF89a" {
		t.Errorf("the recording went: %+v, %q", r, gif)
	}
}
