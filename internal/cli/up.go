package cli

import (
	"io"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/thannoz/pit/internal/config"
	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/forge"
	"github.com/thannoz/pit/internal/proc"
	"github.com/thannoz/pit/internal/sandbox"
	"github.com/thannoz/pit/internal/ui"
)

type upOptions struct {
	open bool
}

// runUp builds the sandbox for a pull request. It is what `pit 482`
// does; the root command dispatches here when its argument is a number.
func runUp(c *cobra.Command, o *upOptions, arg string) error {
	pr, err := strconv.Atoi(arg)
	if err != nil || pr <= 0 {
		return errs.New("%q is not a pull request number", arg).
			WithHint("run `pit --help` to see the available commands")
	}

	ctx := c.Context()
	out := ui.New(c.OutOrStdout(), c.ErrOrStderr())

	repo, err := currentRepo(ctx)
	if err != nil {
		return err
	}
	cfg, _, err := config.LoadFrom(repo.Root)
	if err != nil {
		return err
	}

	// Everything that can be checked before anything is created gets
	// checked first: finding out that gh is missing after a worktree
	// exists is a worse experience than finding out now.
	f, err := forge.For(repo.Identity.Host, repo.Identity.Owner+"/"+repo.Identity.Name, proc.Exec{})
	if err != nil {
		return err
	}
	if gh, ok := f.(forge.GitHub); ok {
		if err := gh.Check(ctx); err != nil {
			return err
		}
	}

	pull, err := f.PullRequest(ctx, pr)
	if err != nil {
		return err
	}
	out.Printf("%s\n", pull.Describe())
	warnIfNotWorthReviewing(out, pull)

	m, err := manager()
	if err != nil {
		return err
	}

	record, err := m.Up(ctx, sandbox.UpRequest{Repo: repo, PR: pull, Config: cfg}, &stepReporter{out: out, err: c.ErrOrStderr()})
	if err != nil {
		return err
	}

	out.Printf("\n  %s\n", record.URL)
	if o.open {
		openInBrowser(ctx, out, record.URL)
	}
	return nil
}

// warnIfNotWorthReviewing says so when the pull request is already
// merged, closed or still a draft. It is not an error -- there are good
// reasons to look at one -- but it is almost always a mistyped number.
func warnIfNotWorthReviewing(out *ui.Printer, pull forge.PR) {
	switch {
	case pull.State == forge.Merged:
		out.Warnf("#%d is already merged", pull.Number)
	case pull.State == forge.Closed:
		out.Warnf("#%d is closed", pull.Number)
	case pull.Draft:
		out.Warnf("#%d is still a draft", pull.Number)
	}
}

// stepReporter renders the stages of a setup as they complete.
type stepReporter struct {
	out *ui.Printer
	err io.Writer
}

func (r *stepReporter) Step(format string, args ...any) {
	r.out.Printf("  ✓ "+format+"\n", args...)
}

// Stdout of a sandbox's own commands goes to stderr, not stdout: the
// answer of `pit 482` is the URL, and a build log on stdout would ruin
// `pit 482 | read url`.
func (r *stepReporter) Stdout() io.Writer { return r.err }
func (r *stepReporter) Stderr() io.Writer { return r.err }

var _ sandbox.Reporter = (*stepReporter)(nil)
