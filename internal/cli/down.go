package cli

import (
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/sandbox"
	"github.com/thannoz/pit/internal/state"
	"github.com/thannoz/pit/internal/ui"
)

type downOptions struct {
	all  bool
	gone bool
	yes  bool
}

func newDownCmd(_ *globalOptions) *cobra.Command {
	o := &downOptions{}

	cmd := &cobra.Command{
		Use:   "down [pull request number]",
		Short: "Remove a sandbox and everything it created",
		Long: `Stop a sandbox's services and remove its containers, networks, volumes,
worktree and generated files, leaving nothing behind.

When the sandbox's data was changed since it was loaded -- something
entered in the browser -- pit offers to save it as a snapshot first.

Without a number, pass --all to remove every sandbox.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return runDown(c, o, args)
		},
	}

	f := cmd.Flags()
	f.BoolVar(&o.all, "all", false, "remove every sandbox, from every repository")
	f.BoolVar(&o.gone, "gone", false, "remove only the sandboxes whose containers no longer exist")
	f.BoolVarP(&o.yes, "yes", "y", false, "do not ask for confirmation, nor whether to save changed data")

	return cmd
}

func runDown(c *cobra.Command, o *downOptions, args []string) error {
	switch {
	case o.all && o.gone:
		return errs.New("--all and --gone ask for different things").
			WithHint("--all removes every sandbox; --gone removes only the ones already stopped")
	case (o.all || o.gone) && len(args) > 0:
		return errs.New("a pull request number and a bulk flag ask for different things").
			WithHint("use either `pit down %s` or one of --all and --gone", args[0])
	case !o.all && !o.gone && len(args) == 0:
		return errs.New("which sandbox should be removed?").
			WithHint("pass a pull request number, --all, or --gone")
	}

	m, err := manager()
	if err != nil {
		return err
	}
	out := ui.New(c.OutOrStdout(), c.ErrOrStderr())

	if o.all || o.gone {
		return downMany(c, m, out, o)
	}
	return downOne(c, m, out, o, args[0])
}

func downOne(c *cobra.Command, m *sandbox.Manager, out *ui.Printer, o *downOptions, arg string) error {
	box, err := sandboxFor(c, arg)
	if err != nil {
		return err
	}
	return removeOne(c, m, out, box, !o.yes)
}

func downMany(c *cobra.Command, m *sandbox.Manager, out *ui.Printer, o *downOptions) error {
	targets, err := bulkTargets(c, m, o)
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		out.Println("No sandboxes to remove.")
		return nil
	}

	if !o.yes {
		out.Printf("This removes %s, with %s containers, volumes and worktrees:\n",
			plural(len(targets), "sandbox", "sandboxes"),
			pick(len(targets), "its", "their"))
		edited := editedNow(c, m)
		for _, box := range targets {
			line := box.Describe()
			if edited[box.Key()] {
				line += "  (its data was changed since it was loaded)"
			}
			out.Printf("  %s\n", line)
		}
		if !confirm(c, out, "Remove "+pick(len(targets), "it", "them all")+"?") {
			out.Println("Nothing was removed.")
			return nil
		}
	}

	var failed int
	for _, box := range targets {
		if err := removeOne(c, m, out, box, !o.yes); err != nil {
			out.Error(err)
			failed++
		}
	}
	if failed > 0 {
		return errs.New("%d of %d sandboxes could not be removed", failed, len(targets))
	}
	return nil
}

// bulkTargets is everything --all or --gone applies to. --gone has to
// ask the runtime, because only it knows which containers are still
// there.
func bulkTargets(c *cobra.Command, m *sandbox.Manager, o *downOptions) ([]state.Sandbox, error) {
	if o.all {
		f, err := m.Store.Load()
		if err != nil {
			return nil, err
		}
		return f.Sandboxes, nil
	}

	entries, err := m.List(c.Context())
	if err != nil {
		return nil, err
	}
	var targets []state.Sandbox
	for _, e := range sandbox.Stale(entries) {
		targets = append(targets, e.Sandbox)
	}
	return targets, nil
}

// editedNow is which sandboxes pit ls would show as edited, for the
// list a bulk removal asks about. Nothing is stopped for it: the
// reviewer may still say no.
func editedNow(c *cobra.Command, m *sandbox.Manager) map[string]bool {
	entries, err := m.List(c.Context())
	if err != nil {
		return nil
	}
	out := map[string]bool{}
	for _, e := range entries {
		out[e.Key()] = e.Edited == sandbox.Edited
	}
	return out
}

func removeOne(c *cobra.Command, m *sandbox.Manager, out *ui.Printer, box state.Sandbox, offer bool) error {
	if err := keepEdits(c, m, out, box, offer); err != nil {
		return err
	}
	if err := m.Down(c.Context(), box, c.ErrOrStderr(), c.ErrOrStderr()); err != nil {
		return err
	}
	out.Printf("Removed %s\n", box.Describe())
	return nil
}

// keepEdits offers to save a sandbox's data before it goes, when it was
// changed since it was loaded: then it is something no scenario and no
// snapshot can bring back, and removing the sandbox removes it for
// good. Data that is still what was loaded is not asked about; it can
// be loaded again.
//
// Saving failing stops the removal. The reviewer asked to keep it.
func keepEdits(c *cobra.Command, m *sandbox.Manager, out *ui.Printer, box state.Sandbox, offer bool) error {
	edit, running := m.EditedBeforeDown(c.Context(), box)
	if edit != sandbox.Edited {
		return nil
	}
	if !running {
		out.Printf("#%d's data was changed since it was loaded, and goes with the sandbox.\n", box.PR)
		out.Printf("It is not running, so pit cannot save it; `pit %d` starts it again.\n", box.PR)
		return nil
	}
	if err := offerSave(c, m, out, box, offer, "goes with the sandbox"); err != nil {
		return errs.Wrap(err, "saving #%d's data failed, so it was not removed", box.PR).
			WithHint("`pit down %d --yes` removes it without saving", box.PR)
	}
	return nil
}

// offerSave says that a sandbox's changed data is about to be lost, and
// offers to save it as a snapshot. With no offer to make -- --yes, or
// nobody to answer -- it says so and how to keep it next time. The
// error is saving's; the caller decides what not to do then.
func offerSave(c *cobra.Command, m *sandbox.Manager, out *ui.Printer, box state.Sandbox, offer bool, fate string) error {
	out.Printf("#%d's data was changed since it was loaded, and %s.\n", box.PR, fate)
	if !offer || !interactive(c) {
		out.Printf("It was not saved; `pit snap save %d` beforehand keeps it.\n", box.PR)
		return nil
	}
	if !askYes(c, out, "Save it as a snapshot first?") {
		return nil
	}
	snap, _, err := m.SaveSnapshot(c.Context(), box, "", false, c.ErrOrStderr())
	if err != nil {
		return err
	}
	out.Printf("Saved it as %s: `pit snap restore <n> %s` loads it into a sandbox, `pit snap promote %s` makes it a scenario.\n",
		snap.ID, snap.ID, snap.ID)
	return nil
}

// saveBeforeReplacing offers to save a running sandbox's changed data
// before it is replaced, and stops the replacing when saving fails.
func saveBeforeReplacing(c *cobra.Command, m *sandbox.Manager, out *ui.Printer, box state.Sandbox, offer bool) error {
	if err := offerSave(c, m, out, box, offer, "is about to be replaced"); err != nil {
		return errs.Wrap(err, "saving #%d's data failed, so it was left as it is", box.PR).
			WithHint("`pit snap save %d` says what goes wrong", box.PR)
	}
	return nil
}

// askYes asks a question whose answer is yes unless someone says no.
func askYes(c *cobra.Command, out *ui.Printer, question string) bool {
	answer := strings.ToLower(prompt(c, out, question+" [Y/n]: "))
	return answer != "n" && answer != "no"
}

// confirm asks a yes-or-no question, refusing when there is nobody to
// ask rather than assuming consent.
func confirm(c *cobra.Command, out *ui.Printer, question string) bool {
	if !interactive(c) {
		out.Warnf("nothing to read an answer from; pass --yes to confirm")
		return false
	}
	return ask(c, out, question)
}

// ask is confirm without the warning, for questions that have a
// sensible answer when nobody is there: the ones where no means
// leaving something alone.
func ask(c *cobra.Command, out *ui.Printer, question string) bool {
	if !interactive(c) {
		return false
	}
	answer := prompt(c, out, question+" [y/N]: ")
	return answer == "y" || answer == "Y" || answer == "yes"
}

// pick chooses between a singular and a plural word.
func pick(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}
