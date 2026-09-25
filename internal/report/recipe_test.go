package report

import (
	"strings"
	"testing"

	"github.com/thannoz/pit/internal/inspect"
	"github.com/thannoz/pit/internal/notes"
)

var ordered = notes.Note{
	ID: 2, Text: "A new order shows no refund line", URL: "http://localhost:43077/orders/1004", SHA: "3701136aa94", Scenario: "standard",
	Recording: &notes.Recording{SHA: "3701136aa94", Scenario: "standard", Steps: []inspect.Step{
		{Action: inspect.Goto, Path: "/"},
		{Action: inspect.Fill, Selector: `input[name="item"]`, Text: "item", Value: "Genmaicha, 100 g"},
		{Action: inspect.Select, Selector: `select[name="size"]`, Text: "size", Value: "l", Choice: "Large"},
		{Action: inspect.Fill, Selector: `input[name="pin"]`, Text: "pin", Secret: true},
		{Action: inspect.Click, Selector: "body > form > button", Text: "Order it"},
		{Action: inspect.Press, Selector: "#q", Text: "q", Key: "Enter"},
		{Action: inspect.Click, Selector: "div.card > span:nth-of-type(2)"},
		{Action: inspect.Fill, Selector: "#note", Text: "note"},
	}},
}

// TestRecipeInTheComment is the comment's half of T-808: what the
// reviewer did, as steps a person follows and a recipe pit replays.
func TestRecipeInTheComment(t *testing.T) {
	got := Comment(Input{PR: 7, Notes: []notes.Note{ordered}})
	for _, want := range []string{
		"Page: `/orders/1004`\n\nWhat I did:\n\n" +
			"1. Open `/`\n" +
			"2. Type `Genmaicha, 100 g` into \"item\"\n" +
			"3. Choose \"Large\" in \"size\"\n" +
			"4. Type the password into \"pin\"\n" +
			"5. Click \"Order it\"\n" +
			"6. Press Enter in \"q\"\n" +
			"7. Click `div.card > span:nth-of-type(2)`\n" +
			"8. Empty \"note\"\n\n" +
			"The browser reported no errors on the page.\n",
		"<details>\n<summary>Recipe for pit replay</summary>\n\n`pit replay 7` takes this comment, or this block saved as a file, and does the steps again from the scenario.\n\n```json\n{\n  \"pit\": \"recipe\",\n  \"version\": 1,\n  \"pr\": 7,\n  \"note\": 2,\n",
		`"selector": "body > form > button",`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the comment lacks\n%s\nin\n%s", want, got)
		}
	}
	recipes, err := ParseRecipes([]byte(got))
	if err != nil || len(recipes) != 1 {
		t.Fatalf("%v, %d recipes", err, len(recipes))
	}
	r := recipes[0]
	if r.PR != 7 || r.Note != 2 || r.Scenario != "standard" || r.SHA != "3701136aa94" || len(r.Steps) != 8 || r.Steps[4] != ordered.Recording.Steps[4] {
		t.Errorf("read back %+v", r)
	}
}

func TestNoRecordingNoRecipe(t *testing.T) {
	got := Comment(Input{PR: 7, Notes: []notes.Note{voucher}})
	if strings.Contains(got, "What I did") || strings.Contains(got, "Recipe") {
		t.Errorf("the comment is\n%s", got)
	}
	n := voucher
	n.Recording = &notes.Recording{SHA: "3701136aa94", Scenario: "refunded"}
	if got := Comment(Input{PR: 7, Notes: []notes.Note{n}}); strings.Contains(got, "Recipe") {
		t.Errorf("a recording of no steps made a recipe:\n%s", got)
	}
}

func TestARecordingOnChangedData(t *testing.T) {
	n := ordered
	rec := *n.Recording
	rec.Edited = true
	n.Recording = &rec
	if got := Comment(Input{PR: 7, Notes: []notes.Note{n}}); !strings.Contains(got, "The recording began on data I had changed by hand; the steps alone may not lead there.") {
		t.Errorf("the comment is\n%s", got)
	}
}

func TestParseRecipes(t *testing.T) {
	a, b := ordered, ordered
	b.ID = 5
	two := Comment(Input{PR: 7, Notes: []notes.Note{a, b}})
	recipes, err := ParseRecipes([]byte("Copied from GitHub:\n\n" + two))
	if err != nil || len(recipes) != 2 || recipes[0].Note != 2 || recipes[1].Note != 5 {
		t.Errorf("%v: %+v", err, recipes)
	}

	alone := `  {"pit": "recipe", "version": 1, "pr": 7, "note": 3, "sha": "abc", "steps": [{"action": "goto", "path": "/"}]}` + "\n"
	recipes, err = ParseRecipes([]byte(alone))
	if err != nil || len(recipes) != 1 || recipes[0].Note != 3 || recipes[0].Steps[0].Path != "/" {
		t.Errorf("%v: %+v", err, recipes)
	}

	for text, want := range map[string]string{
		"just words":                               "there is no pit recipe in it",
		"```json\n{\"other\": true}\n```":          "there is no pit recipe in it",
		`{"pit": "recipe", "version": 9, "pr": 7}`: "the recipe is of a newer pit (version 9",
	} {
		if _, err := ParseRecipes([]byte(text)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v", text, err)
		}
	}
}

// A page's words cannot turn into Markdown in the steps.
func TestStepWordsAreEscaped(t *testing.T) {
	s := inspect.Step{Action: inspect.Click, Selector: "a", Text: `*Buy* [now] <b>|#1 "best"`}
	if got := stepText(s); got != `Click "\*Buy\* \[now\] \<b\>\|\#1 \"best\""` {
		t.Errorf("stepText = %s", got)
	}
}
