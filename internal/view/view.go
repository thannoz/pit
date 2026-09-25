// Package view shows two sandboxes side by side in one browser page:
// a pull request and the branch it goes into, scrolled together.
//
// The two run on ports of their own, which makes them two origins, and a
// page cannot read or set the scroll position of another origin's frame.
// So each is shown through a proxy that adds a script to its pages; the
// script and the page around the frames talk with postMessage, which
// works across origins.
package view

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/thannoz/pit/internal/errs"
)

// Side is one of the two things shown.
type Side struct {
	// Label says what it is: "#482", "base".
	Label string
	// Detail says what it runs: the branch, the commit.
	Detail string
	// Target is where it runs.
	Target string
}

// Serve shows left and right side by side until ctx ends, and reports
// the address of the page as soon as it is served.
func Serve(ctx context.Context, left, right Side, ready func(page string)) error {
	var listeners []net.Listener
	defer func() {
		for _, l := range listeners {
			_ = l.Close()
		}
	}()
	listen := func() (net.Listener, error) {
		l, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
		if err != nil {
			return nil, errs.Wrap(err, "cannot listen on this machine")
		}
		listeners = append(listeners, l)
		return l, nil
	}

	var servers []*http.Server
	frames := make([]string, 2)
	for i, side := range []Side{left, right} {
		target, err := url.Parse(side.Target)
		if err != nil || target.Host == "" {
			return errs.New("%q is not an address to show", side.Target)
		}
		l, err := listen()
		if err != nil {
			return err
		}
		frames[i] = "http://" + l.Addr().String() + "/"
		servers = append(servers, serve(l, Proxy(target)))
	}
	l, err := listen()
	if err != nil {
		return err
	}
	servers = append(servers, serve(l, page(left, right, frames[0], frames[1])))

	if ready != nil {
		ready("http://" + l.Addr().String() + "/")
	}
	<-ctx.Done()
	shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	for _, s := range servers {
		_ = s.Shutdown(shutdown)
	}
	return nil
}

func serve(l net.Listener, h http.Handler) *http.Server {
	s := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := s.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
			_ = err // the page says what is not there
		}
	}()
	return s
}

// Proxy passes everything on to target, adding the frame's script to
// its pages and taking away what forbids showing them in a frame.
func Proxy(target *url.URL) http.Handler {
	p := &httputil.ReverseProxy{Rewrite: func(r *httputil.ProxyRequest) {
		r.SetURL(target)
		r.Out.Host = target.Host
		// Pages have the script added, which a compressed page would
		// not take.
		r.Out.Header.Del("Accept-Encoding")
	}}
	p.ModifyResponse = func(resp *http.Response) error {
		h := resp.Header
		h.Del("X-Frame-Options")
		if csp := h.Get("Content-Security-Policy"); csp != "" {
			// A policy can forbid frames, and the added script: the
			// page is shown on the reviewer's own machine, to them.
			h.Del("Content-Security-Policy")
		}
		// A redirect to the sandbox's own address goes to the same
		// place through the proxy.
		if loc := h.Get("Location"); loc != "" {
			if u, err := url.Parse(loc); err == nil && u.Host == target.Host {
				h.Set("Location", u.RequestURI())
			}
		}
		if !strings.HasPrefix(h.Get("Content-Type"), "text/html") || resp.Body == nil {
			return nil
		}
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			return err
		}
		body = inject(body)
		resp.Body = io.NopCloser(bytes.NewReader(body))
		resp.ContentLength = int64(len(body))
		h.Set("Content-Length", strconv.Itoa(len(body)))
		return nil
	}
	p.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = fmt.Fprintf(w, "<p>pit could not reach %s: %s</p>", template.HTMLEscapeString(target.String()), template.HTMLEscapeString(err.Error()))
	}
	return p
}

var head = regexp.MustCompile(`(?i)<head[^>]*>`)

// inject adds the frame's script at the start of a page's head, or of
// the page when it has none.
func inject(page []byte) []byte {
	tag := []byte("<script>" + frameJS + "</script>")
	if loc := head.FindIndex(page); loc != nil {
		return append(append(append([]byte{}, page[:loc[1]]...), tag...), page[loc[1]:]...)
	}
	return append(tag, page...)
}

