package view

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/chromedp"

	"github.com/thannoz/pit/internal/inspect"
)

func chrome(t *testing.T) context.Context {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping: starts a browser")
	}
	path, err := inspect.FindBrowser()
	if err != nil {
		if os.Getenv("PIT_REQUIRE_BROWSER") != "" {
			t.Fatalf("PIT_REQUIRE_BROWSER is set: %v", err)
		}
		t.Skipf("skipping: %v", err)
	}
	opts := append(chromedp.DefaultExecAllocatorOptions[:], chromedp.ExecPath(path), chromedp.WindowSize(1280, 800))
	if os.Geteuid() == 0 {
		opts = append(opts, chromedp.NoSandbox)
	}
	actx, cancelAlloc := chromedp.NewExecAllocator(t.Context(), opts...)
	ctx, cancel := chromedp.NewContext(actx)
	t.Cleanup(func() { cancel(); cancelAlloc() })
	return ctx
}

// tall is an application with a long page and a second one.
func tall(t *testing.T, name string) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprintf(w, `<html><head><title>%s</title></head><body style="margin:0">
<p id="top"><a id="next" href="/second">Second page</a></p>
<div style="height:3000px"></div><p id="far">far down in %s</p><div style="height:1000px"></div></body></html>`, name, name)
	})
	mux.HandleFunc("GET /second", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, `<html><head></head><body><h1>Second</h1></body></html>`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

// waitFor polls the page around the frames until cond is true.
func waitFor(ctx context.Context, t *testing.T, cond, what string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var ok bool
		if err := chromedp.Run(ctx, chromedp.Evaluate(cond, &ok)); err == nil && ok {
			return
		}
		if time.Now().After(deadline) {
			var state string
			_ = chromedp.Run(ctx, chromedp.Evaluate(`JSON.stringify(window.pit)`, &state))
			t.Fatalf("%s did not happen; pit = %s", what, state)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// TestSideBySide is the acceptance criterion for T-904: both are shown
// next to each other and can be used there, and scrolling one scrolls
// the other.
func TestSideBySide(t *testing.T) {
	ctx := chrome(t)
	left, right := tall(t, "the pull request"), tall(t, "the base")
	serving, stop := context.WithCancel(t.Context())
	defer stop()
	pages := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		done <- Serve(serving, Side{Label: "#482", Detail: "feat/refunds at a3f91c2", Target: left},
			Side{Label: "#482 base", Detail: "main at 8c21f0d", Target: right}, func(p string) { pages <- p })
	}()
	page := <-pages

	var title string
	if err := chromedp.Run(ctx, chromedp.Navigate(page), chromedp.Title(&title)); err != nil {
		t.Fatal(err)
	}
	if title != "#482 and #482 base" {
		t.Errorf("title %q", title)
	}
	waitFor(ctx, t, `pit.paths.left === '/' && pit.paths.right === '/'`, "both pages loading")

	var frames []*cdp.Node
	if err := chromedp.Run(ctx, chromedp.Nodes("iframe", &frames, chromedp.ByQueryAll)); err != nil || len(frames) != 2 {
		t.Fatalf("%d frames, %v", len(frames), err)
	}
	// Each shows its own application.
	var text string
	if err := chromedp.Run(ctx, chromedp.Text("#far", &text, chromedp.ByQuery, chromedp.FromNode(frames[1]))); err != nil || !strings.Contains(text, "the base") {
		t.Errorf("the right frame says %q, %v", text, err)
	}

	if err := chromedp.Run(ctx, chromedp.ScrollIntoView("#far", chromedp.ByQuery, chromedp.FromNode(frames[0]))); err != nil {
		t.Fatal(err)
	}
	waitFor(ctx, t, `pit.positions.left > 1000 && Math.abs(pit.positions.right - pit.positions.left) <= 2`, "the right frame following")
	// The right frame's echo is not passed back: the left one, which
	// the reviewer scrolled, was never told to scroll.
	time.Sleep(300 * time.Millisecond)
	var told string
	var ok bool
	if err := chromedp.Run(ctx, chromedp.AttributeValue("html", "data-pit-told", &told, &ok, chromedp.ByQuery, chromedp.FromNode(frames[0]))); err != nil || ok {
		t.Errorf("the left frame was told to scroll %s times (%v)", told, err)
	}

	// Not together: the other stays where it is.
	if err := chromedp.Run(ctx, chromedp.Click("#scroll", chromedp.ByQuery),
		chromedp.ScrollIntoView("#top", chromedp.ByQuery, chromedp.FromNode(frames[0]))); err != nil {
		t.Fatal(err)
	}
	waitFor(ctx, t, `pit.positions.left < 100`, "the left frame going up")
	time.Sleep(500 * time.Millisecond)
	var stayed float64
	if err := chromedp.Run(ctx, chromedp.Evaluate(`pit.positions.right`, &stayed)); err != nil || stayed < 1000 {
		t.Errorf("the right frame moved to %v", stayed)
	}

	// Following pages, a link taken in one is taken in the other.
	if err := chromedp.Run(ctx, chromedp.Click("#follow", chromedp.ByQuery),
		chromedp.ScrollIntoView("#next", chromedp.ByQuery, chromedp.FromNode(frames[1])),
		chromedp.Click("#next", chromedp.ByQuery, chromedp.FromNode(frames[1]))); err != nil {
		t.Fatal(err)
	}
	waitFor(ctx, t, `pit.paths.right === '/second' && pit.paths.left === '/second'`, "both on the second page")

	stop()
	if err := <-done; err != nil {
		t.Errorf("Serve: %v", err)
	}
}
