package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/thannoz/pit/internal/doctor"
	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/proc"
	"github.com/thannoz/pit/internal/state"
	"github.com/thannoz/pit/internal/ui"
	"github.com/thannoz/pit/internal/workspace"
)

func newDoctorCmd(opts *globalOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check whether this machine can run pit",
		Long: `Check everything pit needs, and say what to do about whatever is missing.

Exits non-zero when something would stop pit working, so it can be used
in a setup script.`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return runDoctor(c, opts)
		},
	}
}

func runDoctor(c *cobra.Command, opts *globalOptions) error {
	env := doctor.Environment{Runner: proc.Exec{}, WorkDir: workDir()}

	// The state directory is itself one of the things under test, so a
	// failure to open it becomes a finding rather than an error.
	if dir, err := workspace.StateDir(); err == nil {
		env.StateDir = dir
		if store, err := state.Open(dir); err == nil {
			env.Store = store
		}
	}

	report := doctor.Run(c.Context(), doctor.Default(env))
	out := ui.New(c.OutOrStdout(), c.ErrOrStderr())

	if opts.jsonOutput {
		enc := json.NewEncoder(out.Out())
		enc.SetIndent("", "  ")
		if err := enc.Encode(report); err != nil {
			return err
		}
	} else if err := writeReport(out, report); err != nil {
		return err
	}

	if report.Failed() {
		_, _, fail := report.Counts()
		return errs.New("%s would stop pit working", plural(fail, "check", "checks")).
			WithHint("the lines marked ✗ above say what to do")
	}
	return nil
}

func writeReport(out *ui.Printer, report doctor.Report) error {
	w := tabwriter.NewWriter(out.Out(), 0, 0, 2, ' ', 0)
	for _, f := range report.Findings {
		// A detail can run to several lines -- a configuration report
		// does. The first shares the row; the rest are indented under
		// it, because a table cell cannot hold them.
		first, rest, _ := strings.Cut(f.Detail, "\n")

		// tabwriter buffers; failures surface at Flush.
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\n", mark(f.Result), f.Name, first)
		for _, line := range splitLines(rest) {
			_, _ = fmt.Fprintf(w, "\t\t%s\n", line)
		}
		if f.Fix != "" {
			_, _ = fmt.Fprintf(w, "\t\t%s\n", f.Fix)
		}
	}
	if err := w.Flush(); err != nil {
		return errs.Wrap(err, "cannot write the report")
	}

	ok, warn, fail := report.Counts()
	var parts []string
	parts = append(parts, fmt.Sprintf("%d ok", ok))
	if warn > 0 {
		parts = append(parts, fmt.Sprintf("%d to know about", warn))
	}
	if fail > 0 {
		parts = append(parts, fmt.Sprintf("%d to fix", fail))
	}
	out.Printf("\n%s\n", strings.Join(parts, ", "))
	return nil
}

// mark is the glyph in front of a finding. Colour would say it better,
// but these still read in a pipe and in a bug report.
func mark(r doctor.Result) string {
	switch r {
	case doctor.OK:
		return "✓"
	case doctor.Warn:
		return "!"
	default:
		return "✗"
	}
}

// splitLines returns the lines of s, and nothing at all for an empty
// string -- which strings.Split would report as one empty line.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func workDir() string {
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}
	return dir
}
