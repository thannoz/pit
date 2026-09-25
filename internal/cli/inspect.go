package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/inspect"
	"github.com/thannoz/pit/internal/sandbox"
	"github.com/thannoz/pit/internal/state"
	"github.com/thannoz/pit/internal/ui"
)

// capture loads a page in a browser; a variable so that tests need no
// Chrome.
var capture = func(ctx context.Context, page string, shot inspect.Shot) (inspect.Report, error) {
	return inspect.Capture(ctx, page, inspect.Options{Screenshot: shot})
}

// inspection is what pit inspect --json prints.
type inspection struct {
	inspect.Report
	Screenshot *savedShot `json:"screenshot,omitempty"`
}

type savedShot struct {
	File     string `json:"file"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	FullPage bool   `json:"full_page"`
	// PageHeight is how tall the page is when the picture stops short
	// of its bottom.
	PageHeight int `json:"page_height,omitempty"`
}

func newInspectCmd(opts *globalOptions) *cobra.Command {
	var file string
	var fullPage bool
	cmd := &cobra.Command{
		Use:   "inspect <pull request number> [path]",
		Short: "Load a page of a sandbox and report what goes wrong on it",
		Long: `Load a page of a sandbox in a browser nobody sees, the way a reviewer's
would, and report what went wrong: JavaScript that threw, errors and
warnings the page logged, and requests that failed or were answered
with an error -- including the ones it makes after it has loaded.

Needs Chrome or Chromium; PIT_BROWSER names one when pit cannot find
it. What pit loads here does not count as looked at in pit what.

--screenshot saves a picture of the page as a PNG: what a window of
1280 by 800 shows, or with --full-page all of it down to its bottom.`,
		Example: `  pit inspect 482
  pit inspect 482 /orders/1001
  pit inspect 482 /orders/1001 --screenshot order.png --full-page`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(c *cobra.Command, args []string) error {
			if fullPage && file == "" {
				return errs.New("--full-page says how much of the page to picture, and there is no picture").
					WithHint("add --screenshot <file>")
			}
			if err := canWrite(file); err != nil {
				return err
			}
			box, err := sandboxFor(c, args[0])
			if err != nil {
				return err
			}
			path := "/"
			if len(args) == 2 {
				path = args[1]
			}
			page, err := pageOf(box.URL, path)
			if err != nil {
				return err
			}
			m, err := manager()
			if err != nil {
				return err
			}
			entry, err := m.Find(c.Context(), box.RepoRef, box.PR)
			if err != nil {
				return err
			}
			if !entry.AnyRunning() {
				return errs.New("nothing is running for #%d to load a page from", box.PR).
					WithHint("`pit %d` brings the sandbox up again", box.PR)
			}

			shot := inspect.NoShot
			switch {
			case fullPage:
				shot = inspect.FullPage
			case file != "":
				shot = inspect.Window
			}
			from := time.Now()
			report, err := capture(c.Context(), page, shot)
			if rerr := recordBrowsing(m, box, state.Span{From: from, To: time.Now()}); err == nil {
				err = rerr
			}
			if err != nil {
				return err
			}
			result := inspection{Report: report}
			if s := report.Screenshot; s != nil {
				if err := os.WriteFile(file, s.PNG, 0o644); err != nil {
					return errs.Wrap(err, "cannot save the picture to %s", file)
				}
				result.Screenshot = &savedShot{File: file, Width: s.Width, Height: s.Height, FullPage: s.FullPage, PageHeight: s.Cut}
			}

			out := ui.New(c.OutOrStdout(), c.ErrOrStderr())
			if opts.jsonOutput {
				enc := json.NewEncoder(out.Out())
				enc.SetIndent("", "  ")
				return enc.Encode(result)
			}
			writeInspection(out, report)
			if s := result.Screenshot; s != nil {
				writeShot(out, *s)
			}
			return out.Err()
		},
	}
	cmd.Flags().StringVar(&file, "screenshot", "", "save a picture of the page to `file`, a PNG")
	cmd.Flags().BoolVar(&fullPage, "full-page", false, "picture the whole page, not only what the window shows")
	return cmd
}

// canWrite finds out before a browser is started whether the picture
// has somewhere to go.
func canWrite(file string) error {
	if file == "" {
		return nil
	}
	if info, err := os.Stat(file); err == nil && info.IsDir() {
		return errs.New("%s is a directory", file).WithHint("give the picture a file name, like %s", filepath.Join(file, "page.png"))
	}
	dir := filepath.Dir(file)
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return errs.New("cannot save the picture to %s: there is no directory %s", file, dir)
	}
	return nil
}

func writeShot(out *ui.Printer, s savedShot) {
	out.Printf("Screenshot saved to %s (%d×%d).\n", s.File, s.Width, s.Height)
	if s.PageHeight > 0 {
		out.Printf("  The page is %d pixels tall; the picture stops at %d, as tall as one can be.\n", s.PageHeight, s.Height)
	}
}

// pageOf joins a sandbox's URL and a path someone typed, which may be
// a path, a path with a query, or the whole URL copied from a browser.
func pageOf(base, path string) (string, error) {
	b, err := url.Parse(base)
	if err != nil {
		return "", errs.Wrap(err, "the sandbox's URL %q cannot be read", base)
	}
	p, err := url.Parse(path)
	if err != nil {
		return "", errs.New("%q is not a path", path).WithHint("give one like /orders/1001")
	}
	if p.IsAbs() && p.Host != b.Host {
		return "", errs.New("%s is not a page of this sandbox, which is at %s", path, base)
	}
	return b.ResolveReference(p).String(), nil
}

// recordBrowsing notes when pit loaded the sandbox's pages, so that pit
// what does not take its requests for the reviewer's.
func recordBrowsing(m *sandbox.Manager, box state.Sandbox, span state.Span) error {
	return m.Store.Update(func(f *state.File) error {
		current, ok := f.Find(box.RepoRef, box.PR)
		if !ok {
			return nil
		}
		current.Browsed = append(current.Browsed, span)
		if extra := len(current.Browsed) - state.MaxBrowsed; extra > 0 {
			current.Browsed = current.Browsed[extra:]
		}
		f.Put(current)
		return nil
	})
}

func writeInspection(out *ui.Printer, r inspect.Report) {
	defer func() {
		for _, p := range r.Pending {
			out.Printf("  … %s  still unanswered when pit stopped waiting\n", p)
		}
	}()
	if len(r.Problems) == 0 {
		out.Printf("Nothing went wrong on %s.\n", r.URL)
		return
	}
	out.Printf("%s\n", r.URL)
	for _, p := range r.Problems {
		mark := "✗"
		if p.Level == "warning" {
			mark = "!"
		}
		out.Printf("  %s %s\n", mark, describeProblem(p))
	}
	errors, warnings := r.Errors(), len(r.Problems)-r.Errors()
	var parts []string
	if errors > 0 {
		parts = append(parts, plural(errors, "error", "errors"))
	}
	if warnings > 0 {
		parts = append(parts, plural(warnings, "warning", "warnings"))
	}
	out.Printf("%s.\n", strings.Join(parts, ", "))
}

// describeProblem is one line about one problem, the way the developer
// tools would show it.
func describeProblem(p inspect.Problem) string {
	switch p.Kind {
	case inspect.Request:
		if p.Status == 0 {
			return fmt.Sprintf("%s %s  failed: %s", p.Method, p.URL, p.Text)
		}
		return fmt.Sprintf("%s %s  %d %s", p.Method, p.URL, p.Status, p.Text)
	case inspect.Console:
		return withSource("console."+map[string]string{"error": "error", "warning": "warn"}[p.Level]+": "+p.Text, p.Source)
	default:
		return withSource(p.Text, orElse(p.Source, p.URL))
	}
}

func withSource(text, source string) string {
	if source == "" {
		return text
	}
	return text + "  (" + source + ")"
}

func orElse(s, fallback string) string {
	if s != "" {
		return s
	}
	return fallback
}
