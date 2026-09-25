package cli

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/thannoz/pit/internal/config"
	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/sandbox"
	"github.com/thannoz/pit/internal/snapshot"
	"github.com/thannoz/pit/internal/state"
	"github.com/thannoz/pit/internal/ui"
)

func newSnapPromoteCmd(opts *globalOptions) *cobra.Command {
	var (
		o   snapshot.PromoteOptions
		yes bool
	)
	cmd := &cobra.Command{
		Use:   "promote <snapshot>",
		Short: "Turn a snapshot into a scenario of the repository",
		Long: `Write a snapshot into the repository, and add a scenario to .pit.yaml
that loads it. A snapshot stays on this machine; a scenario is
committed, and every reviewer can start from it.

The data goes to fixtures/<name>.sql, or with several databases one
file for each. The scenario loads it with data.snapshot's restore
command, then runs data.migrate. The rest of .pit.yaml is left exactly
as it is.

Promoting again under the same name replaces the files, after asking.
Commit what it wrote; until then, pit loads the scenario from your
.pit.yaml even for a pull request whose own file does not have it.`,
		Example: `  pit snap promote cart-with-voucher
  pit snap promote sn_7f3a1b --as=voucher-case --description="Cart with an expired voucher"`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			repo, err := currentRepo(c.Context())
			if err != nil {
				return err
			}
			path, err := config.Find(repo.Root)
			if err != nil {
				return err
			}
			m, err := manager()
			if err != nil {
				return err
			}
			store := m.Snapshots(state.Sandbox{RepoRef: repo.Identity.Ref()})
			snap, err := store.Find(args[0])
			if err != nil {
				return err
			}
			if o.Name == "" {
				o.Name = snap.Name
			}
			if o.Name == "" {
				return errs.New("%s has no name to give the scenario", snap.ID).
					WithHint("name it with --as: pit snap promote %s --as=<scenario>", snap.ID)
			}

			o.Params = sandboxParams(m, repo.Identity.Ref(), snap)
			plan, err := store.Plan(snap, path, o)
			if err != nil {
				return err
			}
			out := ui.New(c.OutOrStdout(), c.ErrOrStderr())
			if plan.Replacing && !yes {
				out.Printf("Scenario %q loads %s already. This replaces %s with %s, saved %s ago in #%d.\n",
					plan.Scenario.Name, strings.Join(plan.Files, ", "), pick(len(plan.Files), "it", "them"),
					snap.Label(), shortDuration(time.Since(snap.CreatedAt)), snap.PR)
				if !confirm(c, out, "Replace?") {
					out.Println("Nothing was written.")
					return nil
				}
			}

			size, err := plan.Write()
			if err != nil {
				return err
			}
			if opts.jsonOutput {
				return writePromoteJSON(out, plan, size)
			}
			return reportPromotion(out, plan, size, filepath.Base(path))
		},
	}
	cmd.Flags().StringVar(&o.Name, "as", "", "the scenario's name (default: the snapshot's)")
	cmd.Flags().StringVar(&o.Description, "description", "", "what `pit scenarios` says about it")
	cmd.Flags().StringVar(&o.Dir, "dir", snapshot.DefaultPromoteDir, "where the data goes, relative to .pit.yaml")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "replace the files of a promoted scenario without asking")
	return cmd
}

// sandboxParams are the example values of the scenario a snapshot was
// taken on, read from the configuration of its sandbox while that is
// still there: a scenario the pull request brought is in its file, not
// in the one the snapshot is promoted into.
func sandboxParams(m *sandbox.Manager, repoRef string, snap snapshot.Snapshot) map[string]string {
	if snap.Scenario == "" {
		return nil
	}
	f, err := m.Store.Load()
	if err != nil {
		return nil
	}
	box, ok := f.Find(repoRef, snap.PR)
	if !ok {
		return nil
	}
	cfg, _, err := config.LoadFrom(box.Worktree)
	if err != nil {
		return nil
	}
	params, err := cfg.Params(snap.Scenario)
	if err != nil {
		return nil
	}
	return params
}

func reportPromotion(out *ui.Printer, plan snapshot.Promotion, size int64, file string) error {
	for _, f := range plan.Files {
		out.Printf("Wrote %s", f)
		if len(plan.Files) == 1 {
			out.Printf(" (%s)", ui.Size(size))
		}
		out.Println(".")
	}
	name := plan.Scenario.Name
	switch {
	case plan.Replacing:
		out.Printf("Scenario %q loads %s now; %s is unchanged.\n", name, pick(len(plan.Files), "it", "them"), file)
	case plan.Manual != nil:
		out.Printf("\nAdd this under data.scenarios in %s:\n\n%s\n", file, config.ScenarioYAML(plan.Scenario))
		return errs.Hinted(plan.Manual, "%s %s written; with the lines above the scenario loads %s",
			strings.Join(plan.Files, " and "), pick(len(plan.Files), "is", "are"), pick(len(plan.Files), "it", "them"))
	default:
		out.Printf("Added scenario %q to %s.\n", name, file)
	}
	out.Printf("\nReview and commit them. `pit <pull request number> --scenario=%s` loads it,\n", name)
	out.Printf("and `pit data reset <pull request number> --scenario=%s` puts a running sandbox into it.\n", name)
	return out.Err()
}

type promoteJSON struct {
	Scenario string   `json:"scenario"`
	Files    []string `json:"files"`
	Size     int64    `json:"size"`
	Replaced bool     `json:"replaced"`
	Added    bool     `json:"added"`
}

func writePromoteJSON(out *ui.Printer, plan snapshot.Promotion, size int64) error {
	enc := json.NewEncoder(out.Out())
	enc.SetIndent("", "  ")
	if err := enc.Encode(promoteJSON{
		Scenario: plan.Scenario.Name, Files: plan.Files, Size: size,
		Replaced: plan.Replacing, Added: plan.Edited != nil,
	}); err != nil {
		return err
	}
	if plan.Manual != nil {
		return errs.Hinted(plan.Manual, "add this under data.scenarios:\n%s", config.ScenarioYAML(plan.Scenario))
	}
	return nil
}
