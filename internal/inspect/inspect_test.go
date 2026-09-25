package inspect

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// browser skips a test on a machine without Chrome or Chromium.
func browser(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping: starts a browser")
	}
	path, err := FindBrowser()
	if err != nil {
		if os.Getenv("PIT_REQUIRE_BROWSER") != "" {
			t.Fatalf("PIT_REQUIRE_BROWSER is set: %v", err)
		}
		t.Skipf("skipping: %v", err)
	}
	return path
}

// closedPort is an address nothing listens on.
func closedPort(t *testing.T) string {
	t.Helper()
	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

func site(t *testing.T, pages map[string]string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	for path, body := range pages {
		mux.HandleFunc("GET "+path, func(w http.ResponseWriter, _ *http.Request) {
			if strings.HasSuffix(path, ".js") {
				w.Header().Set("Content-Type", "text/javascript")
			} else {
				w.Header().Set("Content-Type", "text/html")
			}
			_, _ = w.Write([]byte(body))
		})
	}
	mux.HandleFunc("GET /api/slow", func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(1200 * time.Millisecond)
		http.Error(w, "upstream timed out", http.StatusBadGateway)
	})
	// A favicon that takes its time, as one on a slow machine does.
	mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(3 * time.Second):
		case <-r.Context().Done():
		}
		http.NotFound(w, r)
	})
	mux.HandleFunc("GET /api/broken", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func find(r Report, kind Kind, text string) (Problem, bool) {
	for _, p := range r.Problems {
		if p.Kind == kind && (strings.Contains(p.Text, text) || strings.Contains(p.URL, text)) {
			return p, true
		}
	}
	return Problem{}, false
}

// TestCaptureCatchesAJavaScriptError is the acceptance criterion for
// T-801: a script that throws is reported, with where it threw.
func TestCaptureCatchesAJavaScriptError(t *testing.T) {
	path := browser(t)
	elsewhere := closedPort(t)
	srv := site(t, map[string]string{
		"/{$}": `<html><body><h1>Cart</h1>
<script src="/app.js"></script>
<script>
console.warn("voucher code is deprecated");
console.error("payment failed:", 42);
fetch("/api/missing");
setTimeout(() => fetch("/api/broken"), 300);
</script>
<img src="http://` + elsewhere + `/logo.png">
</body></html>`,
		"/app.js": "function checkout() {\n  applyVoucher();\n}\ncheckout();\n",
	})

	r, err := Capture(t.Context(), srv.URL+"/", Options{Browser: path})
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}

	ex, ok := find(r, Exception, "applyVoucher is not defined")
	if !ok || ex.Text != "ReferenceError: applyVoucher is not defined" || ex.Level != "error" || !strings.HasSuffix(ex.Source, "/app.js:2") {
		t.Errorf("exception = %+v, found %v\n%+v", ex, ok, r.Problems)
	}
	if p, ok := find(r, Console, "payment failed: 42"); !ok || p.Level != "error" {
		t.Errorf("console.error = %+v, found %v", p, ok)
	}
	if p, ok := find(r, Console, "voucher code is deprecated"); !ok || p.Level != "warning" {
		t.Errorf("console.warn = %+v, found %v", p, ok)
	}
	if p, ok := find(r, Request, "/api/missing"); !ok || p.Status != 404 || p.Method != "GET" {
		t.Errorf("404 = %+v, found %v", p, ok)
	}
	// Late, after the page had loaded: waiting for it to settle is
	// what catches this one.
	if p, ok := find(r, Request, "/api/broken"); !ok || p.Status != 500 {
		t.Errorf("500 = %+v, found %v", p, ok)
	}
	if p, ok := find(r, Request, "/logo.png"); !ok || p.Status != 0 || !strings.Contains(p.Text, "ERR_CONNECTION_REFUSED") {
		t.Errorf("refused = %+v, found %v", p, ok)
	}
	if r.Errors() != 5 {
		t.Errorf("errors = %d:\n%+v", r.Errors(), r.Problems)
	}
}

// A request still on its way is waited for, however long it has been
// quiet otherwise.
func TestCaptureWaitsForWhatIsInFlight(t *testing.T) {
	path := browser(t)
	srv := site(t, map[string]string{"/{$}": `<html><body><script>fetch("/api/slow")</script></body></html>`})
	r, err := Capture(t.Context(), srv.URL+"/", Options{Browser: path})
	if err != nil {
		t.Fatal(err)
	}
	if p, ok := find(r, Request, "/api/slow"); !ok || p.Status != 502 {
		t.Errorf("slow = %+v, found %v: %+v", p, ok, r.Problems)
	}

	// Waited for as long as allowed, and not answered: said so.
	r, err = Capture(t.Context(), srv.URL+"/", Options{Browser: path, Settle: 300 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Pending) != 1 || !strings.HasSuffix(r.Pending[0], "/api/slow") || !strings.HasPrefix(r.Pending[0], "GET ") {
		t.Errorf("pending = %v", r.Pending)
	}
}

func TestCaptureOfACleanPage(t *testing.T) {
	path := browser(t)
	srv := site(t, map[string]string{"/{$}": `<html><body><h1>Fine</h1><script>console.log("hello")</script></body></html>`})
	// A generous limit, so that waiting until it is the failure and a
	// slow machine starting Chrome is not.
	start := time.Now()
	r, err := Capture(t.Context(), srv.URL+"/", Options{Browser: path, Settle: 20 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Problems) != 0 || len(r.Pending) != 0 {
		t.Errorf("problems on a clean page: %+v, pending %v", r.Problems, r.Pending)
	}
	if r.Screenshot != nil {
		t.Error("a picture was taken that nobody asked for")
	}
	if out, _ := json.Marshal(r); !strings.Contains(string(out), `"problems":[]`) {
		t.Errorf("no problems read as %s", out)
	}
	if took := time.Since(start); took > 12*time.Second {
		t.Errorf("a quiet page took %v to settle; pending %v", took, r.Pending)
	}
}

func TestCaptureOfAPageThatIsNotThere(t *testing.T) {
	path := browser(t)
	_, err := Capture(t.Context(), "http://"+closedPort(t)+"/", Options{Browser: path})
	if err == nil || !strings.Contains(err.Error(), "could not load") {
		t.Errorf("err = %v", err)
	}
}

func TestFindBrowserHonoursPitBrowser(t *testing.T) {
	t.Setenv("PIT_BROWSER", "/no/such/chrome")
	if _, err := FindBrowser(); err == nil || !strings.Contains(err.Error(), "PIT_BROWSER names /no/such/chrome") {
		t.Errorf("err = %v", err)
	}
}
