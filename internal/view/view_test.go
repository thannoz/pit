package view

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func backend(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'; script-src 'self'")
		_, _ = io.WriteString(w, `<!doctype html><html><HEAD lang="en"><title>Teashop</title></head><body>hi</body></html>`)
	})
	mux.HandleFunc("GET /api/orders", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"orders":[]}`)
	})
	mux.HandleFunc("POST /orders", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		w.Header().Set("Location", "http://"+r.Host+"/orders/1004?item="+url.QueryEscape(r.FormValue("item")))
		w.WriteHeader(http.StatusSeeOther)
	})
	mux.HandleFunc("GET /away", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://example.com/login", http.StatusFound)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func through(t *testing.T, target string) *httptest.Server {
	t.Helper()
	u, _ := url.Parse(target)
	p := httptest.NewServer(Proxy(u))
	t.Cleanup(p.Close)
	return p
}

var noRedirects = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

// answer is what came back for a request.
type answer struct {
	status int
	header http.Header
}

// fetch asks for a page, the way a browser in the frame would, and
// reads it.
func fetch(t *testing.T, client *http.Client, method, address string, form url.Values) (answer, []byte) {
	t.Helper()
	var payload io.Reader
	if form != nil {
		payload = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(t.Context(), method, address, payload)
	if err != nil {
		t.Fatal(err)
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return answer{resp.StatusCode, resp.Header}, body
}

func TestProxyAddsTheScriptToPages(t *testing.T) {
	p := through(t, backend(t).URL)
	got, body := fetch(t, http.DefaultClient, http.MethodGet, p.URL+"/", nil)
	page := string(body)
	if !strings.HasPrefix(page, `<!doctype html><html><HEAD lang="en"><script>(() => {`) || !strings.Contains(page, "<title>Teashop</title>") {
		t.Errorf("page:\n%s", page)
	}
	if n, _ := strconv.Atoi(got.header.Get("Content-Length")); n != len(body) {
		t.Errorf("Content-Length %s for %d bytes", got.header.Get("Content-Length"), len(body))
	}
	// What forbids a frame, or the script, is taken away.
	if got.header.Get("X-Frame-Options") != "" || got.header.Get("Content-Security-Policy") != "" {
		t.Errorf("headers %v", got.header)
	}
}

// A browser asks for compressed pages; the page it gets has the script
// all the same.
func TestProxyAddsTheScriptToACompressedPage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			_, _ = io.WriteString(w, "<html><head></head><body>plain</body></html>")
			return
		}
		w.Header().Set("Content-Encoding", "gzip")
		z := gzip.NewWriter(w)
		_, _ = io.WriteString(z, "<html><head></head><body>squeezed</body></html>")
		_ = z.Close()
	}))
	t.Cleanup(srv.Close)
	p := through(t, srv.URL)
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, p.URL+"/", nil)
	req.Header.Set("Accept-Encoding", "gzip, deflate, br")
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	var body io.Reader = resp.Body
	if resp.Header.Get("Content-Encoding") == "gzip" {
		if body, err = gzip.NewReader(resp.Body); err != nil {
			t.Fatal(err)
		}
	}
	page, _ := io.ReadAll(body)
	if !strings.HasPrefix(string(page), "<html><head><script>") || !strings.Contains(string(page), "</script></head><body>") {
		t.Errorf("page:\n%s", page)
	}
}

func TestProxyLeavesWhatIsNotAPage(t *testing.T) {
	p := through(t, backend(t).URL)
	if _, body := fetch(t, http.DefaultClient, http.MethodGet, p.URL+"/api/orders", nil); string(body) != `{"orders":[]}` {
		t.Errorf("body %q", body)
	}
}

// A redirect to the sandbox's own address stays in the proxy; one to
// somewhere else goes there.
func TestProxyRedirects(t *testing.T) {
	p := through(t, backend(t).URL)
	got, _ := fetch(t, noRedirects, http.MethodPost, p.URL+"/orders", url.Values{"item": {"Genmaicha"}})
	if loc := got.header.Get("Location"); got.status != http.StatusSeeOther || loc != "/orders/1004?item=Genmaicha" {
		t.Errorf("%d, Location %q", got.status, loc)
	}
	got, _ = fetch(t, noRedirects, http.MethodGet, p.URL+"/away", nil)
	if loc := got.header.Get("Location"); loc != "https://example.com/login" {
		t.Errorf("Location %q", loc)
	}
}

func TestProxyToASandboxThatIsNotThere(t *testing.T) {
	gone := httptest.NewServer(http.NotFoundHandler())
	target := gone.URL
	gone.Close()
	p := through(t, target)
	got, body := fetch(t, http.DefaultClient, http.MethodGet, p.URL+"/", nil)
	if got.status != http.StatusBadGateway || !strings.Contains(string(body), "pit could not reach "+target) {
		t.Errorf("%d:\n%s", got.status, body)
	}
}

func TestInjectWithoutAHead(t *testing.T) {
	if got := string(inject([]byte("<p>bare</p>"))); !strings.HasPrefix(got, "<script>") || !strings.HasSuffix(got, "</script><p>bare</p>") {
		t.Errorf("inject = %q", got)
	}
}
