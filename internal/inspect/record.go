package inspect

import (
	"context"
	"encoding/json"
	"net/url"
	"sync"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// Action is what a step of a recording does.
type Action string

// The actions a recording is made of.
const (
	// Goto opens an address, the way one typed into the address bar
	// or reached by going back.
	Goto Action = "goto"
	// Click presses a button, follows a link, ticks a box.
	Click Action = "click"
	// Fill types into a field.
	Fill Action = "fill"
	// Select chooses an option in a list.
	Select Action = "select"
	// Press presses a key in a field: Enter, which sends a form.
	Press Action = "press"
)

// Step is one thing the reviewer did on a page.
type Step struct {
	Action Action `json:"action"`
	// Path is where Goto goes: a path of the sandbox, or a whole URL
	// for somewhere else.
	Path string `json:"path,omitempty"`
	// Selector finds the element again; Text is what it says -- its
	// label, its words -- for a person reading the steps, and for
	// finding it when the selector no longer does.
	Selector string `json:"selector,omitempty"`
	Text     string `json:"text,omitempty"`
	// Value is what was typed or chosen; Choice is the chosen option's
	// words.
	Value  string `json:"value,omitempty"`
	Choice string `json:"choice,omitempty"`
	// Secret marks a password field: what was typed is not kept.
	Secret bool   `json:"secret,omitempty"`
	Key    string `json:"key,omitempty"`
}

// RecordOptions says how to record.
type RecordOptions struct {
	// Browser is the executable to run; empty finds one.
	Browser string
	// Headless records without a window. Drive then does what a
	// reviewer would, which is how the recording is tested.
	Headless bool
	Drive    func(ctx context.Context) error
	// OnStep hears of each step as it is taken.
	OnStep func(Step)
	// GIF is how much of the end of the recording to keep as a GIF:
	// the seconds before the reviewer stopped, which is where what they
	// found happened. Zero keeps none.
	GIF time.Duration
}

// Recorded is what a recording brings back.
type Recorded struct {
	Steps []Step
	// GIF shows the last seconds, when it was asked for and could be
	// made; GIFError says why not, when it could not.
	GIF      []byte
	GIFError error
}

// binding is the name the page reports what the reviewer did under.
const binding = "__pitRecord"

// Record opens a page in a browser the reviewer uses, and writes down
// what they do there -- the addresses they open, what they type, what
// they press -- until they close the window or ctx ends.
//
// What it writes down is meant to be done again, on another machine,
// by Replay: steps a person can read, with a way to find each element
// that does not depend on the reviewer's screen.
func Record(ctx context.Context, address string, o RecordOptions) (Recorded, error) {
	base, err := url.Parse(address)
	if err != nil {
		return Recorded{}, err
	}
	bctx, cancel, browser, err := startBrowser(ctx, o.Browser, o.Headless)
	if err != nil {
		return Recorded{}, err
	}
	defer cancel()

	rec := &recording{base: base, onStep: o.OnStep}
	chromedp.ListenTarget(bctx, rec.handle)
	if err := chromedp.Run(bctx,
		page.Enable(),
		runtime.Enable(),
		runtime.AddBinding(binding),
		chromedp.ActionFunc(func(ctx context.Context) error {
			_, err := page.AddScriptToEvaluateOnNewDocument(recorderJS).Do(ctx)
			return err
		}),
	); err != nil {
		return Recorded{}, loadFailure(err, browser, address)
	}
	var cast *screencast
	var castErr error
	if o.GIF > 0 {
		cast, castErr = startCast(bctx, o.GIF, 960, 600)
	}
	if err := chromedp.Run(bctx, chromedp.Navigate(address)); err != nil {
		if ctx.Err() != nil {
			return Recorded{Steps: rec.done()}, nil
		}
		return Recorded{}, loadFailure(err, browser, address)
	}

	if o.Drive != nil {
		if err := o.Drive(bctx); err != nil {
			return Recorded{}, err
		}
		// What the page reports arrives a moment after it happens.
		time.Sleep(300 * time.Millisecond)
	} else {
		waitForClose(ctx, bctx)
	}
	out := Recorded{Steps: rec.done()}
	switch {
	case castErr != nil:
		out.GIFError = castErr
	case cast != nil:
		out.GIF, out.GIFError = cast.gif(time.Now())
	}
	return out, nil
}

// waitForClose returns when the reviewer has closed every window of the
// browser, or ctx ends. On macOS closing the last window leaves the
// browser running, so what is asked is whether any page is left.
func waitForClose(ctx, bctx context.Context) {
	tick := time.NewTicker(300 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-bctx.Done():
			return
		case <-tick.C:
		}
		targets, err := chromedp.Targets(bctx)
		if err != nil {
			return
		}
		open := false
		for _, t := range targets {
			if t.Type == "page" {
				open = true
			}
		}
		if !open {
			return
		}
	}
}

