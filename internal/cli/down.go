package cli

import (
	"strconv"

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

Without a number, pass --all to remove every sandbox.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return runDown(c, o, args)
		},
	}

	f := cmd.Flags()
	f.BoolVar(&o.all, "all", false, "remove every sandbox, from every repository")
	f.BoolVar(&o.gone, "gone", false, "remove only the sandboxes whose containers no longer exist")
	f.BoolVarP(&o.yes, "yes", "y", false, "do not ask for confirmation")

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
	return downOne(c, m, out, args[0])
}

func downOne(c *cobra.Command, m *sandbox.Manager, out *ui.Printer, arg string) error {
	pr, err := strconv.Atoi(arg)
	if err != nil || pr <= 0 {
		return errs.New("%q is not a pull request number", arg)
	}

	// The number alone is ambiguous: the record is global, and the same
	// number exists in every repository. Which one is meant follows
	// from where the command was run.
	repo, err := currentRepo(c.Context())
	if err != nil {
		return err
	}

	f, err := m.Store.Load()
	if err != nil {
		return err
	}
	box, ok := f.Find(repo.Identity.Ref(), pr)
	if !ok {
		return errs.New("there is no sandbox for #%d in %s", pr, repo.Identity).
			WithHint("`pit ls` shows what exists")
	}

	return removeOne(c, m, out, box)
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
		for _, box := range targets {
			out.Printf("  %s #%d\n", box.Repo, box.PR)
		}
		if !confirm(c, out, "Remove "+pick(len(targets), "it", "them all")+"?") {
			out.Println("Nothing was removed.")
			return nil
		}
	}

	var failed int
	for _, box := range targets {
		if err := removeOne(c, m, out, box); err != nil {
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

func removeOne(c *cobra.Command, m *sandbox.Manager, out *ui.Printer, box state.Sandbox) error {
	if err := m.Down(c.Context(), box, c.ErrOrStderr(), c.ErrOrStderr()); err != nil {
		return err
	}
	out.Printf("Removed #%d (%s)\n", box.PR, box.Repo)
	return nil
}

// confirm asks a yes-or-no question, refusing when there is nobody to
// ask rather than assuming consent.
func confirm(c *cobra.Command, out *ui.Printer, question string) bool {
	if !interactive(c) {
		out.Warnf("nothing to read an answer from; pass --yes to confirm")
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
