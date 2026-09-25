package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
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
var capture = func(ctx context.Context, page string) (inspect.Report, error) {
	return inspect.Capture(ctx, page, inspect.Options{})
}

func newInspectCmd(opts *globalOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "inspect <pull request number> [path]",
		Short: "Load a page of a sandbox and report what goes wrong on it",
		Long: `Load a page of a sandbox in a browser nobody sees, the way a reviewer's
would, and report what went wrong: JavaScript that threw, errors and
warnings the page logged, and requests that failed or were answered
with an error -- including the ones it makes after it has loaded.

Needs Chrome or Chromium; PIT_BROWSER names one when pit cannot find
it. What pit loads here does not count as looked at in pit what.`,
		Example: `  pit inspect 482
  pit inspect 482 /orders/1001`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(c *cobra.Command, args []string) error {
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

			from := time.Now()
			report, err := capture(c.Context(), page)
			if rerr := recordBrowsing(m, box, state.Span{From: from, To: time.Now()}); err == nil {
				err = rerr
			}
			if err != nil {
				return err
			}

			out := ui.New(c.OutOrStdout(), c.ErrOrStderr())
			if opts.jsonOutput {
				enc := json.NewEncoder(out.Out())
				enc.SetIndent("", "  ")
				return enc.Encode(report)
			}
			writeInspection(out, report)
			return out.Err()
		},
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
