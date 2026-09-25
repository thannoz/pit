package config

import (
	"bytes"
	"encoding/json"
	"maps"
	"reflect"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/thannoz/pit/internal/errs"
)

// AddScenario adds a scenario to the end of data.scenarios in the text
// of a .pit.yaml, and changes nothing else.
//
// The file is edited as text, not decoded and encoded again: that
// would lose its comments, its quoting and the way its author folded
// the long commands, and a reviewer would find the whole file rewritten
// in a diff that was meant to add six lines. The YAML parser is asked
// only where things are.
//
// The result is read back before it is returned. Unless it holds the
// same configuration as before plus exactly this scenario, the edit is
// refused, and the caller has ScenarioYAML to show what to add by hand.
func AddScenario(src []byte, sc Scenario) ([]byte, error) {
	before, err := Parse(src)
	if err != nil {
		return nil, err
	}
	if _, ok := before.Scenario(sc.Name); ok {
		return nil, errs.New("there is a scenario named %q already", sc.Name)
	}

	out, err := insertScenario(src, sc)
	if err != nil {
		return nil, err
	}
	if err := checkAdded(before, out, sc); err != nil {
		return nil, err
	}
	return out, nil
}

// ScenarioYAML is a scenario as the lines to paste under
// data.scenarios, for when the file cannot be edited.
func ScenarioYAML(sc Scenario) string {
	return strings.Join(scenarioLines(sc, "    ", "      "), "\n") + "\n"
}

// cannotEdit is the refusal for a file whose shape the text edit does
// not handle. It is rare enough -- data written in flow style, several
// documents in one file -- that pasting six lines is the better answer
// than a YAML writer.
func cannotEdit(why string) error {
	return errs.New("pit cannot add the scenario to this .pit.yaml itself: %s", why)
}

// insertScenario finds where the new lines go and puts them there.
func insertScenario(src []byte, sc Scenario) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil {
		return nil, err
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode || doc.Content[0].Style&yaml.FlowStyle != 0 {
		return nil, cannotEdit("its top level is not a mapping written in block style")
	}
	if documentMarker.Match(src) {
		return nil, cannotEdit("it has more than one document, or marks where one ends")
	}

	eol := "\n"
	if bytes.Contains(src, []byte("\r\n")) {
		eol = "\r\n"
	}
	text := string(src)
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += eol
	}
	lines := strings.SplitAfter(text, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	at, add, err := placement(doc.Content[0], lines, sc)
	if err != nil {
		return nil, err
	}
	for i := range add {
		add[i] += eol
	}
	out := slices.Concat(lines[:at], add, lines[at:])
	return []byte(strings.Join(out, "")), nil
}

// documentMarker finds a "---" after the first line, or a "...".
var documentMarker = regexp.MustCompile(`(?m)(\A(?:.*\n)+---(\s|$))|(^\.\.\.(\s|$))`)

// placement says after which line the scenario goes and what it is
// written as there, going by what the file already has: a list of
// scenarios is continued in its own indentation, a data section
// without one gets it at the end, a file without data gets that.
func placement(root *yaml.Node, lines []string, sc Scenario) (int, []string, error) {
	dataKey, data := child(root, "data")
	if dataKey == nil {
		// At the end of the file, after a blank line like any other
		// section.
		add := []string{"data:", "  scenarios:"}
		add = append(add, scenarioLines(sc, "    ", "      ")...)
		if len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) != "" {
			add = append([]string{""}, add...)
		}
		return len(lines), add, nil
	}

	dataCol := dataKey.Column - 1
	if isNull(data) {
		inner := strings.Repeat(" ", dataCol+2)
		add := append([]string{inner + "scenarios:"}, scenarioLines(sc, inner+"  ", inner+"    ")...)
		return end(lines, dataKey.Line-1, dataCol), add, nil
	}
	if data.Kind != yaml.MappingNode || data.Style&yaml.FlowStyle != 0 {
		return 0, nil, cannotEdit("data is not a mapping written in block style")
	}

	key, list := child(data, "scenarios")
	if key == nil {
		// At the end of data, in the indentation of its other keys.
		inner := strings.Repeat(" ", data.Column-1)
		add := append([]string{inner + "scenarios:"}, scenarioLines(sc, inner+"  ", inner+"    ")...)
		return end(lines, dataKey.Line-1, dataCol), add, nil
	}
	keyCol := key.Column - 1
	if isNull(list) {
		inner := strings.Repeat(" ", keyCol)
		return end(lines, key.Line-1, keyCol), scenarioLines(sc, inner+"  ", inner+"    "), nil
	}
	if list.Kind != yaml.SequenceNode || list.Style&yaml.FlowStyle != 0 || len(list.Content) == 0 {
		return 0, nil, cannotEdit("data.scenarios is not a list written in block style")
	}

	last := list.Content[len(list.Content)-1]
	if last.Kind != yaml.MappingNode || last.Style&yaml.FlowStyle != 0 {
		return 0, nil, cannotEdit("its last scenario is not a mapping written in block style")
	}
	dash := list.Column - 1
	itemCol := last.Column - 1
	if last.Line != list.Content[0].Line && dashAt(lines, last.Line-1) < 0 {
		return 0, nil, cannotEdit("its last scenario does not start on the line of its dash")
	}
	add := scenarioLines(sc, strings.Repeat(" ", dash), strings.Repeat(" ", itemCol))

	// Scenarios kept apart by a blank line get one more of those.
	if len(list.Content) > 1 {
		if prev := last.Line - 2; prev >= 0 && strings.TrimSpace(lines[prev]) == "" {
			add = append([]string{""}, add...)
		}
	}
	return end(lines, last.Line-1, dash), add, nil
}

