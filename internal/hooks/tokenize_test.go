package hooks

import (
	"slices"
	"testing"

	"github.com/thannoz/pit/internal/errs"
)

func TestTokenize(t *testing.T) {
	tests := []struct {
		name string
		line string
		want []string
	}{
		{"plain words", "npm run migrate", []string{"npm", "run", "migrate"}},
		{"repeated spaces", "npm   run    migrate", []string{"npm", "run", "migrate"}},
		{"leading and trailing space", "  npm run  ", []string{"npm", "run"}},
		{"tabs count as spaces", "npm\trun", []string{"npm", "run"}},
		{
			name: "double quotes hold a phrase together",
			line: `psql -c "select count(*) from orders"`,
			want: []string{"psql", "-c", "select count(*) from orders"},
		},
		{
			name: "single quotes keep everything literal",
			line: `psql -c 'select ''a'' from t'`,
			want: []string{"psql", "-c", "select a from t"},
		},
		{
			name: "a backslash escapes a space",
			line: `cat /fixtures/with\ space.sql`,
			want: []string{"cat", "/fixtures/with space.sql"},
		},
		{
			name: "an escaped quote inside double quotes",
			line: `echo "she said \"hello\""`,
			want: []string{"echo", `she said "hello"`},
		},
		{
			name: "an empty argument survives",
			line: `psql -c ""`,
			want: []string{"psql", "-c", ""},
		},
		{
			name: "a backslash inside single quotes is literal",
			line: `grep 'a\b'`,
			want: []string{"grep", `a\b`},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tokenize(tt.line)
			if err != nil {
				t.Fatalf("tokenize(%q): %v", tt.line, err)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("tokenize(%q) = %q, want %q", tt.line, got, tt.want)
			}
		})
	}
}

func TestTokenizeRejectsBrokenInput(t *testing.T) {
	tests := []struct{ name, line string }{
		{"unbalanced double quote", `psql -c "select 1`},
		{"unbalanced single quote", `psql -c 'select 1`},
		{"empty", ""},
		{"only spaces", "   "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tokenize(tt.line)
			if err == nil {
				t.Fatalf("tokenize(%q) = nil error, want one", tt.line)
			}
			if errs.Hint(err) == "" && tt.line != "" && tt.line != "   " {
				t.Error("the error carries no hint")
			}
		})
	}
}

// TestNoGlobExpansion pins the decision not to involve a shell: a
// pattern reaches the program as written, so it decides what to do
// with it rather than the working directory deciding for it.
func TestNoGlobExpansion(t *testing.T) {
	got, err := tokenize("psql -f /fixtures/*.sql")
	if err != nil {
		t.Fatalf("tokenize: %v", err)
	}
	if want := []string{"psql", "-f", "/fixtures/*.sql"}; !slices.Equal(got, want) {
		t.Errorf("tokenize() = %q, want %q", got, want)
	}
}
