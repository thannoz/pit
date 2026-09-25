package sandbox

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/thannoz/pit/internal/analysis"
	"github.com/thannoz/pit/internal/hooks"
	"github.com/thannoz/pit/internal/proc"
	"github.com/thannoz/pit/internal/state"
)

// Rollback is what undoing a pull request's migrations showed.
type Rollback struct {
	// Ran says data.rollback was run; Took how long it did, and
	// Failed why it failed.
	Ran    bool
	Took   time.Duration
	Failed error
	// Leftover is how the schema differs, after the rollback, from
	// the base's: what is still there, what did not come back.
	Leftover []string
	// Compared says the schema could be compared at all.
	Compared bool
	// MissingDown are new migrations whose down migration, where the
	// project keeps them in pairs, is not there.
	MissingDown []string
}

// Reversible reports whether the rollback ran and left the schema as
// the base had it.
func (r Rollback) Reversible() bool {
	return r.Ran && r.Failed == nil && len(r.Leftover) == 0 && len(r.MissingDown) == 0
}

// rollBack runs data.rollback on a sandbox the migrations ran in, and
// compares the schema it leaves with the base's.
func (m *Manager) rollBack(ctx context.Context, box state.Sandbox, lines []string, n int, before shape, look func() (shape, bool)) Rollback {
	var r Rollback
	if len(lines) == 0 {
		return r
	}
	expanded := make([]string, len(lines))
	for i, l := range lines {
		expanded[i] = strings.ReplaceAll(l, "{migrations}", strconv.Itoa(n))
	}
	target := hooks.Sandbox{Project: box.Project, Files: box.ComposeFiles, Dir: box.Worktree}
	var stderr bytes.Buffer
	started := time.Now()
	err := hooks.Run(ctx, m.Proc, hooks.List{Path: "data.rollback", Lines: expanded}, target, &bytes.Buffer{}, &stderr)
	r.Ran, r.Took = true, time.Since(started)
	if err != nil {
		r.Failed = err
		return r
	}
	if after, ok := look(); ok && (len(before.columns) > 0 || len(before.rows) > 0) {
		r.Compared = true
		r.Leftover = schemaDiff(before, after)
	}
	return r
}

// schemaDiff says how a schema differs from what it was: tables and
// columns that are there and were not, or were and are not.
func schemaDiff(was, is shape) []string {
	var out []string
	tables := func(s shape) []string {
		var t []string
		for name := range s.rows {
			t = append(t, name)
		}
		for name := range s.columns {
			if !slices.Contains(t, name) {
				t = append(t, name)
			}
		}
		slices.Sort(t)
		return t
	}
	wasTables, isTables := tables(was), tables(is)
	for _, t := range isTables {
		if !slices.Contains(wasTables, t) {
			out = append(out, fmt.Sprintf("table %s is still there", t))
		}
	}
	for _, t := range wasTables {
		if !slices.Contains(isTables, t) {
			out = append(out, fmt.Sprintf("table %s did not come back", t))
			continue
		}
		for _, c := range is.columns[t] {
			if len(was.columns[t]) > 0 && !slices.Contains(was.columns[t], c) {
				out = append(out, fmt.Sprintf("column %s.%s is still there", t, c))
			}
		}
		for _, c := range was.columns[t] {
			if !slices.Contains(is.columns[t], c) {
				out = append(out, fmt.Sprintf("column %s.%s did not come back", t, c))
			}
		}
	}
	return out
}

// missingDown finds the new migrations that come in up and down pairs
// -- 0042_vat.up.sql and 0042_vat.down.sql, or up.sql and down.sql in a
// folder of their own -- whose down is not at head.
func (m *Manager) missingDown(ctx context.Context, root, head string, files []analysis.File) []string {
	var out []string
	for _, f := range files {
		down := downOf(f.Path)
		if down == "" {
			continue
		}
		if _, err := m.Git.Output(ctx, proc.Command{Name: "git", Args: []string{"cat-file", "-e", head + ":" + down}, Dir: root}); err != nil {
			out = append(out, f.Path)
		}
	}
	return out
}

// downOf is the down migration that goes with an up migration, or
// empty for a migration that is not one of a pair.
func downOf(path string) string {
	dir, base := "", path
	if i := strings.LastIndex(path, "/"); i >= 0 {
		dir, base = path[:i+1], path[i+1:]
	}
	switch {
	case strings.Contains(base, ".up."):
		return dir + strings.Replace(base, ".up.", ".down.", 1)
	case strings.HasPrefix(base, "up."):
		return dir + "down." + strings.TrimPrefix(base, "up.")
	}
	return ""
}
