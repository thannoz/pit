package report

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/inspect"
	"github.com/thannoz/pit/internal/notes"
)

// RecipeVersion is the shape of a recipe; a pit reading a newer one
// says so rather than guessing.
const RecipeVersion = 1

// Recipe is what pit replay does again: the data to start from, and
// the steps the reviewer took from there.
//
// It is the way to a state without a copy of the data: the scenario is
// in the repository already, and the steps are only what the reviewer
// did. What is in a comment is read by everyone who can read the pull
// request; a dump of a database should not be.
type Recipe struct {
	Kind     string         `json:"pit"`
	Version  int            `json:"version"`
	PR       int            `json:"pr"`
	Note     int            `json:"note"`
	SHA      string         `json:"sha"`
	Scenario string         `json:"scenario,omitempty"`
	Steps    []inspect.Step `json:"steps"`
}

const recipeKind = "recipe"

// RecipeOf is the recipe of a note that has a recording.
func RecipeOf(pr int, n notes.Note) (Recipe, bool) {
	if n.Recording == nil || len(n.Recording.Steps) == 0 {
		return Recipe{}, false
	}
	return Recipe{Kind: recipeKind, Version: RecipeVersion, PR: pr, Note: n.ID,
		SHA: n.Recording.SHA, Scenario: n.Recording.Scenario, Steps: n.Recording.Steps}, true
}

// jsonBlock finds the fenced JSON blocks of a text: a comment copied
// from GitHub, or a file of its own.
var jsonBlock = regexp.MustCompile("(?s)(`{3,})json[^\\n]*\\n(.*?)\\n\\s*`{3,}")

// ParseRecipes reads the recipes in a text: a recipe on its own, or a
// whole comment with one for each note that had a recording.
func ParseRecipes(text []byte) ([]Recipe, error) {
	candidates := [][]byte{bytes.TrimSpace(text)}
	if !bytes.HasPrefix(candidates[0], []byte("{")) {
		candidates = nil
		for _, m := range jsonBlock.FindAllSubmatch(text, -1) {
			candidates = append(candidates, m[2])
		}
	}
	var out []Recipe
	for _, c := range candidates {
		var r Recipe
		if json.Unmarshal(c, &r) != nil || r.Kind != recipeKind {
			continue
		}
		if r.Version > RecipeVersion {
			return nil, errs.New("the recipe is of a newer pit (version %d; this one reads %d)", r.Version, RecipeVersion).
				WithHint("update pit")
		}
		out = append(out, r)
	}
	if len(out) == 0 {
		return nil, errs.New("there is no pit recipe in it").
			WithHint("a recipe is the JSON block under \"Recipe for pit replay\" in a comment pit wrote")
	}
	return out, nil
}

// writeSteps lists what the reviewer did, for a person to do again.
func writeSteps(b *strings.Builder, n notes.Note) {
	if n.Recording == nil || len(n.Recording.Steps) == 0 {
		return
	}
	b.WriteString("\nWhat I did:\n\n")
	for i, s := range n.Recording.Steps {
		fmt.Fprintf(b, "%d. %s\n", i+1, stepText(s))
	}
}

// stepText is a step in words, with what was typed set as code and
// what was pressed as the page names it.
func stepText(s inspect.Step) string {
	name := code(s.Selector)
	if s.Text != "" {
		name = `"` + escapeText(s.Text) + `"`
	}
	switch s.Action {
	case inspect.Goto:
		return "Open " + code(pathOf(s.Path, s.Path))
	case inspect.Fill:
		if s.Secret {
			return "Type the password into " + name
		}
		if s.Value == "" {
			return "Empty " + name
		}
		return "Type " + code(clip(s.Value)) + " into " + name
	case inspect.Select:
		return fmt.Sprintf(`Choose "%s" in %s`, escapeText(orElse(s.Choice, s.Value)), name)
	case inspect.Press:
		return "Press " + s.Key + " in " + name
	default:
		return "Click " + name
	}
}

// writeRecipe adds the recipe, folded away: it is for pit, and the
// steps above say the same for a person.
func writeRecipe(b *strings.Builder, pr int, n notes.Note) {
	r, ok := RecipeOf(pr, n)
	if !ok {
		return
	}
	// Written for people as much as for pit: a selector's ">" as
	// itself, not as \u003e.
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(r); err != nil {
		return
	}
	body := strings.TrimSuffix(buf.String(), "\n")
	fence := strings.Repeat("`", max(3, longestRun(body, '`')+1))
	fmt.Fprintf(b, "\n<details>\n<summary>Recipe for pit replay</summary>\n\n"+
		"`pit replay %d` takes this comment, or this block saved as a file, and does the steps again from the scenario.\n\n"+
		"%sjson\n%s\n%s\n\n</details>\n", pr, fence, body, fence)
}

// escapeText keeps a page's words from being read as Markdown.
func escapeText(s string) string {
	return strings.NewReplacer(`\`, `\\`, "`", "\\`", "*", `\*`, "_", `\_`, "[", `\[`, "]", `\]`,
		"<", `\<`, ">", `\>`, "#", `\#`, "|", `\|`, `"`, `\"`).Replace(oneLine(s))
}
