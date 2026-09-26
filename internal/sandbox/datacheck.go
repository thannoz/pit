package sandbox

import (
	"bytes"
	"context"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/thannoz/pit/internal/config"
	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/hooks"
	"github.com/thannoz/pit/internal/state"
)

// Loss is data a migration took away.
type Loss struct {
	Table string
	// Column is the column that went, or empty when the table did, or
	// some of its rows.
	Column string
	// Rows are the rows that went, or that had the column.
	Rows int64
	// Dropped says the whole table went.
	Dropped bool
}

// Lock is a table a migration held so that others had to wait.
type Lock struct {
	Table string
	// Mode is the database's word for how: AccessExclusiveLock waits
	// even for reads.
	Mode string
	// Held is how long pit saw it held, from its first look to its
	// last; a lock seen once was held for less than a look takes.
	Held time.Duration
}

// shape is what a database holds: the rows of each table, and its
// columns.
type shape struct {
	rows    map[string]int64
	columns map[string][]string
}

func (s shape) total() int64 {
	var n int64
	for _, r := range s.rows {
		n += r
	}
	return n
}

// shapeOf asks the database what it holds, with the commands the
// configuration gives. A command not configured leaves its part empty.
func (m *Manager) shapeOf(ctx context.Context, box state.Sandbox, c config.Check) (shape, error) {
	s := shape{rows: map[string]int64{}, columns: map[string][]string{}}
	if c.Rows != "" {
		lines, err := m.fields(ctx, box, "data.check.rows", c.Rows)
		if err != nil {
			return s, err
		}
		for _, f := range lines {
			if len(f) < 2 {
				continue
			}
			n, err := strconv.ParseInt(strings.TrimSpace(f[1]), 10, 64)
			if err != nil {
				return s, errs.New("data.check.rows printed %q for %s, which is not a number of rows", f[1], f[0])
			}
			s.rows[f[0]] = n
		}
	}
	if c.Columns != "" {
		lines, err := m.fields(ctx, box, "data.check.columns", c.Columns)
		if err != nil {
			return s, err
		}
		for _, f := range lines {
			if len(f) >= 2 {
				s.columns[f[0]] = append(s.columns[f[0]], f[1])
			}
		}
	}
	return s, nil
}

// fields runs a check command and splits what it printed: one line per
// thing, fields separated by "|" or a tab.
func (m *Manager) fields(ctx context.Context, box state.Sandbox, path, line string) ([][]string, error) {
	target := commandsIn(box)
	var out, stderr bytes.Buffer
	if err := hooks.Run(ctx, m.Proc, hooks.List{Path: path, Lines: []string{line}}, target, &out, &stderr); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, errs.Wrap(err, "%s", msg)
		}
		return nil, err
	}
	var lines [][]string
	for _, l := range strings.Split(out.String(), "\n") {
		if l = strings.TrimSpace(l); l == "" {
			continue
		}
		sep := "|"
		if !strings.Contains(l, sep) {
			sep = "\t"
		}
		parts := strings.Split(l, sep)
		for i := range parts {
			parts[i] = strings.TrimSpace(parts[i])
		}
		lines = append(lines, parts)
	}
	return lines, nil
}

// losses compares what a database held before a migration with what it
// holds after: a table gone, a column gone, rows gone. A table that
// went is said once, not once for each of its columns.
func losses(before, after shape) []Loss {
	var out []Loss
	tables := make([]string, 0, len(before.rows)+len(before.columns))
	for t := range before.rows {
		tables = append(tables, t)
	}
	for t := range before.columns {
		if _, ok := before.rows[t]; !ok {
			tables = append(tables, t)
		}
	}
	slices.Sort(tables)
	for _, t := range tables {
		_, hasRows := after.rows[t]
		_, hasColumns := after.columns[t]
		if !hasRows && !hasColumns {
			out = append(out, Loss{Table: t, Rows: before.rows[t], Dropped: true})
		}
		if b, ok := before.rows[t]; ok && hasRows && after.rows[t] < b {
			out = append(out, Loss{Table: t, Rows: b - after.rows[t]})
		}
		if hasColumns {
			for _, c := range before.columns[t] {
				if !slices.Contains(after.columns[t], c) {
					out = append(out, Loss{Table: t, Column: c, Rows: before.rows[t]})
				}
			}
		}
	}
	return out
}

// lockWatch looks, again and again, at which tables are locked, until it
// is stopped.
type lockWatch struct {
	mu    sync.Mutex
	seen  map[string]*Lock
	first map[string]time.Time
	stop  context.CancelFunc
	done  chan struct{}
}

// watchLocks starts looking at the locks of a sandbox's database with
// the locks command.
func (m *Manager) watchLocks(ctx context.Context, box state.Sandbox, line string, every time.Duration) *lockWatch {
	ctx, cancel := context.WithCancel(ctx)
	w := &lockWatch{seen: map[string]*Lock{}, first: map[string]time.Time{}, stop: cancel, done: make(chan struct{})}
	go func() {
		defer close(w.done)
		for ctx.Err() == nil {
			looked := time.Now()
			lines, err := m.fields(ctx, box, "data.check.locks", line)
			if err == nil {
				w.saw(looked, lines)
			}
			select {
			case <-ctx.Done():
			case <-time.After(every):
			}
		}
	}()
	return w
}

func (w *lockWatch) saw(at time.Time, lines [][]string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, f := range lines {
		if len(f) < 2 {
			continue
		}
		table, mode := f[0], f[1]
		first, ok := w.first[table]
		if !ok {
			w.first[table], first = at, at
			w.seen[table] = &Lock{Table: table, Mode: mode}
		}
		l := w.seen[table]
		l.Held = at.Sub(first)
		// The strongest mode seen is the one that says most.
		if mode == "AccessExclusiveLock" {
			l.Mode = mode
		}
	}
}

// locks stops looking and says what was seen, table by table.
func (w *lockWatch) locks() []Lock {
	w.stop()
	<-w.done
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]Lock, 0, len(w.seen))
	for _, l := range w.seen {
		out = append(out, *l)
	}
	slices.SortFunc(out, func(a, b Lock) int { return strings.Compare(a.Table, b.Table) })
	return out
}