// recording collects the steps from the browser's events.
type recording struct {
	mu     sync.Mutex
	base   *url.URL
	steps  []Step
	onStep func(Step)
	// requested is where the page itself last asked to go: a link, a
	// form, a script. Such a navigation follows from a step already
	// recorded and is not one of its own -- unless the reviewer went
	// somewhere else before it got there.
	requested string
	// byPage says the navigation under way is the one requested.
	byPage bool
	// started says the browser reports navigations as they start,
	// which older ones do not.
	started bool
	main    cdp.FrameID
}

func (r *recording) handle(ev any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch e := ev.(type) {
	case *runtime.EventBindingCalled:
		if e.Name != binding {
			return
		}
		var s Step
		if json.Unmarshal([]byte(e.Payload), &s) == nil {
			r.add(s)
		}
	case *page.EventFrameRequestedNavigation:
		if r.main == "" || e.FrameID == r.main {
			r.requested = e.URL
		}
	case *page.EventFrameStartedNavigating:
		if (r.main != "" && e.FrameID != r.main) || e.NavigationType == page.FrameStartedNavigatingNavigationTypeSameDocument ||
			e.NavigationType == page.FrameStartedNavigatingNavigationTypeHistorySameDocument {
			return
		}
		r.started = true
		r.byPage = r.requested != "" && e.URL == r.requested
		r.requested = ""
	case *page.EventFrameNavigated:
		if e.Frame.ParentID != "" {
			return
		}
		r.main = e.Frame.ID
		if !r.started {
			r.byPage, r.requested = r.requested != "", ""
		}
		if !r.byPage {
			if u, err := url.Parse(e.Frame.URL); err == nil && (u.Scheme == "http" || u.Scheme == "https") {
				r.add(Step{Action: Goto, Path: r.path(u)})
			}
		}
		r.byPage = false
	}
}

// add keeps a step, unless it says again what was just said: a field
// reports what was typed in it when Enter is pressed, and again when it
// is left.
func (r *recording) add(s Step) {
	if s.Action == Fill {
		for i := len(r.steps) - 1; i >= 0 && r.steps[i].Selector == s.Selector; i-- {
			if r.steps[i] == s {
				return
			}
		}
	}
	r.steps = append(r.steps, s)
	if r.onStep != nil {
		r.onStep(s)
	}
}

func (r *recording) done() []Step {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Step(nil), r.steps...)
}

// path is an address the way a recording keeps it: a path of the
// sandbox, which works wherever the sandbox runs, or whole when it is
// somewhere else.
func (r *recording) path(u *url.URL) string {
	if u.Host != r.base.Host {
		return u.String()
	}
	p := u.RequestURI()
	if u.Fragment != "" {
		p += "#" + u.Fragment
	}
	return p
}

