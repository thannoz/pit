package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/thannoz/pit/internal/analysis"
	"github.com/thannoz/pit/internal/proc"
	"github.com/thannoz/pit/internal/review"
	"github.com/thannoz/pit/internal/state"
	"github.com/thannoz/pit/internal/ui"
)

func newWhatCmd(opts *globalOptions) *cobra.Command {
	var done, undone []int
	cmd := &cobra.Command{
		Use:   "what <pull request number>",
		Short: "List what to look at in a sandbox",
		Long: `Print the addresses a pull request's changes lead to, as links into
its running sandbox, and what clicking through them will not show:
migrations, removed endpoints, permission checks, error handling.

Addresses pit is sure of come first. The others say why it is not.

--done marks addresses as looked at, by their number on the list, and
--undone takes the mark back. The marks are kept with the sandbox. When
the pull request gets a new commit, an address stays looked at unless a
file that leads to it has changed; then it asks to be looked at again.`,
		Example: `  pit what 482
  pit what 482 --done 1,3
  pit what 482 --undone 3`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			box, err := sandboxFor(c, args[0])
			if err != nil {
				return err
			}
			in, err := review.Load(c.Context(), proc.Exec{}, box)
			if err != nil {
				return err
			}
			list, err := review.Build(c.Context(), in)
			if err != nil {
				return err
			}
			if len(done) > 0 || len(undone) > 0 {
				m, err := manager()
				if err != nil {
					return err
				}
				if box, err = review.Record(m.Store, box, list, undone, false); err != nil {
					return err
				}
				if box, err = review.Record(m.Store, box, list, done, true); err != nil {
					return err
				}
			}
			review.Progress(c.Context(), proc.Exec{}, box.RepoRoot, box.Checked, &list)

			out := ui.New(c.OutOrStdout(), c.ErrOrStderr())
			if opts.jsonOutput {
				return writeWhatJSON(out.Out(), box, list)
			}
			if !answers(c.Context(), box.Port) {
				out.Notef("#%d does not answer at %s right now; `pit %d` starts it again", box.PR, box.URL, box.PR)
			}
			writeWhat(out, box, list)
			return out.Err()
		},
	}
	cmd.Flags().IntSliceVar(&done, "done", nil, "mark these numbers as looked at")
	cmd.Flags().IntSliceVar(&undone, "undone", nil, "take the mark back from these numbers")
	return cmd
}

