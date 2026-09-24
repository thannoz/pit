package analysis_test

import (
	"slices"
	"testing"

	"github.com/thannoz/pit/internal/analysis"
)

func TestFill(t *testing.T) {
	values := map[string]string{
		"slug":  "cursor",
		"id":    "1042",
		"path":  "installation/using vite/",
		"odd":   "a/b?c#d",
		"empty": "",
	}
	for _, tc := range []struct {
		path, want string
		missing    []string
	}{
		{"/", "/", nil},
		{"/partners", "/partners", nil},
		{"/partners/{slug}", "/partners/cursor", nil},
		{"/orders/{id}/items/{slug}", "/orders/1042/items/cursor", nil},
		// The rest of the address keeps its slashes; each part is
		// escaped, and a trailing slash in the value adds nothing.
		{"/docs/{path...}", "/docs/installation/using%20vite", nil},
		// One segment is one segment: its slash is data.
		{"/search/{odd}", "/search/a%2Fb%3Fc%23d", nil},
		// What has no value stays visible, and is named.
		{"/groups/{groupId}/partners/{slug}", "/groups/{groupId}/partners/cursor", []string{"groupId"}},
		{"/x/{empty}", "/x/{empty}", []string{"empty"}},
		// Braces that are not a whole segment are not a placeholder.
		{"/a{b}/{}", "/a{b}/{}", nil},
	} {
		got, missing := analysis.Fill(tc.path, values)
		if got != tc.want || !slices.Equal(missing, tc.missing) {
			t.Errorf("Fill(%q) = %q, %v; want %q, %v", tc.path, got, missing, tc.want, tc.missing)
		}
	}
}
