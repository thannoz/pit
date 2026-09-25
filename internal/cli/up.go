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
	"github.com/thannoz/pit/internal/snapshot"
	"github.com/thannoz/pit/internal/state"
	"github.com/thannoz/pit/internal/ui"
	"github.com/thannoz/pit/internal/workspace"
)

type upOptions struct {
	open bool
	// scenario is the data state to load. Empty means the one the
	// repository configured as its default.
	scenario string
	// snapshot is a saved state to load instead.
	snapshot string
}

// runUp builds the sandbox for a pull request. It is what `pit 482`
// does; the root command dispatches here when its argument is a number.
func runUp(c *cobra.Command, o *upOptions, arg string) error {
	pr, err := strconv.Atoi(arg)
	if err != nil || pr <= 0 {
		return errs.New("%q is not a pull request number", arg).
			WithHint("run `pit --help` to see the available commands")
	}

	if o.scenario != "" && o.snapshot != "" {
		return errs.New("--scenario and --snapshot both say where the data comes from").
			WithHint("pick one: a scenario the repository describes, or a snapshot someone saved")
	}

	ctx := c.Context()
	out := ui.New(c.OutOrStdout(), c.ErrOrStderr())

	repo, err := currentRepo(ctx)
	if err != nil {
		return err
	}
	// Looked up before anything else happens: a mistyped ID is cheaper
	// to hear about now than after a build.
	m, err := manager()
	if err != nil {
		return err
	}
	var snap *snapshot.Snapshot
	if o.snapshot != "" {
		found, err := m.Snapshots(state.Sandbox{RepoRef: repo.Identity.Ref()}).Find(o.snapshot)
		if err != nil {
			return err
		}
		snap = &found
	}
	cfg, _, err := config.LoadFrom(repo.Root)
	if err != nil {
		return err
	}

	// Everything that can be checked before anything is created gets
	// checked first: finding out that gh is missing after a worktree
	// exists is a worse experience than finding out now.
	f, err := forge.For(forge.Options{
		Host:     repo.Identity.Host,
		Repo:     repo.Identity.Owner + "/" + repo.Identity.Name,
		Runner:   proc.Exec{},
		Resolver: workspace.PullRequests{Runner: proc.Exec{}, Repo: repo},
		Dir:      repo.Root,
	})
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
	warnIfNotWorthReviewing(out, pull, repo.Identity.Host)

	rep := newStepReporter(out, c.ErrOrStderr())
	record, err := m.Up(ctx, sandbox.UpRequest{
		Repo:     repo,
		PR:       pull,
		Config:   cfg,
		Scenario: o.scenario,
		Snapshot: snap,
		Confirm:  func(question string) bool { return ask(c, out, question) },
		OfferSave: func(box state.Sandbox) error {
			return saveBeforeReplacing(c, m, out, box, true)
		},
	}, rep)
	if err != nil {
		return err
	}

	// The URL is the answer, so it goes to stdout on its own line. The
	// narration went to stderr, which is what makes `pit 482` usable
	// in a pipe.
	rep.Blank()
	out.Println(record.URL)
	if o.open {
		openInBrowser(ctx, out, record.URL)
	}
	return nil
}

// warnIfNotWorthReviewing says so when the pull request is already
// merged, closed or still a draft. It is not an error -- there are good
// reasons to look at one -- but it is almost always a mistyped number.
func warnIfNotWorthReviewing(out *ui.Printer, pull forge.PR, host string) {
	if pull.Limited {
		// Saying nothing would let "open" be read as a fact, when all
		// pit knows is that the ref exists.
		out.Warnf("%s; the description above is the commit's own", cannotRead(host))
		return
	}

	switch {
	case pull.State == forge.Merged:
		out.Warnf("#%d is already merged", pull.Number)
	case pull.State == forge.Closed:
		out.Warnf("#%d is closed", pull.Number)
	case pull.Draft:
		out.Warnf("#%d is still a draft", pull.Number)
	}
}

// cannotRead phrases why the details are missing. "local" is pit's own
// marker for a filesystem remote, not the name of a service, and
// reading it as one produces a sentence about a place that does not
// exist.
func cannotRead(host string) string {
	if host == forge.LocalHost || host == "" {
		return "this repository has no hosting service to ask for pull request details"
	}
	return "pit cannot read pull request details from " + host
}

// stepReporter renders the stages of a setup as they happen.
//
// A sandbox's own output goes to stderr, not stdout: the answer of
// `pit 482` is the URL, and a build log on stdout would ruin
// `pit 482 | read url`.
type stepReporter struct {
	progress *ui.Progress
	out      *ui.Printer
	err      io.Writer
}

func newStepReporter(out *ui.Printer, err io.Writer) *stepReporter {
	return &stepReporter{progress: ui.NewProgress(err), out: out, err: err}
}

func (r *stepReporter) Begin(name string, streams bool) { r.progress.Begin(name, streams) }
func (r *stepReporter) Done(format string, args ...any) { r.progress.Done(format, args...) }

// Note goes to stderr like the steps do, and without a prefix: it is
// part of the narration, not a warning about it.
func (r *stepReporter) Note(format string, args ...any) { r.out.Notef(format, args...) }
func (r *stepReporter) Blank()                          { r.progress.Blank() }
func (r *stepReporter) Stdout() io.Writer               { return r.err }
func (r *stepReporter) Stderr() io.Writer               { return r.err }

var _ sandbox.Reporter = (*stepReporter)(nil)
