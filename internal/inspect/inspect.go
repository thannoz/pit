package inspect

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	goruntime "runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/emulation"
	cdplog "github.com/chromedp/cdproto/log"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"

	"github.com/thannoz/pit/internal/errs"
)

// Kind is what went wrong.
type Kind string

// The kinds of problem a page can have.
const (
	// Exception is JavaScript that threw and was not caught.
	Exception Kind = "exception"
	// Console is something the page itself reported as an error or a
	// warning.
	Console Kind = "console"
	// Request is a request that failed or was answered with an error.
	Request Kind = "request"
	// Browser is what the browser reported on its own: a blocked
	// script, a content security policy that refused something.
	Browser Kind = "browser"
)

// Problem is one thing that went wrong on a page.
type Problem struct {
	Kind Kind `json:"kind"`
	// Level is "error" or "warning".
	Level string `json:"level"`
	// Text is what was said: the exception's message, the console's
	// words, the browser's reason a request failed.
	Text string `json:"text,omitempty"`
	// Source is where in the page's code it happened: app.js:12.
	Source string `json:"source,omitempty"`
	// Method, URL and Status describe a failed request. Status is 0
	// when there was no answer at all.
	Method string    `json:"method,omitempty"`
	URL    string    `json:"url,omitempty"`
	Status int       `json:"status,omitempty"`
	At     time.Time `json:"at"`
}

// Report is what loading a page turned up.
type Report struct {
	URL      string    `json:"url"`
	Problems []Problem `json:"problems"`
	// Pending are requests still unanswered when pit stopped waiting:
	// a page that never finishes loading says something too.
	Pending []string `json:"pending,omitempty"`
	// Screenshot is the picture asked for with Options.Screenshot.
	Screenshot *Screenshot `json:"-"`
}

// Shot is which picture of a page to take.
type Shot int

// The pictures of a page there are.
const (
	// NoShot takes none.
	NoShot Shot = iota
	// Window is what fits in the browser's window, the way the reviewer
	// sees the page when it opens.
	Window
	// FullPage is the whole page, from its top to its bottom.
	FullPage
)

// Screenshot is a picture of a page.
type Screenshot struct {
	PNG           []byte
	Width, Height int
	FullPage      bool
	// Cut is the page's height when it was taller than a picture can be
	// and this one stops short of its bottom; 0 when it shows it all.
	Cut int
}

// Width and Height are the size of the browser's window, in CSS pixels,
// one to a pixel of the picture: a laptop's screen.
const (
	Width  = 1280
	Height = 800
)

// maxSide is as long as a picture can be. Chrome paints a page in
// tiles of at most this, and past it it repeats what it painted
// instead of the page.
const maxSide = 16384

// Errors counts the problems that are errors rather than warnings.
func (r Report) Errors() int {
	n := 0
	for _, p := range r.Problems {
		if p.Level == "error" {
			n++
		}
	}
	return n
}

// Options says how to load a page.
type Options struct {
	// Browser is the executable to run; empty finds one.
	Browser string
	// Settle is how long to wait at most, after the page has loaded,
	// for what it goes on to request: an error that comes from a
	// fetch a second later is as much the page's as one on load.
	Settle time.Duration
	// Quiet is how long nothing has to be in flight for the page to
	// count as settled.
	Quiet time.Duration
	// Screenshot is the picture to take once the page has settled.
	Screenshot Shot
}

const (
	defaultSettle = 5 * time.Second
	defaultQuiet  = 500 * time.Millisecond
)

// Capture loads a page in a browser nobody sees and reports what went
// wrong on it.
//
// It is the browser a reviewer would have used, minus the reviewer:
// JavaScript runs, requests go out, and the errors that would have
// shown in the developer tools are collected instead of scrolling by
// unread.
func Capture(ctx context.Context, url string, o Options) (Report, error) {
	if o.Settle == 0 {
		o.Settle = defaultSettle
	}
	if o.Quiet == 0 {
		o.Quiet = defaultQuiet
	}
	bctx, cancel, browser, err := startBrowser(ctx, o.Browser, true)
	if err != nil {
		return Report{}, err
	}
	defer cancel()

	rec := newRecorder()
	chromedp.ListenTarget(bctx, rec.handle)

	err = chromedp.Run(bctx,
		network.Enable(),
		runtime.Enable(),
		cdplog.Enable(),
		// The window's frame takes some of its size; the page gets all
		// of it this way.
		emulation.SetDeviceMetricsOverride(Width, Height, 1, false),
		chromedp.Navigate(url),
	)
	if err != nil {
		if ctx.Err() != nil {
			return Report{}, ctx.Err()
		}
		return Report{}, loadFailure(err, browser, url)
	}
	rec.settle(bctx, o.Settle, o.Quiet)
	report := Report{URL: url, Problems: rec.problems(), Pending: rec.pending()}
	if o.Screenshot != NoShot {
		shot, err := screenshot(bctx, o.Screenshot == FullPage)
		if err != nil {
			if ctx.Err() != nil {
				return Report{}, ctx.Err()
			}
			return Report{}, errs.Wrap(err, "the browser could not take a picture of %s", url)
		}
		report.Screenshot = &shot
	}
	return report, nil
}

