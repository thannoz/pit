package cli

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/thannoz/pit/internal/inspect"
	"github.com/thannoz/pit/internal/sandbox"
	"github.com/thannoz/pit/internal/state"
)

func withCapture(t *testing.T, r inspect.Report, err error) *[]string {
	t.Helper()
	var loaded []string
	previous := capture
	capture = func(_ context.Context, page string) (inspect.Report, error) {
		loaded = append(loaded, page)
		r.URL = page
		return r, err
	}
	t.Cleanup(func() { capture = previous })
	return &loaded
}

func runningBox(t *testing.T) (*sandbox.Manager, state.Sandbox) {
	t.Helper()
	box := recorded(482, "github.com/acme/shop", "acme-shop-c56680", "refunds", time.Minute)
	box.URL = "http://localhost:41234/"
	m, fake := withManager(t, box)
	if err := fake.Up(t.Context(), sandbox.RuntimeSandbox(box), nil, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	return m, box
}

var broken = inspect.Report{Problems: []inspect.Problem{
	{Kind: inspect.Exception, Level: "error", Text: "ReferenceError: applyVoucher is not defined", Source: "http://localhost:41234/app.js:2"},
	{Kind: inspect.Console, Level: "warning", Text: "voucher code is deprecated"},
	{Kind: inspect.Request, Level: "error", Method: "GET", URL: "http://localhost:41234/api/cart", Status: 500, Text: "Internal Server Error"},
	{Kind: inspect.Request, Level: "error", Method: "GET", URL: "http://cdn.example/logo.png", Text: "net::ERR_NAME_NOT_RESOLVED"},
}}

func TestInspectReportsWhatWentWrong(t *testing.T) {
	m, box := runningBox(t)
	loaded := withCapture(t, broken, nil)

	out, _, err := run(t, "inspect", "482", "/orders/1001")
	if err != nil {
		t.Fatal(err)
	}
	if len(*loaded) != 1 || (*loaded)[0] != "http://localhost:41234/orders/1001" {
		t.Errorf("loaded %v", *loaded)
	}
	for _, want := range []string{
		"✗ ReferenceError: applyVoucher is not defined  (http://localhost:41234/app.js:2)",
		"! console.warn: voucher code is deprecated",
		"✗ GET http://localhost:41234/api/cart  500 Internal Server Error",
		"✗ GET http://cdn.example/logo.png  failed: net::ERR_NAME_NOT_RESOLVED",
		"3 errors, 1 warning.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	// What pit loaded is not what the reviewer looked at.
	f, _ := m.Store.Load()
	got, _ := f.Find(box.RepoRef, box.PR)
	if len(got.Browsed) != 1 || got.Browsed[0].To.Before(got.Browsed[0].From) {
		t.Errorf("Browsed = %+v", got.Browsed)
	}
}

func TestInspectOfACleanPage(t *testing.T) {
	runningBox(t)
	withCapture(t, inspect.Report{}, nil)
	out, _, err := run(t, "inspect", "482")
	if err != nil || !strings.Contains(out, "Nothing went wrong on http://localhost:41234/.") {
		t.Errorf("%v:\n%s", err, out)
	}
}

func TestInspectJSON(t *testing.T) {
	runningBox(t)
	withCapture(t, broken, nil)
	out, _, err := run(t, "inspect", "482", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var r inspect.Report
	if err := json.Unmarshal([]byte(out), &r); err != nil || len(r.Problems) != 4 || r.Problems[2].Status != 500 {
		t.Errorf("%v:\n%s", err, out)
	}
}

func TestInspectNeedsARunningSandbox(t *testing.T) {
	box := recorded(482, "github.com/acme/shop", "acme-shop-c56680", "refunds", time.Minute)
	withManager(t, box)
	loaded := withCapture(t, inspect.Report{}, nil)
	_, _, err := run(t, "inspect", "482")
	if err == nil || !strings.Contains(err.Error(), "nothing is running for #482") || len(*loaded) != 0 {
		t.Errorf("err = %v, loaded %v", err, *loaded)
	}
}

// Only the latest spans are kept; a sandbox inspected all day does not
// grow its record without end.
func TestInspectKeepsTheLatestSpans(t *testing.T) {
	m, box := runningBox(t)
	withCapture(t, inspect.Report{}, nil)
	for range state.MaxBrowsed + 3 {
		if _, _, err := run(t, "inspect", "482"); err != nil {
			t.Fatal(err)
		}
	}
	f, _ := m.Store.Load()
	if got, _ := f.Find(box.RepoRef, box.PR); len(got.Browsed) != state.MaxBrowsed {
		t.Errorf("kept %d", len(got.Browsed))
	}
}

func TestPageOf(t *testing.T) {
	for path, want := range map[string]string{
		"/":                               "http://localhost:41234/",
		"/orders/1001":                    "http://localhost:41234/orders/1001",
		"orders?page=2":                   "http://localhost:41234/orders?page=2",
		"http://localhost:41234/cart#top": "http://localhost:41234/cart#top",
	} {
		if got, err := pageOf("http://localhost:41234/", path); err != nil || got != want {
			t.Errorf("pageOf(%q) = %q, %v; want %q", path, got, err, want)
		}
	}
	if _, err := pageOf("http://localhost:41234/", "https://example.com/"); err == nil {
		t.Error("a page of another site was accepted")
	}
}