// frameJS runs in each shown page: it says where the page is scrolled
// and which page it is, and scrolls and goes where it is told.
const frameJS = `(() => {
  if (window.top === window || window.__pitSide) return;
  window.__pitSide = true;
  // Where the page was told to scroll: the scroll that follows is an
  // echo, reported so the page around knows, but not passed on.
  let told = null;
  const report = () => {
    const at = {x: Math.round(scrollX), y: Math.round(scrollY)};
    const echo = !!told && Math.abs(told.x - at.x) <= 1 && Math.abs(told.y - at.y) <= 1;
    told = null;
    parent.postMessage({pit: 'scroll', x: at.x, y: at.y, echo: echo}, '*');
  };
  let pending = false;
  addEventListener('scroll', () => {
    if (pending) return;
    pending = true;
    requestAnimationFrame(() => { pending = false; report(); });
  }, {passive: true});
  addEventListener('message', (e) => {
    if (e.source !== parent) return;
    const m = e.data || {};
    if (m.pit === 'scrollTo') {
      told = {x: m.x, y: m.y};
      // Counted where a test can see it: a frame told to scroll by
      // its own echo would pull the reviewer back while they scroll.
      document.documentElement.dataset.pitTold = (+document.documentElement.dataset.pitTold || 0) + 1;
      scrollTo(m.x, m.y);
    }
    if (m.pit === 'go' && location.pathname + location.search !== m.path) location.href = m.path;
  });
  addEventListener('load', () => parent.postMessage({pit: 'page', path: location.pathname + location.search}, '*'));
})();`

func page(left, right Side, leftFrame, rightFrame string) http.Handler {
	t := template.Must(template.New("page").Parse(pageHTML))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = t.Execute(w, map[string]any{"Left": left, "Right": right, "LeftFrame": leftFrame, "RightFrame": rightFrame})
	})
}

const pageHTML = `<!doctype html>
<html><head><meta charset="utf-8"><title>{{.Left.Label}} and {{.Right.Label}}</title>
<style>
  html, body { margin: 0; height: 100%; font: 13px -apple-system, system-ui, sans-serif; }
  body { display: flex; flex-direction: column; }
  header { display: flex; gap: 16px; align-items: center; padding: 6px 12px; border-bottom: 1px solid #ccc; background: #f6f6f6; }
  header .side { flex: 1; overflow: hidden; white-space: nowrap; text-overflow: ellipsis; }
  header .detail { color: #666; margin-left: 6px; }
  main { flex: 1; display: flex; min-height: 0; }
  iframe { flex: 1; border: 0; min-width: 0; }
  iframe + iframe { border-left: 2px solid #ccc; }
</style></head>
<body>
<header>
  <span class="side"><b>{{.Left.Label}}</b><span class="detail">{{.Left.Detail}}</span></span>
  <label><input type="checkbox" id="scroll" checked> scroll together</label>
  <label><input type="checkbox" id="follow"> follow pages</label>
  <span class="side"><b>{{.Right.Label}}</b><span class="detail">{{.Right.Detail}}</span></span>
</header>
<main>
  <iframe id="left" src="{{.LeftFrame}}"></iframe>
  <iframe id="right" src="{{.RightFrame}}"></iframe>
</main>
<script>
const frames = {left: document.getElementById('left'), right: document.getElementById('right')};
const scroll = document.getElementById('scroll'), follow = document.getElementById('follow');
window.pit = {positions: {}, paths: {}};
addEventListener('message', (e) => {
  const side = e.source === frames.left.contentWindow ? 'left' : e.source === frames.right.contentWindow ? 'right' : null;
  if (!side) return;
  const other = side === 'left' ? 'right' : 'left';
  const m = e.data || {};
  if (m.pit === 'scroll') {
    pit.positions[side] = m.y;
    if (scroll.checked && !m.echo) frames[other].contentWindow.postMessage({pit: 'scrollTo', x: m.x, y: m.y}, '*');
  }
  if (m.pit === 'page') {
    pit.paths[side] = m.path;
    if (follow.checked && pit.paths[other] !== m.path) frames[other].contentWindow.postMessage({pit: 'go', path: m.path}, '*');
  }
});
</script>
</body></html>`
