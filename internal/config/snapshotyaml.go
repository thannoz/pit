package config

import (
	"fmt"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/thannoz/pit/internal/hooks"
)

// UnmarshalYAML reads either form of data.snapshot: a mapping with save
// and restore, or a list of them, one for each database's service.
//
// The decoder's KnownFields does not reach inside a type that decodes
// itself, so the keys are checked here: a misspelt "restor" would
// otherwise be a snapshot that can be saved and never restored.
func (s *Snapshot) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.MappingNode:
		part, err := decodePart(n, "data.snapshot")
		if err != nil {
			return err
		}
		*s = Snapshot{Save: part.Save, Restore: part.Restore, Service: part.Service}
		return nil
	case yaml.SequenceNode:
		parts := make([]SnapshotPart, 0, len(n.Content))
		for i, item := range n.Content {
			if item.Kind != yaml.MappingNode {
				return &yaml.TypeError{Errors: []string{fmt.Sprintf(
					"line %d: data.snapshot[%d] is not a mapping; each entry has service, save and restore", item.Line, i)}}
			}
			part, err := decodePart(item, fmt.Sprintf("data.snapshot[%d]", i))
			if err != nil {
				return err
			}
			parts = append(parts, part)
		}
		*s = Snapshot{Parts: parts}
		return nil
	}
	return &yaml.TypeError{Errors: []string{fmt.Sprintf(
		"line %d: data.snapshot is neither save and restore commands nor a list of them", n.Line)}}
}

var partKeys = []string{"service", "save", "restore"}

func decodePart(n *yaml.Node, path string) (SnapshotPart, error) {
	for i := 0; i+1 < len(n.Content); i += 2 {
		if key := n.Content[i].Value; !slices.Contains(partKeys, key) {
			return SnapshotPart{}, &yaml.TypeError{Errors: []string{fmt.Sprintf(
				"line %d: unknown field %q in %s; it takes %s", n.Content[i].Line, key, path, strings.Join(partKeys, ", "))}}
		}
	}
	var part SnapshotPart
	if err := n.Decode(&part); err != nil {
		return SnapshotPart{}, err
	}
	return part, nil
}

// MarshalYAML writes the form it was read in, so that comparing two
// configurations compares what their authors wrote.
func (s Snapshot) MarshalYAML() (any, error) {
	if len(s.Parts) > 0 {
		return s.Parts, nil
	}
	single := map[string]string{}
	for k, v := range map[string]string{"save": s.Save, "restore": s.Restore, "service": s.Service} {
		if v != "" {
			single[k] = v
		}
	}
	return single, nil
}

// Target is the service a part's commands work on: the one it names,
// or else the one its save command runs in.
func (p SnapshotPart) Target() (string, bool) {
	if p.Service != "" {
		return p.Service, true
	}
	return hooks.ExecService(p.Save)
}

// Each is every pair of commands, whichever form they were written in.
func (s Snapshot) Each() []SnapshotPart {
	if len(s.Parts) > 0 {
		return s.Parts
	}
	if s.Save == "" && s.Restore == "" {
		return nil
	}
	return []SnapshotPart{{Service: s.Service, Save: s.Save, Restore: s.Restore}}
}

// UnmarshalYAML reads a scenario's snapshot: a path, or a mapping from
// each service to its path. The mapping is read in the file's order, so
// that the databases are loaded in the order the author wrote them.
func (s *ScenarioSnapshot) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.ScalarNode:
		if n.Tag == "!!null" {
			*s = ScenarioSnapshot{}
			return nil
		}
		*s = ScenarioSnapshot{File: n.Value}
		return nil
	case yaml.MappingNode:
		files := make([]SnapshotFile, 0, len(n.Content)/2)
		for i := 0; i+1 < len(n.Content); i += 2 {
			key, value := n.Content[i], n.Content[i+1]
			if value.Kind != yaml.ScalarNode {
				return &yaml.TypeError{Errors: []string{fmt.Sprintf(
					"line %d: the snapshot of %q is not a path", value.Line, key.Value)}}
			}
			files = append(files, SnapshotFile{Service: key.Value, File: value.Value})
		}
		*s = ScenarioSnapshot{Files: files}
		return nil
	}
	return &yaml.TypeError{Errors: []string{fmt.Sprintf(
		"line %d: a scenario's snapshot is a path, or a mapping from each service to its path", n.Line)}}
}

// MarshalYAML writes the form it was read in.
func (s ScenarioSnapshot) MarshalYAML() (any, error) {
	if len(s.Files) == 0 {
		if s.File == "" {
			return nil, nil
		}
		return s.File, nil
	}
	m := &yaml.Node{Kind: yaml.MappingNode}
	for _, f := range s.Files {
		m.Content = append(m.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: f.Service},
			&yaml.Node{Kind: yaml.ScalarNode, Value: f.File})
	}
	return m, nil
}

// IsZero lets a scenario without a snapshot leave the key out.
func (s ScenarioSnapshot) IsZero() bool { return s.File == "" && len(s.Files) == 0 }

// Each is every file, whichever form they were written in. The one file
// of the single form names no service.
func (s ScenarioSnapshot) Each() []SnapshotFile {
	if len(s.Files) > 0 {
		return s.Files
	}
	if s.File == "" {
		return nil
	}
	return []SnapshotFile{{File: s.File}}
}
