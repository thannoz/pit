package inspect

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"time"

	"github.com/chromedp/cdproto/emulation"
	cdplog "github.com/chromedp/cdproto/log"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"

	"github.com/thannoz/pit/internal/errs"
)

// ReplayOptions says how to replay.
type ReplayOptions struct {
	// Browser is the executable to run; empty finds one.
	Browser string
	// Password is typed where the reviewer typed one, which a
	// recording does not keep.
	Password string
	// OnStep hears of each step before it is taken, numbered from 1.
	OnStep func(n int, s Step)
	// Find is how long to look for an element a step needs: a page
	// may draw it a moment after it has loaded.
	Find time.Duration
	// Settle and Quiet are as in Options, after each step.
	Settle, Quiet time.Duration
}

// Replay does again what a recording says, in a browser nobody sees,
// on the sandbox at base, and reports what went wrong on the way.
func Replay(ctx context.Context, base string, steps []Step, o ReplayOptions) (Report, error) {
	if o.Find == 0 {
		o.Find = 5 * time.Second
	}
	if o.Settle == 0 {
		o.Settle = 10 * time.Second
	}
	if o.Quiet == 0 {
		o.Quiet = defaultQuiet
	}
	b, err := url.Parse(base)
	if err != nil {
		return Report{}, err
	}
	for i, s := range steps {
		if s.Secret && o.Password == "" {
			return Report{}, errs.New("step %d types a password, which a recording does not keep", i+1).
				WithHint("--password gives the one to type")
		}
	}
	bctx, cancel, browser, err := startBrowser(ctx, o.Browser, true)
	if err != nil {
		return Report{}, err
	}
	defer cancel()

	rec := newRecorder()
	chromedp.ListenTarget(bctx, rec.handle)
	if err := chromedp.Run(bctx, network.Enable(), runtime.Enable(), cdplog.Enable(), page.Enable(),
		emulation.SetDeviceMetricsOverride(Width, Height, 1, false)); err != nil {
		return Report{}, loadFailure(err, browser, base)
	}

	for i, s := range steps {
		if o.OnStep != nil {
			o.OnStep(i+1, s)
		}
		// What a step sets off -- a form sent, a script's request --
		// starts a moment after it; waiting counts from the step.
		rec.touch()
		if err := replayStep(bctx, b, s, o); err != nil {
			if ctx.Err() != nil {
				return Report{}, ctx.Err()
			}
			return Report{}, errs.Wrap(err, "step %d, %s", i+1, s)
		}
		rec.settle(bctx, o.Settle, o.Quiet)
	}

	var now string
	if err := chromedp.Run(bctx, chromedp.Location(&now)); err != nil {
		return Report{}, err
	}
	return Report{URL: now, Problems: rec.problems(), Pending: rec.pending()}, nil
}

func replayStep(ctx context.Context, base *url.URL, s Step, o ReplayOptions) error {
	if s.Action == Goto {
		target, err := base.Parse(s.Path)
		if err != nil {
			return err
		}
		return chromedp.Run(ctx, chromedp.Navigate(target.String()))
	}

	value := s.Value
	if s.Secret {
		value = o.Password
	}
	step, err := json.Marshal(map[string]string{
		"action": string(s.Action), "selector": s.Selector, "text": s.Text, "value": value,
	})
	if err != nil {
		return err
	}
	deadline := time.Now().Add(o.Find)
	for {
		var outcome string
		if err := chromedp.Run(ctx, chromedp.Evaluate("("+replayJS+")("+string(step)+")", &outcome)); err != nil {
			return err
		}
		switch {
		case outcome == "ok" && s.Action == Press:
			return chromedp.Run(ctx, chromedp.KeyEvent("\r"))
		case outcome == "ok":
			return nil
		case outcome != "missing":
			return errs.New("%s", outcome)
		case time.Now().After(deadline):
			var where string
			_ = chromedp.Run(ctx, chromedp.Location(&where))
			return errs.New("nothing on %s is %s", pathOf(where), describeTarget(s)).
				WithHint("the page is not what it was when this was recorded; the steps before this one are what got here")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func pathOf(address string) string {
	u, err := url.Parse(address)
	if err != nil {
		return address
	}
	return u.RequestURI()
}

func describeTarget(s Step) string {
	if s.Text != "" {
		return fmt.Sprintf("%q (%s)", s.Text, s.Selector)
	}
	return s.Selector
}

// String is a step the way a person would say it.
func (s Step) String() string {
	name := s.Selector
	if s.Text != "" {
		name = fmt.Sprintf("%q", s.Text)
	}
	switch s.Action {
	case Goto:
		return "open " + s.Path
	case Fill:
		if s.Secret {
			return "type the password into " + name
		}
		return fmt.Sprintf("type %q into %s", s.Value, name)
	case Select:
		return fmt.Sprintf("choose %q in %s", orElse(s.Choice, s.Value), name)
	case Press:
		return fmt.Sprintf("press %s in %s", s.Key, name)
	default:
		return "click " + name
	}
}

func orElse(s, fallback string) string {
	if s != "" {
		return s
	}
	return fallback
}

// replayJS finds a step's element and does to it what the step says.
// It answers "ok", "missing" while there is nothing to find, or what
// else is wrong. The element is found by its selector, or else by what
// it says, the way the recording described it.
const replayJS = `(step) => {
  const q = (sel) => { try { return document.querySelector(sel); } catch (e) { return null; } };
` + textOfJS + `
  let el = step.selector ? q(step.selector) : null;
  const kind = step.action === 'click' ? 'a,button,input,label,summary,[role],[onclick]' :
    step.action === 'select' ? 'select' : 'input,textarea';
  const byText = () => step.text ? Array.from(document.querySelectorAll(kind)).find(c => textOf(c) === step.text) || null : null;
  if (el && step.text && textOf(el) !== step.text) el = byText() || el;
  if (!el) el = byText();
  if (!el) return 'missing';
  el.scrollIntoView({block: 'center'});
  const setValue = (v) => {
    const proto = el.matches('textarea') ? HTMLTextAreaElement.prototype : el.matches('select') ? HTMLSelectElement.prototype : HTMLInputElement.prototype;
    Object.getOwnPropertyDescriptor(proto, 'value').set.call(el, v);
    el.dispatchEvent(new Event('input', {bubbles: true}));
    el.dispatchEvent(new Event('change', {bubbles: true}));
  };
  switch (step.action) {
    case 'click': el.click(); return 'ok';
    case 'fill': el.focus(); setValue(step.value); return 'ok';
    case 'select':
      if (!Array.from(el.options || []).some(o => o.value === step.value)) return 'there is no option ' + JSON.stringify(step.value) + ' to choose';
      el.focus(); setValue(step.value); return 'ok';
    case 'press': el.focus(); return 'ok';
  }
  return 'cannot ' + step.action;
}`