// screenshot takes a picture of what the window shows, or of the whole
// page.
func screenshot(bctx context.Context, full bool) (Screenshot, error) {
	shot := Screenshot{Width: Width, Height: Height, FullPage: full}
	err := chromedp.Run(bctx, chromedp.ActionFunc(func(ctx context.Context) error {
		capture := page.CaptureScreenshot().WithFormat(page.CaptureScreenshotFormatPng)
		if full {
			_, _, _, _, _, content, err := page.GetLayoutMetrics().Do(ctx)
			if err != nil {
				return err
			}
			// The page is never smaller than the window: a short one
			// fills it, the way the window shows it.
			w, h := int(content.Width), int(content.Height)
			if h > maxSide {
				shot.Cut, h = h, maxSide
			}
			shot.Width, shot.Height = min(w, maxSide), h
			capture = capture.
				WithCaptureBeyondViewport(true).
				WithClip(&page.Viewport{Width: float64(shot.Width), Height: float64(shot.Height), Scale: 1})
		}
		var err error
		shot.PNG, err = capture.Do(ctx)
		return err
	}))
	return shot, err
}

// startBrowser starts Chrome, headless or in a window, and returns a
// context for its first tab.
func startBrowser(ctx context.Context, browser string, headless bool) (context.Context, context.CancelFunc, string, error) {
	if browser == "" {
		found, err := FindBrowser()
		if err != nil {
			return nil, nil, "", err
		}
		browser = found
	}
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(browser),
		chromedp.WindowSize(Width, Height),
	)
	if !headless {
		opts = append(opts, chromedp.Flag("headless", false))
	}
	// Chrome refuses to start its sandbox as root, which is what a
	// container usually is.
	if os.Geteuid() == 0 {
		opts = append(opts, chromedp.NoSandbox)
	}
	actx, cancelAlloc := chromedp.NewExecAllocator(ctx, opts...)
	bctx, cancelBrowser := chromedp.NewContext(actx)
	return bctx, func() { cancelBrowser(); cancelAlloc() }, browser, nil
}

// loadFailure says why a page could not be loaded: no browser to load
// it with, or nothing answering there.
func loadFailure(err error, browser, url string) error {
	if errors.Is(err, exec.ErrNotFound) || strings.Contains(err.Error(), "executable file not found") {
		return errs.Wrap(err, "cannot start the browser at %s", browser).
			WithHint("set PIT_BROWSER to a Chrome or Chromium executable")
	}
	return errs.Wrap(err, "the browser could not load %s", url).
		WithHint("`pit ls` shows whether the sandbox is running")
}

// recorder turns the browser's events into problems.
type recorder struct {
	mu       sync.Mutex
	found    []Problem
	requests map[network.RequestID]*network.Request
	inflight map[network.RequestID]bool
	changed  time.Time
}

func newRecorder() *recorder {
	return &recorder{
		requests: map[network.RequestID]*network.Request{},
		inflight: map[network.RequestID]bool{},
		changed:  time.Now(),
	}
}

func (r *recorder) add(p Problem) {
	p.At = time.Now()
	r.found = append(r.found, p)
}

func (r *recorder) problems() []Problem {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Problem{}, r.found...)
}

// touch counts now as a moment something happened.
func (r *recorder) touch() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.changed = time.Now()
}

func (r *recorder) pending() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for id := range r.inflight {
		if req := r.requests[id]; req != nil {
			out = append(out, req.Method+" "+req.URL)
		}
	}
	slices.Sort(out)
	return out
}

