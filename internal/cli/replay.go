package cli

import (
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/inspect"
	"github.com/thannoz/pit/internal/report"
	"github.com/thannoz/pit/internal/state"
	"github.com/thannoz/pit/internal/ui"
)

// replayBrowser does a recording's steps again; a variable so that
// tests need no Chrome.
var replayBrowser = inspect.Replay

func newReplayCmd(opts *globalOptions) *cobra.Command {
	var note int
	var password string
	var yes bool
	cmd := &cobra.Command{
		Use:   "replay <pull request number> <recipe>",
		Short: "Do again what a reviewer recorded, from the scenario they started on",
		Long: `Take a recipe -- a file with the JSON block from a comment pit wrote,
the whole comment, or - for standard input -- and do again what the
reviewer did: the scenario they started from is loaded, and their steps
are taken in a browser nobody sees. The sandbox is then where theirs
was when they noted what they found.

Loading the scenario replaces the data, so pit asks first; --yes does
not ask. A recording keeps no passwords; --password gives the one to
type where the reviewer typed one.`,
		Example: `  pit replay 482 recipe.json
  pbpaste | pit replay 482 - --note 2`,
		Args: cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			recipe, err := readRecipe(c, args[1], note)
			if err != nil {
				return err
			}
			box, err := sandboxFor(c, args[0])
			if err != nil {
				return err
			}
			if recipe.PR != box.PR {
				return errs.New("the recipe is for #%d, not #%d", recipe.PR, box.PR).
					WithHint("`pit replay %d …` replays it where it was recorded", recipe.PR)
			}
			m, err := manager()
			if err != nil {
				return err
			}
			if err := needsRunning(c, m, box); err != nil {
				return err
			}

			out := ui.New(c.OutOrStdout(), c.ErrOrStderr())
			if recipe.SHA != "" && !strings.HasPrefix(box.SHA, recipe.SHA) {
				out.Notef("recorded at %s; #%d is at %s now, so the steps may not fit", short(recipe.SHA), box.PR, short(box.SHA))
			}
			if recipe.Scenario == "" {
				out.Notef("the recording did not start from a scenario; the steps are taken on the data as it is")
			} else {
				if !yes {
					out.Printf("This loads scenario %q on #%d again, then takes the %s recorded. Anything entered by hand since is lost.\n",
						recipe.Scenario, box.PR, plural(len(recipe.Steps), "step", "steps"))
					if !confirm(c, out, "Go on?") {
						out.Println("Nothing was changed.")
						return out.Err()
					}
				}
				if err := reloadScenario(c, m, out, box, recipe.Scenario, !yes); err != nil {
					return err
				}
			}

			from := time.Now()
			rep, err := replayBrowser(c.Context(), box.URL, recipe.Steps, inspect.ReplayOptions{
				Password: password,
				OnStep: func(n int, s inspect.Step) {
					if !opts.jsonOutput {
						out.Printf("  %d. %s\n", n, s)
					}
				},
			})
			if rerr := recordBrowsing(m, box, state.Span{From: from, To: time.Now()}); err == nil {
				err = rerr
			}
			if err != nil {
				return err
			}
			if opts.jsonOutput {
				return writeJSON(out, struct {
					Note   int            `json:"note"`
					Steps  int            `json:"steps"`
					Report inspect.Report `json:"report"`
				}{recipe.Note, len(recipe.Steps), rep})
			}
			out.Printf("Replayed note %d: #%d is where the recording left it, at %s\n", recipe.Note, box.PR, rep.URL)
			if len(rep.Problems) > 0 || len(rep.Pending) > 0 {
				writeInspection(out, rep)
			}
			return out.Err()
		},
	}
	f := cmd.Flags()
	f.IntVar(&note, "note", 0, "the note whose recipe to take, when the text has several")
	f.StringVar(&password, "password", "", "the `password` to type where the reviewer typed one")
	f.BoolVarP(&yes, "yes", "y", false, "load the scenario without asking")
	return cmd
}

// readRecipe reads the recipe to replay from a file or standard input,
// picking the note's when there are several.
func readRecipe(c *cobra.Command, source string, note int) (report.Recipe, error) {
	var text []byte
	var err error
	if source == "-" {
		text, err = io.ReadAll(c.InOrStdin())
	} else {
		text, err = os.ReadFile(source)
	}
	if err != nil {
		return report.Recipe{}, errs.Wrap(err, "cannot read %s", source)
	}
	recipes, err := report.ParseRecipes(text)
	if err != nil {
		return report.Recipe{}, err
	}
	if note != 0 {
		for _, r := range recipes {
			if r.Note == note {
				return r, nil
			}
		}
		return report.Recipe{}, errs.New("there is no recipe for note %d in %s", note, source).
			WithHint("it has %s", recipeNotes(recipes))
	}
	if len(recipes) > 1 {
		return report.Recipe{}, errs.New("%s has a recipe for more than one note", source).
			WithHint("it has %s; --note picks one", recipeNotes(recipes))
	}
	return recipes[0], nil
}

func recipeNotes(recipes []report.Recipe) string {
	var ids []string
	for _, r := range recipes {
		ids = append(ids, strconv.Itoa(r.Note))
	}
	return "notes " + strings.Join(ids, ", ")
}
