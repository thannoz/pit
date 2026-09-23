package cli

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/state"
	"github.com/thannoz/pit/internal/ui"
)

func newTimingCmd(opts *globalOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "timing <pull request number>",
		Short: "Show where the time went while a sandbox was built",
		Long: `Print how long each part of a setup took.

Measured while the sandbox was built, not now: this is a record of
what happened, which is the only version of it that is true.`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return runTiming(c, opts, args[0])
		},
	}
}

func runTiming(c *cobra.Command, opts *globalOptions, arg string) error {
	box, err := sandboxFor(c, arg)
	if err != nil {
		return err
	}
	if len(box.Steps) == 0 {
		return errs.New("no timings were recorded for #%d", box.PR).
			WithHint("they are written while a sandbox is built; this one predates that")
	}

	out := ui.New(c.OutOrStdout(), c.ErrOrStderr())
	if opts.jsonOutput {
		return writeTimingJSON(out, box)
	}
	return writeTimingTable(out, box)
}

// timingRow is the shape `--json` promises. It is a type of its own
// rather than the internal one, so that renaming a field inside pit
// does not silently break someone's script.
type timingRow struct {
	Step   string `json:"step"`
	Millis int64  `json:"ms"`
}

func writeTimingJSON(out *ui.Printer, box state.Sandbox) error {
	rows := make([]timingRow, 0, len(box.Steps)+1)
	for _, s := range box.Steps {
		rows = append(rows, timingRow{Step: s.Name, Millis: s.Millis})
	}
	rows = append(rows, timingRow{Step: "total", Millis: box.SetupMillis})

	enc := json.NewEncoder(out.Out())
	enc.SetIndent("", "  ")
	return enc.Encode(rows)
}

func writeTimingTable(out *ui.Printer, box state.Sandbox) error {
	total := box.SetupTook()

	w := tabwriter.NewWriter(out.Out(), 0, 0, 2, ' ', 0)
	// tabwriter buffers, so these cannot fail in a way worth checking
	// here; a broken pipe or a full disk surfaces at Flush below.
	_, _ = fmt.Fprintln(w, "STEP\tTOOK\tSHARE")

	var counted time.Duration
	for _, s := range box.Steps {
		counted += s.Took()
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\n", s.Name, took(s.Took()), share(s.Took(), total))
	}

	// What is left is real time that no step claimed: reserving a
	// port, writing the override, recording the result. Hiding it
	// would make the table add up to a lie.
	if rest := total - counted; rest >= 100*time.Millisecond {
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\n", "(the rest)", took(rest), share(rest, total))
	}
	_, _ = fmt.Fprintf(w, "%s\t%s\t\n", "total", took(total))

	if err := w.Flush(); err != nil {
		return errs.Wrap(err, "cannot write the timings")
	}
	return nil
}

// took renders a duration at the resolution a person cares about: a
// build is interesting to a tenth of a second, a fetch to the
// millisecond, and neither to nine digits.
func took(d time.Duration) string {
	switch {
	case d >= time.Minute:
		return d.Round(time.Second).String()
	case d >= time.Second:
		return strconv.FormatFloat(d.Seconds(), 'f', 1, 64) + "s"
	default:
		return strconv.FormatInt(d.Milliseconds(), 10) + "ms"
	}
}

// share is the part of the whole a step took. It is what makes the
// table useful: the numbers say how long, the percentages say where to
// look.
func share(d, total time.Duration) string {
	if total <= 0 {
		return "-"
	}
	return strings.TrimSpace(fmt.Sprintf("%3.0f%%", 100*d.Seconds()/total.Seconds()))
}
