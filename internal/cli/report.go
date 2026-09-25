package cli

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/forge"
	"github.com/thannoz/pit/internal/notes"
	"github.com/thannoz/pit/internal/proc"
	"github.com/thannoz/pit/internal/report"
	"github.com/thannoz/pit/internal/ui"
)

// commenterFor finds what posts comments on a repository's pull
// requests; a variable so that tests post nowhere.
var commenterFor = func(ctx context.Context, repo string) (forge.Commenter, error) {
	host, name, _ := strings.Cut(repo, "/")
	f, err := forge.For(forge.Options{Host: host, Repo: name, Runner: proc.Exec{}})
	if err != nil {
		where := "on " + host
		if host == forge.LocalHost {
			where = "in a directory on this machine"
		}
		return nil, errs.New("pit can post comments only on GitHub, and this repository is %s", where).
			WithHint("the comment is above; paste it into the pull request yourself")
	}
	gh, ok := f.(forge.GitHub)
	if !ok {
		return nil, errs.New("pit cannot post comments on %s", host)
	}
	if err := gh.Check(ctx); err != nil {
		return nil, err
	}
	return gh, nil
}

// reportJSON is what pit report --json prints.
type reportJSON struct {
	PR     int    `json:"pr"`
	Body   string `json:"body"`
	Notes  []int  `json:"notes"`
	Posted bool   `json:"posted"`
	URL    string `json:"url,omitempty"`
}

func newReportCmd(opts *globalOptions) *cobra.Command {
	var only []int
	var dryRun, yes bool
	cmd := &cobra.Command{
		Use:   "report <pull request number>",
		Short: "Post what was noted as a comment on the pull request",
		Long: `Write a comment for the pull request from what pit note kept: each
note with its page and what went wrong there, and the command that
brings the pull request up with the same data, for its author to see
it too.

The comment is shown first, and posted only when you say yes to it.
Nothing is posted without that: not with --dry-run, not when there is
nobody to ask, and not with --json -- unless --yes says so beforehand.

Notes that went into a comment are not told again; --notes picks the
ones to tell, posted or not. Screenshots are not uploaded: pit names
them, for you to drag into the comment if they help.`,
		Example: `  pit report 482
  pit report 482 --notes 1,3
  pit report 482 --dry-run`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if err := notOnBase(c, "pit report"); err != nil {
				return err
			}
			if dryRun && yes {
				return errs.New("--dry-run and --yes contradict each other")
			}
			b, _, err := book(c, args[0])
			if err != nil {
				return err
			}
			all, err := b.List()
			if err != nil {
				return err
			}
			list, err := chosenNotes(all, only, b.PR)
			if err != nil {
				return err
			}
			in := report.Input{PR: b.PR, Notes: list}
			if box, err := sandboxFor(c, args[0]); err == nil {
				in.Head = box.SHA
			}
			body := report.Comment(in)
			result := reportJSON{PR: b.PR, Body: body, Notes: ids(list)}

			out := ui.New(c.OutOrStdout(), c.ErrOrStderr())
			if !opts.jsonOutput {
				writePreview(out, b, list, body)
			}
			if dryRun || (opts.jsonOutput && !yes) {
				if opts.jsonOutput {
					return writeJSON(out, result)
				}
				out.Printf("Not posted: --dry-run.\n")
				return out.Err()
			}

			commenter, err := commenterFor(c.Context(), b.Repo)
			if err != nil {
				return err
			}
			if !yes && !ask(c, out, "Post this comment on #"+strconv.Itoa(b.PR)+"?") {
				out.Printf("Not posted.\n")
				if !interactive(c) {
					out.Printf("  There was nobody to ask; `pit report %d --yes` posts it without asking.\n", b.PR)
				}
				return out.Err()
			}

			url, err := commenter.Comment(c.Context(), b.PR, body)
			if err != nil {
				return err
			}
			result.Posted, result.URL = true, url
			if err := b.MarkPosted(result.Notes, orElse(url, "#"+strconv.Itoa(b.PR))); err != nil {
				// The comment is there; failing now would invite posting
				// it again.
				out.Warnf("the comment is posted, but pit could not note that: %v", err)
			}
			if opts.jsonOutput {
				return writeJSON(out, result)
			}
			out.Printf("Posted: %s\n", orElse(url, "on #"+strconv.Itoa(b.PR)))
			return out.Err()
		},
	}
	f := cmd.Flags()
	f.IntSliceVar(&only, "notes", nil, "tell the notes with these `numbers`, posted before or not")
	f.BoolVar(&dryRun, "dry-run", false, "show the comment, and post nothing")
	f.BoolVarP(&yes, "yes", "y", false, "post without asking")
	return cmd
}

// writePreview shows the comment as it would be posted, and the
// screenshots that are not part of it.
func writePreview(out *ui.Printer, b notes.Book, list []notes.Note, body string) {
	rule := strings.Repeat("─", 60)
	out.Printf("This comment would go on #%d in %s:\n\n%s\n%s%s\n", b.PR, b.Repo, rule, body, rule)
	var files []string
	for _, n := range list {
		for _, f := range []string{n.Screenshot, n.GIF} {
			if f != "" {
				files = append(files, fmt.Sprintf("  note %d  %s", n.ID, f))
			}
		}
	}
	if len(files) > 0 {
		out.Printf("Screenshots and GIFs are not posted; drag them into the comment if they help:\n%s\n", strings.Join(files, "\n"))
	}
	out.Printf("\n")
}

// chosenNotes is the notes asked for; without --notes, the ones no
// comment has told yet.
func chosenNotes(list []notes.Note, only []int, pr int) ([]notes.Note, error) {
	if len(list) == 0 {
		return nil, errs.New("there are no notes on #%d to write a comment from", pr).
			WithHint(`pit note %d "what you found" --page <path> notes something`, pr)
	}
	if len(only) == 0 {
		fresh := slices.DeleteFunc(slices.Clone(list), func(n notes.Note) bool { return n.Posted != "" })
		if len(fresh) == 0 {
			return nil, errs.New("every note on #%d is in a comment already", pr).
				WithHint("`pit note %d` lists them; --notes tells some again", pr)
		}
		return fresh, nil
	}
	var unknown []string
	for _, id := range only {
		if !slices.ContainsFunc(list, func(n notes.Note) bool { return n.ID == id }) {
			unknown = append(unknown, strconv.Itoa(id))
		}
	}
	if len(unknown) > 0 {
		return nil, errs.New("#%d has no note %s", pr, strings.Join(unknown, ", ")).
			WithHint("`pit note %d` lists the notes there are", pr)
	}
	return slices.DeleteFunc(slices.Clone(list), func(n notes.Note) bool { return !slices.Contains(only, n.ID) }), nil
}

func ids(list []notes.Note) []int {
	out := make([]int, len(list))
	for i, n := range list {
		out[i] = n.ID
	}
	return out
}
