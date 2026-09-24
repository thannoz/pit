package analysis

import (
	"net/url"
	"strings"
)

// Fill puts example values into an address's placeholders, so that
// /orders/{id} becomes an address a browser can open.
//
// A value for {id} is one segment and is escaped as one: a slash in it
// is data, not a separator. A value for {path...} is the rest of the
// address, so its slashes stay; each part between them is escaped.
//
// Placeholders without a value stay as they are, and missing names
// them. An address with a hole in it is still worth showing -- it says
// where to go, only not with what -- but it is not a link.
func Fill(path string, values map[string]string) (filled string, missing []string) {
	segments := strings.Split(path, "/")
	for i, seg := range segments {
		name, rest, ok := placeholder(seg)
		if !ok {
			continue
		}
		value, ok := values[name]
		if !ok || value == "" {
			missing = append(missing, name)
			continue
		}
		if !rest {
			segments[i] = url.PathEscape(value)
			continue
		}
		parts := strings.Split(strings.Trim(value, "/"), "/")
		for j, p := range parts {
			parts[j] = url.PathEscape(p)
		}
		segments[i] = strings.Join(parts, "/")
	}
	return strings.Join(segments, "/"), missing
}

// placeholder reports whether a segment is {name} or {name...}.
func placeholder(seg string) (name string, rest, ok bool) {
	if !strings.HasPrefix(seg, "{") || !strings.HasSuffix(seg, "}") {
		return "", false, false
	}
	name = seg[1 : len(seg)-1]
	name, rest = strings.CutSuffix(name, "...")
	return name, rest, name != ""
}
