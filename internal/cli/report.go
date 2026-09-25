package cli

import (
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/notes"
	"github.com/thannoz/pit/internal/report"
	"github.com/thannoz/pit/internal/ui"
)

func newReportCmd(opts *globalOptions) *cobra.Command {
	var only []int
	cmd := &cobra.Command{
		Use:   "report <pull request number>",
		Short: "Write the comment for the pull request from what was noted",
		Long: `Write a comment for the pull request from what pit note kept: each
note with its page and what went wrong there, and the command that
brings the pull request up with the same data, for its author to see
it too.

The comment is printed, in Markdown.`,
		Example: `  pit report 482
  pit report 482 --notes 1,3`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			b, _, err := book(c, args[0])
			if err != nil {
				return err
			}
			list, err := b.List()
			if err != nil {
				return err
			}
			if len(list) == 0 {
				return errs.New("there are no notes on #%d to write a comment from", b.PR).
					WithHint(`pit note %d "what you found" --page <path> notes something`, b.PR)
			}
			list, err = chosenNotes(list, only, b.PR)
			if err != nil {
				return err
			}
			in := report.Input{PR: b.PR, Notes: list}
			if box, err := sandboxFor(c, args[0]); err == nil {
				in.Head = box.SHA
			}
			body := report.Comment(in)

			out := ui.New(c.OutOrStdout(), c.ErrOrStderr())
			if opts.jsonOutput {
				return writeJSON(out, struct {
					PR   int    `json:"pr"`
					Body string `json:"body"`
				}{b.PR, body})
			}
			out.Printf("%s", body)
			return out.Err()
		},
	}
	cmd.Flags().IntSliceVar(&only, "notes", nil, "only the notes with these `numbers`")
	return cmd
}

// chosenNotes is the notes asked for, or all of them.
func chosenNotes(list []notes.Note, only []int, pr int) ([]notes.Note, error) {
	if len(only) == 0 {
		return list, nil
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
