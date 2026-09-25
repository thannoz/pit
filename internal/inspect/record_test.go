package inspect

import (
	"context"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// shop is a small application with a form, a list, a box to tick and a
// search, and remembers the orders placed.
type shop struct {
	mu       sync.Mutex
	orders   []string
	searches []string
}

func newShop(t *testing.T) (*shop, *httptest.Server) {
	t.Helper()
	s := &shop{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `<html><body><h1>Teashop</h1>
<form method="post" action="/orders">
  <label for="item">Item</label> <input id="item" name="item">
  <select name="size"><option value="s">Small</option><option value="l">Large</option></select>
  <label><input type="checkbox" name="gift"> Gift wrap</label>
  <input type="password" name="pin">
  <button>Order it</button>
</form>
<a href="/orders">See orders</a> <a href="/slow">Specials</a>
</body></html>`)
	})
	mux.HandleFunc("POST /orders", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.orders = append(s.orders, fmt.Sprintf("%s/%s/gift=%s/pin=%s", r.FormValue("item"), r.FormValue("size"), r.FormValue("gift"), r.FormValue("pin")))
		s.mu.Unlock()
		http.Redirect(w, r, "/orders", http.StatusSeeOther)
	})
	mux.HandleFunc("GET /orders", func(w http.ResponseWriter, _ *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		_, _ = fmt.Fprintf(w, `<html><body><h1>Orders</h1><p>%s</p><a href="/">Back</a></body></html>`, html.EscapeString(strings.Join(s.orders, ", ")))
	})
	// A form the way a framework makes one: what is typed is kept by
	// the page's script as it is typed, and sent from there.
	mux.HandleFunc("GET /app", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `<html><body><input id="item" name="item">
<select id="size" name="size"><option value="s">Small</option><option value="l">Large</option></select>
<button id="send">Order it</button>
<script>
let item = '', size = 'none';
document.getElementById('item').addEventListener('input', (e) => { item = e.target.value; });
document.getElementById('size').addEventListener('change', (e) => { size = e.target.value; });
document.getElementById('send').addEventListener('click', () => {
  const f = document.createElement('form');
  f.method = 'post'; f.action = '/orders';
  for (const [k, v] of [['item', item], ['size', 'js-' + size]]) { const i = document.createElement('input'); i.name = k; i.value = v; f.appendChild(i); }
  document.body.appendChild(f); f.submit();
});
</script></body></html>`)
	})
	mux.HandleFunc("GET /slow", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(3 * time.Second):
		case <-r.Context().Done():
		}
		_, _ = fmt.Fprint(w, "slow")
	})
	mux.HandleFunc("GET /search", func(w http.ResponseWriter, r *http.Request) {
		if q := r.URL.Query().Get("q"); q != "" {
			s.mu.Lock()
			s.searches = append(s.searches, q)
			s.mu.Unlock()
		}
		_, _ = fmt.Fprint(w, `<html><body><form action="/search"><input id="q" name="q" placeholder="Search teas"></form></body></html>`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return s, srv
}

func (s *shop) state() (orders, searches []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.orders...), append([]string(nil), s.searches...)
}

// reviewer does what a reviewer would, with the keyboard and the mouse.
func reviewer(srv string) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		return chromedp.Run(ctx,
			chromedp.WaitVisible("#item"),
			// Into the field first, the way a person types.
			chromedp.Click("#item"),
			chromedp.SendKeys("#item", "Genmaicha"),
			// A list is chosen from with the keyboard as often as not.
			chromedp.Focus(`select[name="size"]`),
			chromedp.KeyEvent("L"),
			chromedp.Click(`input[name="gift"]`),
			chromedp.SendKeys(`input[name="pin"]`, "1234"),
			chromedp.Click("button"),
			chromedp.WaitVisible(`//h1[text()="Orders"]`, chromedp.BySearch),
			chromedp.Navigate(srv+"/search"),
			chromedp.WaitVisible("#q"),
			chromedp.SendKeys("#q", "sencha\r"),
			chromedp.WaitVisible("#q"),
		)
	}
}