// answers reports whether something listens on the sandbox's port. A
// link into a sandbox that is not running leads nowhere, and saying so
// before the reviewer clicks is cheaper than after.
func answers(ctx context.Context, port int) bool {
	d := net.Dialer{Timeout: 300 * time.Millisecond}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func writeWhat(out *ui.Printer, box state.Sandbox, list review.Checklist) {
	title := fmt.Sprintf("#%d", box.PR)
	if box.Title != "" {
		title += " " + box.Title
	}
	out.Println(title)
	var context []string
	context = append(context, box.URL)
	into := box.BaseBranch
	if into == "" {
		into = "the default branch"
	}
	context = append(context, fmt.Sprintf("%s into %s", short(list.Head), into))
	if list.Scenario != "" {
		context = append(context, "scenario "+list.Scenario)
	}
	out.Println(strings.Join(context, " · "))
	out.Println()

	if len(list.Items) == 0 {
		out.Println("Nothing in this pull request leads to an address pit knows.")
	} else {
		out.Printf("Affected by this pull request (%d of %d looked at):\n", list.Looked(), len(list.Items))
	}
	width := len(strconv.Itoa(len(list.Items)))
	for _, item := range list.Items {
		writeItem(out, item, width, list.Scenario)
	}
	for _, w := range list.Warnings {
		mark := "! "
		if w.Serious {
			mark = "!!"
		}
		out.Printf("  %s %s\n", mark, w.Message)
	}

	if len(list.Unplaced) > 0 {
		warned := map[string]bool{}
		for _, w := range list.Warnings {
			warned[w.File] = true
		}
		out.Println()
		out.Println("No address found for these; look at them yourself:")
		for _, f := range list.Unplaced {
			note := string(f.Kind)
			if warned[f.Path] {
				note += ", and see the warning above"
			}
			out.Printf("  %s  (%s)\n", f.Path, note)
		}
	}
}

func writeItem(out *ui.Printer, item review.Item, width int, scenario string) {
	address := item.URL
	if address == "" {
		address = item.Path
	}
	// A page is opened with GET; saying so is noise. Anything else is
	// worth the word.
	plainPage := len(item.Methods) == 1 && item.Methods[0] == "GET" && item.Kind == analysis.Page
	if len(item.Methods) > 0 && !plainPage {
		address = strings.Join(item.Methods, ",") + " " + address
	}

	mark := " "
	switch item.Mark {
	case review.Looked:
		mark = "✓"
	case review.Again:
		mark = "↻"
	}
	line := fmt.Sprintf("  %s %*d. %s  (%s)", mark, width, item.Number, address, files(item.Files))
	if item.Confidence != analysis.Certain {
		line += "  " + item.Confidence.String()
	}
	out.Println(line)

	indent := strings.Repeat(" ", width+8)
	if item.Mark == review.Again {
		out.Printf("%slooked at in %s; %s changed since, so look again\n", indent, short(item.CheckedAt), files(item.ChangedSince))
	}
	if len(item.Missing) > 0 {
		where := "data.scenarios[].params"
		if scenario != "" {
			where = fmt.Sprintf("data.scenarios[%s].params", scenario)
		}
		out.Printf("%sno link: %s needs a value; set it in %s\n", indent, strings.Join(item.Missing, ", "), where)
	}
	const shown = 2
	for i, d := range item.Doubts {
		if i == shown {
			out.Printf("%s? and %d more\n", indent, len(item.Doubts)-shown)
			break
		}
		out.Printf("%s? %s\n", indent, d)
	}
}

// files names the changed files an address comes from, short: the file
// name, unless two share one.
func files(paths []string) string {
	const shown = 3
	names := make([]string, 0, len(paths))
	seen := map[string]int{}
	for _, p := range paths {
		seen[path.Base(p)]++
	}
	for _, p := range paths {
		if seen[path.Base(p)] > 1 {
			names = append(names, p)
		} else {
			names = append(names, path.Base(p))
		}
	}
	if len(names) > shown {
		return strings.Join(names[:shown], ", ") + fmt.Sprintf(" and %d more", len(names)-shown)
	}
	return strings.Join(names, ", ")
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// The shape `--json` promises, apart from the internal types so that a
// renamed field inside pit does not break someone's script.
type (
	whatJSON struct {
		PR       int           `json:"pr"`
		URL      string        `json:"url"`
		Base     string        `json:"base"`
		Head     string        `json:"head"`
		Scenario string        `json:"scenario,omitempty"`
		Items    []whatItem    `json:"items"`
		Warnings []whatWarning `json:"warnings"`
		Unplaced []string      `json:"unplaced"`
	}
	whatItem struct {
		Number     int        `json:"number"`
		Kind       string     `json:"kind"`
		Methods    []string   `json:"methods,omitempty"`
		Path       string     `json:"path"`
		URL        string     `json:"url,omitempty"`
		Missing    []string   `json:"missing,omitempty"`
		Files      []string   `json:"files"`
		Via        [][]string `json:"via,omitempty"`
		Confidence string     `json:"confidence"`
		Doubts     []string   `json:"doubts,omitempty"`
		// Progress is "open", "looked" or "again".
		Progress     string   `json:"progress"`
		CheckedAt    string   `json:"checkedAt,omitempty"`
		ChangedSince []string `json:"changedSince,omitempty"`
	}
	whatWarning struct {
		Kind    string `json:"kind"`
		Serious bool   `json:"serious"`
		File    string `json:"file"`
		Address string `json:"address,omitempty"`
		Lines   []int  `json:"lines,omitempty"`
		Message string `json:"message"`
	}
)

func writeWhatJSON(w io.Writer, box state.Sandbox, list review.Checklist) error {
	doc := whatJSON{
		PR: box.PR, URL: list.URL, Base: list.Base, Head: list.Head, Scenario: list.Scenario,
		Items: []whatItem{}, Warnings: []whatWarning{}, Unplaced: []string{},
	}
	for _, it := range list.Items {
		var via [][]string
		for _, t := range it.Via {
			via = append(via, []string(t))
		}
		doc.Items = append(doc.Items, whatItem{
			Number: it.Number, Kind: string(it.Kind), Methods: it.Methods, Path: it.Path, URL: it.URL,
			Missing: it.Missing, Files: it.Files, Via: via, Confidence: it.Confidence.String(), Doubts: it.Doubts,
			Progress: []string{"open", "looked", "again"}[it.Mark], CheckedAt: it.CheckedAt, ChangedSince: it.ChangedSince,
		})
	}
	for _, x := range list.Warnings {
		doc.Warnings = append(doc.Warnings, whatWarning{
			Kind: string(x.Kind), Serious: x.Serious, File: x.File, Address: x.Address, Lines: x.Lines, Message: x.Message,
		})
	}
	for _, f := range list.Unplaced {
		doc.Unplaced = append(doc.Unplaced, f.Path)
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(doc)
}
