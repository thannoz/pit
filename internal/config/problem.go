package config

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// Problem is one thing wrong with a configuration, located in the file
// so the user can go straight to it.
type Problem struct {
	// Line is the line in the file, or 0 when the problem is that
	// something is missing and so has no line of its own.
	Line int
	// Path is the field, in dotted form: "web.port".
	Path string
	// Msg says what is wrong.
	Msg string
	// Hint says what to do about it.
	Hint string
}

func (p Problem) String() string {
	loc := p.Path
	if p.Line > 0 {
		loc = fmt.Sprintf("line %d: %s", p.Line, p.Path)
	}
	s := loc + ": " + p.Msg
	if p.Hint != "" {
		s += "\n      " + p.Hint
	}
	return s
}

// InvalidError reports everything wrong with a configuration at once.
// Fixing one problem per run would make a file with three mistakes take
// three runs to sort out.
type InvalidError struct {
	File     string
	Problems []Problem
}

func (e *InvalidError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s has %s", e.File, plural(len(e.Problems), "problem"))
	for _, p := range e.Problems {
		b.WriteString("\n  " + p.String())
	}
	return b.String()
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// lineOf finds the line a field sits on, walking the document by its
// path. It returns 0 when the field is absent, which is exactly the
// case where there is no line to point at.
func lineOf(root *yaml.Node, path ...string) int {
	node := root
	if node != nil && node.Kind == yaml.DocumentNode && len(node.Content) > 0 {
		node = node.Content[0]
	}

	for _, key := range path {
		if node == nil || node.Kind != yaml.MappingNode {
			return 0
		}
		found := false
		// A mapping's Content alternates key, value, key, value.
		for i := 0; i+1 < len(node.Content); i += 2 {
			if node.Content[i].Value == key {
				node = node.Content[i+1]
				found = true
				break
			}
		}
		if !found {
			return 0
		}
	}
	if node == nil {
		return 0
	}
	return node.Line
}

// lineOfIndex finds the line of an element inside a sequence.
func lineOfIndex(root *yaml.Node, index int, path ...string) int {
	node := root
	if node != nil && node.Kind == yaml.DocumentNode && len(node.Content) > 0 {
		node = node.Content[0]
	}
	for _, key := range path {
		if node == nil || node.Kind != yaml.MappingNode {
			return 0
		}
		found := false
		for i := 0; i+1 < len(node.Content); i += 2 {
			if node.Content[i].Value == key {
				node = node.Content[i+1]
				found = true
				break
			}
		}
		if !found {
			return 0
		}
	}
	if node == nil || node.Kind != yaml.SequenceNode || index >= len(node.Content) {
		return 0
	}
	return node.Content[index].Line
}