// TestRecord is half the acceptance criterion for T-808: what a
// reviewer does is written down as steps a person can read and a
// browser can repeat.
func TestRecord(t *testing.T) {
	path := browser(t)
	_, srv := newShop(t)
	steps, err := Record(t.Context(), srv.URL+"/", RecordOptions{Browser: path, Headless: true, Drive: reviewer(srv.URL)})
	if err != nil {
		t.Fatal(err)
	}
	want := []Step{
		{Action: Goto, Path: "/"},
		{Action: Fill, Selector: "#item", Text: "Item", Value: "Genmaicha"},
		{Action: Select, Selector: `select[name="size"]`, Text: "size", Value: "l", Choice: "Large"},
		{Action: Click, Selector: `input[name="gift"]`, Text: "Gift wrap"},
		{Action: Fill, Selector: `input[name="pin"]`, Text: "pin", Secret: true},
		{Action: Click, Selector: "body > form > button", Text: "Order it"},
		{Action: Goto, Path: "/search"},
		{Action: Fill, Selector: "#q", Text: "q", Value: "sencha"},
		{Action: Press, Selector: "#q", Text: "q", Key: "Enter"},
	}
	if len(steps) != len(want) {
		t.Fatalf("recorded %d steps, want %d:\n%s", len(steps), len(want), dump(steps))
	}
	for i := range want {
		if steps[i] != want[i] {
			t.Errorf("step %d = %+v\n        want %+v", i+1, steps[i], want[i])
		}
	}
}

func dump(steps []Step) string {
	var b strings.Builder
	for i, s := range steps {
		fmt.Fprintf(&b, "  %d. %+v\n", i+1, s)
	}
	return b.String()
}

// TestReplay is the other half: the steps do again, on a fresh
// application, what the reviewer did -- the order is placed, with what
// was typed, chosen and ticked, and the search is made.
func TestReplay(t *testing.T) {
	path := browser(t)
	_, recorded := newShop(t)
	steps, err := Record(t.Context(), recorded.URL+"/", RecordOptions{Browser: path, Headless: true, Drive: reviewer(recorded.URL)})
	if err != nil {
		t.Fatal(err)
	}

	fresh, srv := newShop(t)
	var told []string
	report, err := Replay(t.Context(), srv.URL, steps, ReplayOptions{Browser: path, Password: "1234",
		OnStep: func(n int, s Step) { told = append(told, fmt.Sprintf("%d %s", n, s)) }})
	if err != nil {
		t.Fatal(err)
	}
	orders, searches := fresh.state()
	if strings.Join(orders, "|") != "Genmaicha/l/gift=on/pin=1234" || strings.Join(searches, "|") != "sencha" {
		t.Errorf("orders %q, searches %q", orders, searches)
	}
	if report.URL != srv.URL+"/search?q=sencha" {
		t.Errorf("ended at %s", report.URL)
	}
	if want := []string{
		`1 open /`,
		`2 type "Genmaicha" into "Item"`,
		`3 choose "Large" in "size"`,
		`4 click "Gift wrap"`,
		`5 type the password into "pin"`,
		`6 click "Order it"`,
		`7 open /search`,
		`8 type "sencha" into "q"`,
		`9 press Enter in "q"`,
	}; strings.Join(told, "\n") != strings.Join(want, "\n") {
		t.Errorf("told\n%s\nwant\n%s", strings.Join(told, "\n"), strings.Join(want, "\n"))
	}
}

// A password is not in the recording, so a replay needs one given.
func TestReplayNeedsThePassword(t *testing.T) {
	steps := []Step{{Action: Goto, Path: "/"}, {Action: Fill, Selector: "#pin", Secret: true}}
	_, err := Replay(t.Context(), "http://127.0.0.1:1", steps, ReplayOptions{Browser: "/no/chrome"})
	if err == nil || !strings.Contains(err.Error(), "step 2 types a password") {
		t.Errorf("err = %v", err)
	}
}

// When the page is not what it was, replay says which step found
// nothing, and where.
func TestReplayOfAPageThatChanged(t *testing.T) {
	path := browser(t)
	_, srv := newShop(t)
	steps := []Step{{Action: Goto, Path: "/"}, {Action: Click, Selector: "#checkout", Text: "Check out"}}
	_, err := Replay(t.Context(), srv.URL, steps, ReplayOptions{Browser: path, Find: 300 * time.Millisecond})
	if err == nil || !strings.Contains(err.Error(), `step 2, click "Check out": nothing on / is "Check out" (#checkout)`) {
		t.Errorf("err = %v", err)
	}
}

