package cli

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/sandbox"
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
	cmd.AddCommand(newSnapSaveCmd(opts), newSnapRestoreCmd(opts), newSnapLsCmd(opts), newSnapRmCmd(opts), newSnapPromoteCmd(opts))
	return cmd
}

func newSnapSaveCmd(opts *globalOptions) *cobra.Command {
	var consistent bool
	cmd := &cobra.Command{
		Use:   "save <pull request number> [name]",
		Short: "Save the data a sandbox is in",
		Long: `Run data.snapshot.save in a running sandbox and keep what it writes,
compressed, where pit keeps its state.

The snapshot gets an ID, like sn_7f3a1b, and the name if you give one.
The ID is printed on stdout, so a script can hold on to it.

A project with several databases lists commands for each under
data.snapshot, and a snapshot then holds all of them. They are saved one
after the other, and an application writing meanwhile can leave them
describing different moments. --consistent pauses every other service
while they are saved, so that nothing writes in between, and lets them
go on afterwards.`,
		Example: `  pit snap save 482
  pit snap save 482 cart-with-voucher
  pit snap save 482 two-warehouses --consistent`,
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
			snap, paused, err := m.SaveSnapshot(c.Context(), box, name, consistent, c.ErrOrStderr())
			if err != nil {
				return err
			}
			detail := fmt.Sprintf("%s  %s, %s", snap.Label(), ui.Size(snap.Size), ui.Took(snap.Took))
			if services := snapServices(snap); len(services) > 1 {
				detail += "; " + strings.Join(services, ", ")
			}
			if len(paused) > 0 {
				detail += "; " + strings.Join(paused, ", ") + " paused meanwhile"
			}
			progress.Finish("%s", detail)

			if opts.jsonOutput {
				return writeSnapJSON(out, snap)
			}
			out.Println(snap.ID)
			return out.Err()
		},
	}
	cmd.Flags().BoolVar(&consistent, "consistent", false, "pause every other service while the databases are saved")
	return cmd
}

// snapServices are the services a snapshot holds, where it names them.
func snapServices(s snapshot.Snapshot) []string {
	var out []string
	for _, p := range s.Pieces() {
		if p.Service != "" {
			out = append(out, p.Service)
		}
	}
	return out
}

func newSnapRestoreCmd(_ *globalOptions) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "restore <pull request number> <snapshot>",
		Short: "Put a sandbox's data back into a saved state",
		Long: `Feed a snapshot to data.snapshot.restore in a running sandbox. The
snapshot is named by its ID or its name, and may come from the sandbox
of another pull request of the same repository.

Whatever the sandbox's data is now is replaced, so pit asks first.
A snapshot taken at another commit has that commit's schema; the
migrations of this one run after it.`,
		Example: `  pit snap restore 482 cart-with-voucher
  pit snap restore 519 sn_7f3a1b --yes`,
		Args: cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			box, err := sandboxFor(c, args[0])
			if err != nil {
				return err
			}
			m, err := manager()
			if err != nil {
				return err
			}
			snap, err := m.Snapshots(box).Find(args[1])
			if err != nil {
				return err
			}
			out := ui.New(c.OutOrStdout(), c.ErrOrStderr())

			entry, err := m.Find(c.Context(), box.RepoRef, box.PR)
			if err != nil {
				return err
			}
			if entry.Unreachable != nil {
				return errs.Wrap(entry.Unreachable, "cannot tell whether #%d is running", box.PR)
			}
			if !entry.AnyRunning() {
				return errs.New("nothing is running for #%d to restore into", box.PR).
					WithHint("`pit %d` brings the sandbox up again; `pit ls` shows what exists", box.PR)
			}

			if !yes {
				from := fmt.Sprintf("#%d at %s", snap.PR, short(snap.SHA))
				out.Printf("This replaces the data of #%d with %s, saved %s ago from %s. Anything entered since is lost.\n",
					box.PR, snap.Label(), shortDuration(time.Since(snap.CreatedAt)), from)
				if !confirm(c, out, "Restore it?") {
					out.Println("The data was left alone.")
					return nil
				}
			}

			if m.EditedNow(c.Context(), box) == sandbox.Edited {
				if err := saveBeforeReplacing(c, m, out, box, !yes); err != nil {
					return err
				}
			}
			rep := newStepReporter(out, c.ErrOrStderr())
			return m.RestoreSnapshot(c.Context(), box, snap, rep)
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	return cmd
}

// snapJSON is the shape `--json` promises, apart from the internal
// type so that a renamed field inside pit does not break a script.
type snapJSON struct {
	ID        string `json:"id"`
	Name      string `json:"name,omitempty"`
	Repo      string `json:"repo,omitempty"`
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
	return enc.Encode(toSnapJSON(s))
}