func (r *recorder) handle(ev any) {
	r.mu.Lock()
	defer r.mu.Unlock()

	switch e := ev.(type) {
	case *runtime.EventExceptionThrown:
		d := e.ExceptionDetails
		text := d.Text
		if d.Exception != nil && d.Exception.Description != "" {
			// The description is the message and the stack; the
			// first line is the message.
			text, _, _ = strings.Cut(d.Exception.Description, "\n")
		}
		r.add(Problem{Kind: Exception, Level: "error", Text: text, Source: source(d.URL, d.LineNumber, d.StackTrace)})

	case *runtime.EventConsoleAPICalled:
		var level string
		switch e.Type {
		case runtime.APITypeError, runtime.APITypeAssert:
			level = "error"
		case runtime.APITypeWarning:
			level = "warning"
		default:
			return
		}
		r.add(Problem{Kind: Console, Level: level, Text: consoleText(e.Args), Source: source("", 0, e.StackTrace)})

	case *cdplog.EventEntryAdded:
		// Requests are reported from the network, which says more
		// than "Failed to load resource"; the rest is the browser's
		// own word, a refused script or a mixed-content block.
		entry := e.Entry
		if entry.Source == cdplog.SourceNetwork || (entry.Level != cdplog.LevelError && entry.Level != cdplog.LevelWarning) {
			return
		}
		level := "warning"
		if entry.Level == cdplog.LevelError {
			level = "error"
		}
		r.add(Problem{Kind: Browser, Level: level, Text: entry.Text, URL: entry.URL})

	case *network.EventRequestWillBeSent:
		r.requests[e.RequestID] = e.Request
		// The browser asks for /favicon.ico on its own; the page is not
		// waiting for it, and neither is pit.
		if strings.HasSuffix(e.Request.URL, "/favicon.ico") && e.Type == network.ResourceTypeOther {
			return
		}
		r.inflight[e.RequestID] = true
		r.changed = time.Now()

	case *network.EventResponseReceived:
		// Every browser asks for /favicon.ico on its own, and most
		// development servers have none; the page did not ask for it.
		if e.Response.Status == 404 && strings.HasSuffix(e.Response.URL, "/favicon.ico") {
			return
		}
		if e.Response.Status >= 400 {
			method := ""
			if req := r.requests[e.RequestID]; req != nil {
				method = req.Method
			}
			r.add(Problem{Kind: Request, Level: "error", Method: method, URL: e.Response.URL,
				Status: int(e.Response.Status), Text: e.Response.StatusText})
		}

	case *network.EventLoadingFinished:
		delete(r.inflight, e.RequestID)
		r.changed = time.Now()

	case *network.EventLoadingFailed:
		delete(r.inflight, e.RequestID)
		r.changed = time.Now()
		// A request the page gave up on itself -- a navigation away,
		// an aborted fetch -- is not a failure of the server.
		if e.Canceled {
			return
		}
		p := Problem{Kind: Request, Level: "error", Text: e.ErrorText}
		if req := r.requests[e.RequestID]; req != nil {
			p.Method, p.URL = req.Method, req.URL
		}
		r.add(p)
	}
}

// settle waits until nothing has been in flight for quiet, or for at
// most max.
func (r *recorder) settle(ctx context.Context, max, quiet time.Duration) {
	deadline := time.Now().Add(max)
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for time.Now().Before(deadline) {
		r.mu.Lock()
		idle := len(r.inflight) == 0 && time.Since(r.changed) >= quiet
		r.mu.Unlock()
		if idle {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// source is where something happened: the first frame of the stack,
// or the URL and line given.
func source(url string, line int64, stack *runtime.StackTrace) string {
	if stack != nil && len(stack.CallFrames) > 0 {
		f := stack.CallFrames[0]
		url, line = f.URL, f.LineNumber
	}
	if url == "" {
		return ""
	}
	// Lines are counted from 0 by the protocol, from 1 by editors.
	return fmt.Sprintf("%s:%d", url, line+1)
}

// consoleText is what console.error was called with, the way the
// developer tools would print it.
func consoleText(args []*runtime.RemoteObject) string {
	parts := make([]string, 0, len(args))
	for _, a := range args {
		switch {
		case a.Type == runtime.TypeString:
			var s string
			if json.Unmarshal(a.Value, &s) == nil {
				parts = append(parts, s)
				continue
			}
		case len(a.Value) > 0:
			parts = append(parts, string(a.Value))
			continue
		}
		parts = append(parts, a.Description)
	}
	return strings.Join(parts, " ")
}

// FindBrowser looks for Chrome or Chromium: PIT_BROWSER first, then
// where each is usually installed.
func FindBrowser() (string, error) {
	if path := os.Getenv("PIT_BROWSER"); path != "" {
		if _, err := os.Stat(path); err != nil {
			return "", errs.New("PIT_BROWSER names %s, which does not exist", path)
		}
		return path, nil
	}
	var candidates []string
	if goruntime.GOOS == "darwin" {
		for _, app := range []string{
			"Google Chrome.app/Contents/MacOS/Google Chrome",
			"Chromium.app/Contents/MacOS/Chromium",
			"Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
			"Brave Browser.app/Contents/MacOS/Brave Browser",
		} {
			candidates = append(candidates, "/Applications/"+app)
			if home, err := os.UserHomeDir(); err == nil {
				candidates = append(candidates, home+"/Applications/"+app)
			}
		}
		for _, c := range candidates {
			if _, err := os.Stat(c); err == nil {
				return c, nil
			}
		}
	}
	for _, name := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "microsoft-edge", "brave-browser"} {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
	}
	return "", errs.New("no Chrome or Chromium found").
		WithHint("pit uses one to load pages the way a reviewer's browser would; install Chrome, or set PIT_BROWSER to one")
}