// An element whose selector changed is found by what it says.
func TestReplayFindsByText(t *testing.T) {
	path := browser(t)
	fresh, srv := newShop(t)
	steps := []Step{
		{Action: Goto, Path: "/"},
		{Action: Fill, Selector: "#old-item-field", Text: "Item", Value: "Bancha"},
		{Action: Fill, Selector: `input[name="pin"]`, Value: "9"},
		{Action: Click, Selector: "#old-button", Text: "Order it"},
	}
	if _, err := Replay(t.Context(), srv.URL, steps, ReplayOptions{Browser: path}); err != nil {
		t.Fatal(err)
	}
	if orders, _ := fresh.state(); strings.Join(orders, "|") != "Bancha/s/gift=/pin=9" {
		t.Errorf("orders %q", orders)
	}
}

// A page that keeps what is typed as it is typed sees the replay type.
func TestReplayOnAScriptedForm(t *testing.T) {
	path := browser(t)
	fresh, srv := newShop(t)
	steps := []Step{
		{Action: Goto, Path: "/app"},
		{Action: Fill, Selector: "#item", Text: "item", Value: "Kukicha"},
		{Action: Select, Selector: "#size", Text: "size", Value: "l", Choice: "Large"},
		{Action: Click, Selector: "#send", Text: "Order it"},
	}
	if _, err := Replay(t.Context(), srv.URL, steps, ReplayOptions{Browser: path}); err != nil {
		t.Fatal(err)
	}
	if orders, _ := fresh.state(); strings.Join(orders, "|") != "Kukicha/js-l/gift=/pin=" {
		t.Errorf("orders %q", orders)
	}
}

// The page asked to go somewhere, and before it started going the
// reviewer typed another address: that one is a step.
func TestRecordingTellsTypedFromRequested(t *testing.T) {
	base, _ := url.Parse("http://localhost:4000/")
	r := &recording{base: base}
	for _, ev := range []any{
		&page.EventFrameNavigated{Frame: &cdp.Frame{ID: "main", URL: "http://localhost:4000/"}},
		&page.EventFrameRequestedNavigation{FrameID: "main", URL: "http://localhost:4000/orders"},
		&page.EventFrameStartedNavigating{FrameID: "main", URL: "http://localhost:4000/search", NavigationType: page.FrameStartedNavigatingNavigationTypeDifferentDocument},
		&page.EventFrameNavigated{Frame: &cdp.Frame{ID: "main", URL: "http://localhost:4000/search"}},
		// And the page's own, which is no step.
		&page.EventFrameRequestedNavigation{FrameID: "main", URL: "http://localhost:4000/orders"},
		&page.EventFrameStartedNavigating{FrameID: "main", URL: "http://localhost:4000/orders", NavigationType: page.FrameStartedNavigatingNavigationTypeDifferentDocument},
		&page.EventFrameNavigated{Frame: &cdp.Frame{ID: "main", URL: "http://localhost:4000/orders/7"}},
		// A frame inside the page is no step either.
		&page.EventFrameNavigated{Frame: &cdp.Frame{ID: "ad", ParentID: "main", URL: "http://localhost:4000/ad"}},
	} {
		r.handle(ev)
	}
	want := []Step{{Action: Goto, Path: "/"}, {Action: Goto, Path: "/search"}}
	if got := r.done(); len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("recorded:\n%s", dump(got))
	}
}

// A page's own navigation cut short by the reviewer going elsewhere
// leaves that elsewhere a step of its own.
func TestRecordAnAddressTypedWhileAPageWasLoading(t *testing.T) {
	path := browser(t)
	_, srv := newShop(t)
	steps, err := Record(t.Context(), srv.URL+"/", RecordOptions{Browser: path, Headless: true, Drive: func(ctx context.Context) error {
		return chromedp.Run(ctx,
			chromedp.WaitVisible("#item"),
			chromedp.Click(`a[href="/slow"]`),
			chromedp.Sleep(300*time.Millisecond),
			chromedp.Navigate(srv.URL+"/search"),
			chromedp.WaitVisible("#q"),
		)
	}})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(steps); n == 0 || steps[n-1] != (Step{Action: Goto, Path: "/search"}) {
		t.Errorf("recorded:\n%s", dump(steps))
	}
}

// A recording ends when the reviewer closes the browser's window --
// which on macOS leaves the browser itself running.
func TestRecordingEndsWhenTheWindowCloses(t *testing.T) {
	path := browser(t)
	_, srv := newShop(t)
	bctx, cancel, _, err := startBrowser(t.Context(), path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	if err := chromedp.Run(bctx, chromedp.Navigate(srv.URL+"/")); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		waitForClose(t.Context(), bctx)
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("stopped with the window open")
	case <-time.After(time.Second):
	}
	if err := chromedp.Run(bctx, page.Close()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("still recording after the window closed")
	}
}