// textOfJS names an element the way a person would: a field by its
// label, or its name -- a placeholder is as often an example as a name
// -- and anything else by what it says. Recording and replaying name
// elements alike, or a replay would not find what was recorded.
const textOfJS = `  const clean = (s) => (s || '').replace(/\s+/g, ' ').trim().slice(0, 80);
  const textOf = (el) => {
    if (el.matches('input,textarea,select')) {
      const label = el.labels && el.labels.length ? el.labels[0].innerText : '';
      return clean(label || el.getAttribute('aria-label') || el.name || el.placeholder || el.id);
    }
    return clean(el.innerText || el.value || el.getAttribute('aria-label') || el.title || el.alt);
  };`

// recorderJS runs in every page and reports what the reviewer does
// there. It listens before the page's own handlers, so a page that
// stops an event still has it recorded.
const recorderJS = `(() => {
  if (window.__pitRecording) return;
  window.__pitRecording = true;
  const send = (step) => { try { window.__pitRecord(JSON.stringify(step)); } catch (e) {} };
  const textLike = (el) => el.matches('textarea') ||
    (el.matches('input') && !['checkbox','radio','submit','button','reset','image','file','hidden','range','color'].includes((el.type || '').toLowerCase()));
  const unique = (sel) => { try { return document.querySelectorAll(sel).length === 1; } catch (e) { return false; } };
  const quote = (v) => '"' + v.replace(/\\/g, '\\\\').replace(/"/g, '\\"') + '"';
  const selectorOf = (el) => {
    if (el.id && !/\d{4,}/.test(el.id) && unique('#' + CSS.escape(el.id))) return '#' + CSS.escape(el.id);
    const tag = el.tagName.toLowerCase();
    for (const attr of ['data-testid', 'data-test', 'data-cy', 'name', 'aria-label', 'placeholder', 'href']) {
      const v = el.getAttribute(attr);
      if (v && unique(tag + '[' + attr + '=' + quote(v) + ']')) return tag + '[' + attr + '=' + quote(v) + ']';
    }
    const parts = [];
    for (let e = el; e && e.nodeType === 1 && e !== document.documentElement; e = e.parentElement) {
      if (e !== el && e.id && !/\d{4,}/.test(e.id) && unique('#' + CSS.escape(e.id))) { parts.unshift('#' + CSS.escape(e.id)); break; }
      let part = e.tagName.toLowerCase();
      const same = e.parentElement ? Array.from(e.parentElement.children).filter(c => c.tagName === e.tagName) : [];
      if (same.length > 1) part += ':nth-of-type(' + (same.indexOf(e) + 1) + ')';
      parts.unshift(part);
    }
    return parts.join(' > ');
  };
` + textOfJS + `
  const clickable = 'a,button,input,label,summary,select,textarea,[role=button],[role=link],[role=tab],[role=menuitem],[role=checkbox],[onclick]';
  document.addEventListener('click', (e) => {
    const el = (e.target.closest && e.target.closest(clickable)) || e.target;
    if (!el || el === document.body || el === document.documentElement) return;
    if (textLike(el) || el.matches('select')) return;
    send({action: 'click', selector: selectorOf(el), text: textOf(el)});
  }, true);
  const fill = (el) => {
    const secret = (el.type || '').toLowerCase() === 'password';
    send({action: 'fill', selector: selectorOf(el), text: textOf(el), value: secret ? '' : el.value, secret: secret || undefined});
  };
  document.addEventListener('change', (e) => {
    const el = e.target;
    if (el.matches('select')) {
      const option = el.options[el.selectedIndex];
      send({action: 'select', selector: selectorOf(el), text: textOf(el), value: el.value, choice: option ? clean(option.text) : undefined});
    } else if (textLike(el)) {
      fill(el);
    }
  }, true);
  document.addEventListener('keydown', (e) => {
    const el = e.target;
    if (e.key !== 'Enter' || !el.matches || !el.matches('input') || !textLike(el)) return;
    fill(el);
    send({action: 'press', selector: selectorOf(el), text: textOf(el), key: 'Enter'});
  }, true);
})();`
