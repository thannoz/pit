package sandbox

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thannoz/pit/internal/proc"
	"github.com/thannoz/pit/internal/state"
)

func TestCompareWrites(t *testing.T) {
	for _, tc := range []struct {
		name          string
		baseline, now []int64
		want          Edit
	}{
		{"the same", []int64{7}, []int64{7}, Unedited},
		{"more", []int64{7}, []int64{8}, Edited},
		{"fewer: the database began counting again", []int64{7}, []int64{0}, EditUnknown},
		{"one of two", []int64{7, 3}, []int64{7, 4}, Edited},
		{"one restarted, the other written to", []int64{7, 3}, []int64{0, 4}, Edited},
		{"one restarted, the other not", []int64{7, 3}, []int64{0, 3}, EditUnknown},
		{"a database more than before", []int64{7}, []int64{7, 0}, EditUnknown},
	} {
		if got := compareWrites(tc.baseline, tc.now); got != tc.want {
			t.Errorf("%s: compareWrites(%v, %v) = %v, want %v", tc.name, tc.baseline, tc.now, got, tc.want)
		}
	}
}

// counting answers every command with the next line of its script.
type counting struct {
	out []string
	ran []string
}

func (c *counting) Stream(_ context.Context, cmd proc.Command, stdout, stderr io.Writer) error {
	c.ran = append(c.ran, strings.Join(cmd.Args, " "))
	line := c.out[0]
	c.out = c.out[1:]
	if strings.HasPrefix(line, "!") {
		_, _ = io.WriteString(stderr, line[1:]+"\n")
		return io.ErrUnexpectedEOF
	}
	_, err := io.WriteString(stdout, line)
	return err
}

func writesBox(t *testing.T, yaml string) state.Sandbox {
	t.Helper()
	dir := t.TempDir()
	for name, content := range map[string]string{".git": "", "docker-compose.yml": "services:\n  db:\n    image: postgres:17\n", ".pit.yaml": yaml} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return state.Sandbox{PR: 7, Project: "p", Worktree: dir, RepoRoot: dir}
}

func TestCountWrites(t *testing.T) {
	two := writesBox(t, `version: 1
web: {service: web, port: 80}
data:
  snapshot:
    - {service: db, save: a, restore: b, writes: "compose exec -T db count"}
    - {service: cache, save: c, restore: d}
    - {service: stock, save: e, restore: f, writes: "compose exec -T stock count"}
`)
	c := &counting{out: []string{"12\n", " 5 \n"}}
	m := &Manager{Proc: c}
	got, err := m.countWrites(t.Context(), two)
	if err != nil || len(got) != 2 || got[0] != 12 || got[1] != 5 {
		t.Fatalf("counts = %v, %v", got, err)
	}
	if len(c.ran) != 2 || !strings.HasSuffix(c.ran[1], "exec -T stock count") {
		t.Errorf("ran %q", c.ran)
	}

	c = &counting{out: []string{"12\n", "ERROR: no such table\n"}}
	m.Proc = c
	if _, err := m.countWrites(t.Context(), two); err == nil || !strings.Contains(err.Error(), `data.snapshot[2].writes printed "ERROR: no such table", which is not a number`) {
		t.Errorf("err = %v", err)
	}
	c = &counting{out: []string{"!psql: connection refused"}}
	m.Proc = c
	if _, err := m.countWrites(t.Context(), two); err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("err = %v", err)
	}

	// Without writes commands, there is nothing to count and nothing
	// wrong.
	none := writesBox(t, "version: 1\nweb: {service: web, port: 80}\ndata:\n  snapshot: {save: a, restore: b}\n")
	m.Proc = &counting{}
	if got, err := m.countWrites(t.Context(), none); got != nil || err != nil {
		t.Errorf("counts = %v, %v", got, err)
	}
}

func TestEditedSince(t *testing.T) {
	box := writesBox(t, "version: 1\nweb: {service: web, port: 80}\ndata:\n  snapshot: {save: a, restore: b, writes: \"compose exec -T db count\"}\n")
	m := &Manager{}
	box.Writes = []int64{4}
	for now, want := range map[string]Edit{"4": Unedited, "6": Edited, "0": EditUnknown} {
		m.Proc = &counting{out: []string{now}}
		if got := m.editedSince(t.Context(), box); got != want {
			t.Errorf("count %s: %v, want %v", now, got, want)
		}
	}
	// Recorded as edited, it stays so whatever the count says.
	box.Edited = true
	m.Proc = &counting{out: []string{"4"}}
	if got := m.editedSince(t.Context(), box); got != Edited {
		t.Errorf("got %v", got)
	}
	// Never counted: pit cannot tell, and asks nobody.
	box.Edited, box.Writes = false, nil
	m.Proc = &counting{}
	if got := m.editedSince(t.Context(), box); got != EditUnknown {
		t.Errorf("got %v", got)
	}
}
