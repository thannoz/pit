package config

import (
	"reflect"
	"sort"

	"gopkg.in/yaml.v3"
)

// Differences names the sections in which two configurations disagree.
//
// It answers the question a reviewer has when a pull request brings its
// own file: not what every line says, but which parts of the setup this
// branch has changed. Section names are the file's own vocabulary --
// web, data, hooks -- because that is what the reader will look at.
func Differences(mine, theirs *Config) []string {
	a, errA := sections(mine)
	b, errB := sections(theirs)
	if errA != nil || errB != nil {
		// Nothing useful can be said about a comparison that failed,
		// and the caller's answer to "did anything change" has to stay
		// yes rather than become an error about YAML.
		return []string{"the whole file"}
	}

	seen := map[string]bool{}
	var changed []string
	for _, key := range append(keys(a), keys(b)...) {
		if seen[key] {
			continue
		}
		seen[key] = true
		if !reflect.DeepEqual(a[key], b[key]) {
			changed = append(changed, key)
		}
	}
	sort.Strings(changed)
	return changed
}

// sections re-renders a configuration and reads it back as a map, so
// that two of them can be compared without a field-by-field walk that
// would silently miss whatever is added to the schema next.
func sections(c *Config) (map[string]any, error) {
	data, err := yaml.Marshal(c)
	if err != nil {
		return nil, err
	}

	var out map[string]any
	if err := yaml.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
