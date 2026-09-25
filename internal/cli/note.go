package cli

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/inspect"
	"github.com/thannoz/pit/internal/notes"
	"github.com/thannoz/pit/internal/sandbox"
	"github.com/thannoz/pit/internal/state"
	"github.com/thannoz/pit/internal/ui"
)

func newNoteCmd(opts *globalOptions) *cobra.Command {
	var page string
	var fullPage, noCapture bool
	var remove []int
	cmd := &cobra.Command{
		Use:   "note <pull request number> [text]",
		Short: "Note something found in a sandbox, or list what was noted",
		Long: `Note something found in a sandbox, with what it takes to see it again:
the page, the commit, the data it was on, and what went wrong on the
page when pit loaded it -- JavaScript that threw, failed requests --
with a screenshot.

Without text, lists the notes on the pull request. They are kept when
the sandbox is taken down, until they are removed.`,
		Example: `  pit note 482 "The refund total ignores the voucher" --page /orders/1001
  pit note 482
  pit note 482 --remove 2`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			text := strings.TrimSpace(strings.Join(args[1:], " "))
			out := ui.New(c.OutOrStdout(), c.ErrOrStderr())
			adding := c.Flags().Changed("page") || fullPage || noCapture
			switch {
			case len(remove) > 0 && (text != "" || adding):
				return errs.New("--remove takes notes away; it does not go with a new one")
			case text == "" && adding:
				return errs.New("--page, --full-page and --no-capture are about a new note, and there is no text").
					WithHint(`pit note %s "what you found" --page /orders/1001`, args[0])
			case len(remove) > 0:
				return removeNotes(c, out, args[0], remove, opts.jsonOutput)
			case text == "":
				return listNotes(c, out, args[0], opts.jsonOutput)
			}

			box, err := sandboxFor(c, args[0])
			if err != nil {
				return err
			}
			address, err := pageOf(box.URL, page)
			if err != nil {
				return err
			}
			m, err := manager()
			if err != nil {
				return err
			}
			n := notes.Note{
				Text: text, URL: address, SHA: box.SHA,
				Scenario: box.Scenario, Snapshot: box.Snapshot, At: time.Now(),
			}
			png, err := captureForNote(c, m, box, &n, noCapture, fullPage)
			if err != nil {
				return err
			}
			n, err = m.Notes(box.Repo, box.RepoRef, box.PR).Add(n, png)
			if err != nil {
				return err
			}
			if opts.jsonOutput {
				return writeJSON(out, n)
			}
			out.Printf("Noted on #%d as note %d.\n", box.PR, n.ID)
			writeNote(out, n, true)
			return out.Err()
		},
	}
	f := cmd.Flags()
	f.StringVar(&page, "page", "/", "the page the note is about, a `path` of the sandbox")
	f.BoolVar(&fullPage, "full-page", false, "picture the whole page, not only what the window shows")
	f.BoolVar(&noCapture, "no-capture", false, "note the text only, without loading the page")
	f.IntSliceVar(&remove, "remove", nil, "remove the notes with these `numbers`")
	return cmd
}

// captureForNote loads the page a note is about, and says in the note
// why it did not when it could not: the note is kept either way, and
// what the reviewer wrote matters more than the picture.
func captureForNote(c *cobra.Command, m *sandbox.Manager, box state.Sandbox, n *notes.Note, skip, fullPage bool) ([]byte, error) {
	entry, err := m.Find(c.Context(), box.RepoRef, box.PR)
	if err != nil {
		return nil, err
	}
	running := entry.AnyRunning()
	if running {
		n.Edited = m.EditedNow(c.Context(), box) == sandbox.Edited
	} else {
		n.Edited = box.Edited
	}
	switch {
	case skip:
		n.Uncaptured = "not asked to"
		return nil, nil
	case !running:
		n.Uncaptured = "the sandbox was not running"
		return nil, nil
	}

	shot := inspect.Window
	if fullPage {
		shot = inspect.FullPage
	}
	from := time.Now()
	report, err := capture(c.Context(), n.URL, shot)
	if rerr := recordBrowsing(m, box, state.Span{From: from, To: time.Now()}); rerr != nil {
		return nil, rerr
	}
	if err != nil {
		if c.Context().Err() != nil {
			return nil, err
		}
		n.Uncaptured = firstLine(err.Error())
		return nil, nil
	}
	n.Problems = append([]inspect.Problem{}, report.Problems...)
	n.Pending = report.Pending
	if report.Screenshot != nil {
		return report.Screenshot.PNG, nil
	}
	return nil, nil
}

