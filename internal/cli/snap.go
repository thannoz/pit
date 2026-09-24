package cli

import (
	"encoding/json"
	"time"

	"github.com/spf13/cobra"

	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/snapshot"
	"github.com/thannoz/pit/internal/ui"
)

func newSnapCmd(opts *globalOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "snap",
		Short: "Save the data a sandbox is in",
		Long: `Commands for snapshots: the state of a sandbox's database, frozen.

A scenario is a state the repository describes; a snapshot is one you
made by using the sandbox -- a cart with a voucher in it, an order
half-way through a refund. Saving it takes seconds, and it stays when
the sandbox goes.

Snapshots are made by the repository's own commands, data.snapshot in
.pit.yaml: one writes a dump to stdout, the other reads it back. pit
says which to add when there are none.`,
	}
	cmd.AddCommand(newSnapSaveCmd(opts))
	return cmd
}

func newSnapSaveCmd(opts *globalOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "save <pull request number> [name]",
		Short: "Save the data a sandbox is in",
		Long: `Run data.snapshot.save in a running sandbox and keep what it writes,
compressed, where pit keeps its state.

The snapshot gets an ID, like sn_7f3a1b, and the name if you give one.
The ID is printed on stdout, so a script can hold on to it.`,
		Example: `  pit snap save 482
  pit snap save 482 cart-with-voucher`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(c *cobra.Command, args []string) error {
			name := ""
			if len(args) == 2 {
				name = args[1]
			}
			box, err := sandboxFor(c, args[0])
			if err != nil {
				return err
			}
			m, err := manager()
			if err != nil {
				return err
			}
			out := ui.New(c.OutOrStdout(), c.ErrOrStderr())

			// A dump of containers that are not there fails inside a
			// compose command; saying so first is cheaper to read.
			entry, err := m.Find(c.Context(), box.RepoRef, box.PR)
			if err != nil {
				return err
			}
			if entry.Unreachable != nil {
				return errs.Wrap(entry.Unreachable, "cannot tell whether #%d is running", box.PR)
			}
			if !entry.AnyRunning() {
				return errs.New("nothing is running for #%d, so there is no data to save", box.PR).
					WithHint("`pit %d` brings the sandbox up again; `pit ls` shows what exists", box.PR)
			}

			progress := ui.NewProgress(c.ErrOrStderr())
			progress.Begin("snapshot", true)
			snap, err := m.SaveSnapshot(c.Context(), box, name, c.ErrOrStderr())
			if err != nil {
				return err
			}
			progress.Finish("%s  %s, %s", snap.Label(), ui.Size(snap.Size), ui.Took(snap.Took))

			if opts.jsonOutput {
				return writeSnapJSON(out, snap)
			}
			out.Println(snap.ID)
			return out.Err()
		},
	}
}

// snapJSON is the shape `--json` promises, apart from the internal
// type so that a renamed field inside pit does not break a script.
type snapJSON struct {
	ID        string `json:"id"`
	Name      string `json:"name,omitempty"`
	PR        int    `json:"pr"`
	SHA       string `json:"sha"`
	Scenario  string `json:"scenario,omitempty"`
	Service   string `json:"service,omitempty"`
	Size      int64  `json:"size"`
	Raw       int64  `json:"raw"`
	TookMS    int64  `json:"tookMs"`
	CreatedAt string `json:"createdAt"`
}

func writeSnapJSON(out *ui.Printer, s snapshot.Snapshot) error {
	enc := json.NewEncoder(out.Out())
	enc.SetIndent("", "  ")
	return enc.Encode(snapJSON{
		ID: s.ID, Name: s.Name, PR: s.PR, SHA: s.SHA, Scenario: s.Scenario, Service: s.Service,
		Size: s.Size, Raw: s.Raw, TookMS: s.Took.Milliseconds(), CreatedAt: s.CreatedAt.Format(time.RFC3339),
	})
}