// end is the line after the block that starts at line start: the first
// line after it that says something and is indented no deeper than
// col. Blank lines and comments at its end are left to what follows;
// a comment indented into the block belongs to it.
func end(lines []string, start, col int) int {
	after := start + 1
	for i := start + 1; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "" {
			continue
		}
		indent := len(lines[i]) - len(strings.TrimLeft(lines[i], " "))
		if indent <= col {
			break
		}
		after = i + 1
	}
	return after
}

// dashAt is the column of the dash that starts line i, or -1.
func dashAt(lines []string, i int) int {
	trimmed := strings.TrimLeft(lines[i], " ")
	if strings.HasPrefix(trimmed, "- ") {
		return len(lines[i]) - len(trimmed)
	}
	return -1
}

func child(m *yaml.Node, name string) (*yaml.Node, *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == name {
			return m.Content[i], m.Content[i+1]
		}
	}
	return nil, nil
}

func isNull(n *yaml.Node) bool {
	return n.Kind == yaml.ScalarNode && n.Tag == "!!null" && (n.Value == "" || n.Value == "~" || n.Value == "null")
}

// scenarioLines writes a scenario: dash is the indentation of the dash,
// key that of its keys.
func scenarioLines(sc Scenario, dash, key string) []string {
	gap := max(len(key)-len(dash)-1, 1)
	out := []string{dash + "-" + strings.Repeat(" ", gap) + "name: " + scalar(sc.Name)}
	if sc.Description != "" {
		out = append(out, key+"description: "+quoted(sc.Description))
	}
	files := sc.Snapshot.Each()
	if len(files) == 1 && files[0].Service == "" {
		out = append(out, key+"snapshot: "+scalar(files[0].File))
	} else if len(files) > 0 {
		out = append(out, key+"snapshot:")
		for _, f := range files {
			out = append(out, key+"  "+scalar(f.Service)+": "+scalar(f.File))
		}
	}
	if len(sc.Params) > 0 {
		out = append(out, key+"params:")
		for _, k := range slices.Sorted(maps.Keys(sc.Params)) {
			out = append(out, key+"  "+scalar(k)+": "+quoted(sc.Params[k]))
		}
	}
	return out
}

// plain is what can be written without quotes and read back as the same
// string: names and paths, not something YAML would take for a number,
// a boolean or a null.
var plain = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_./-]*$`)

var special = map[string]bool{
	"true": true, "false": true, "yes": true, "no": true, "on": true, "off": true,
	"y": true, "n": true, "null": true, "~": true,
}

func scalar(s string) string {
	if plain.MatchString(s) && !special[strings.ToLower(s)] {
		return s
	}
	return quoted(s)
}

// quoted writes a double-quoted YAML string. A JSON string is one.
func quoted(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// checkAdded reads the edited file back and compares: the scenario has
// to be there as meant, and everything else as it was.
func checkAdded(before *Config, out []byte, sc Scenario) error {
	after, err := Parse(out)
	if err != nil {
		return cannotEdit("the edited file could not be read back: " + err.Error())
	}
	got, ok := after.Scenario(sc.Name)
	if !ok || !sameScenario(got, sc) {
		return cannotEdit("the edited file did not read back as the scenario meant")
	}
	after.Data.Scenarios = slices.DeleteFunc(after.Data.Scenarios, func(s Scenario) bool { return s.Name == sc.Name })
	if len(after.Data.Scenarios) == 0 {
		after.Data.Scenarios = nil
	}
	a, errA := sections(before)
	b, errB := sections(after)
	if errA != nil || errB != nil || !reflect.DeepEqual(a, b) {
		return cannotEdit("the edit would have changed more than the new scenario")
	}
	return nil
}

func sameScenario(a, b Scenario) bool {
	if len(a.Params) == 0 && len(b.Params) == 0 {
		a.Params, b.Params = nil, nil
	}
	return reflect.DeepEqual(a, b)
}