// book finds the notes on a pull request: its sandbox's, or, when it
// has none any more, the repository's the command was run in.
func book(c *cobra.Command, arg string) (notes.Book, string, error) {
	m, err := manager()
	if err != nil {
		return notes.Book{}, "", err
	}
	box, err := sandboxFor(c, arg)
	if err == nil {
		return m.Notes(box.Repo, box.RepoRef, box.PR), box.Title, nil
	}
	ref, perr := state.ParseRef(arg)
	if perr != nil || ref.Repo != "" {
		return notes.Book{}, "", err
	}
	repo, rerr := currentRepo(c.Context())
	if rerr != nil {
		return notes.Book{}, "", err
	}
	return m.Notes(repo.Identity.String(), repo.Identity.Ref(), ref.PR), "", nil
}

func listNotes(c *cobra.Command, out *ui.Printer, arg string, asJSON bool) error {
	b, title, err := book(c, arg)
	if err != nil {
		return err
	}
	list, err := b.List()
	if err != nil {
		return err
	}
	if asJSON {
		return writeJSON(out, list)
	}
	if len(list) == 0 {
		out.Printf("No notes on #%d.\n", b.PR)
		out.Printf("  `pit note %d \"what you found\" --page <path>` notes something.\n", b.PR)
		return out.Err()
	}
	heading := fmt.Sprintf("%s on #%d", plural(len(list), "note", "notes"), b.PR)
	if title != "" {
		heading += fmt.Sprintf(" %q", title)
	}
	out.Printf("%s:\n", heading)
	for _, n := range list {
		out.Printf("\n")
		writeNote(out, n, false)
	}
	return out.Err()
}

func removeNotes(c *cobra.Command, out *ui.Printer, arg string, ids []int, asJSON bool) error {
	b, _, err := book(c, arg)
	if err != nil {
		return err
	}
	removed, err := b.Remove(ids...)
	if err != nil {
		return err
	}
	if asJSON {
		return writeJSON(out, removed)
	}
	for _, n := range removed {
		out.Printf("Removed note %d: %s\n", n.ID, n.Text)
	}
	return out.Err()
}

// maxListed is how many of a note's problems a listing shows; --json
// has them all.
const maxListed = 3

// writeNote prints a note: its text, where it was found, and what went
// wrong there. A note just taken shows all of it; a listing shows the
// first few problems of each, and how long ago it was taken.
func writeNote(out *ui.Printer, n notes.Note, fresh bool) {
	out.Printf("  %d  %s\n", n.ID, n.Text)
	data := orElse(n.Scenario, "no scenario")
	if n.Snapshot != "" {
		data = "snapshot " + n.Snapshot
	}
	if n.Edited {
		data += " +edited"
	}
	where := []string{pathOf(n.URL), short(n.SHA), data}
	if !fresh {
		where = append(where, shortDuration(time.Since(n.At))+" ago")
	}
	out.Printf("     %s\n", strings.Join(where, " · "))

	switch {
	case !n.Captured():
		out.Printf("     page not loaded: %s\n", n.Uncaptured)
	case len(n.Problems) == 0:
		out.Printf("     nothing went wrong on the page\n")
	}
	for i, p := range n.Problems {
		if !fresh && i == maxListed {
			out.Printf("     … %d more\n", len(n.Problems)-maxListed)
			break
		}
		mark := "✗"
		if p.Level == "warning" {
			mark = "!"
		}
		out.Printf("     %s %s\n", mark, describeProblem(p))
	}
	for _, p := range n.Pending {
		out.Printf("     … %s  still unanswered\n", p)
	}
	if n.Screenshot != "" {
		out.Printf("     screenshot %s\n", n.Screenshot)
	}
	if n.Posted != "" {
		out.Printf("     posted %s\n", n.Posted)
	}
}

// pathOf is the part of a sandbox's address that stays when its port
// changes.
func pathOf(address string) string {
	u, err := url.Parse(address)
	if err != nil || u.Host == "" {
		return address
	}
	return u.RequestURI()
}

func writeJSON(out *ui.Printer, v any) error {
	enc := json.NewEncoder(out.Out())
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}
