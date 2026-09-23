package sandbox

import (
	"context"
	"log/slog"
	"time"

	"github.com/thannoz/pit/internal/state"
)

// steps narrates a setup and measures it at the same time.
//
// The two belong together: a step is announced, done, and took as long
// as it took, and keeping the measurement anywhere else would mean a
// second list of step names to keep in sync with this one. What is
// measured is therefore exactly what a reviewer was told about.
type steps struct {
	rep   Reporter
	name  string
	began time.Time
	taken []state.Step
}

func newSteps(rep Reporter) *steps {
	return &steps{rep: rep}
}

// begin announces a step and starts its clock.
func (s *steps) begin(name string, streams bool) {
	s.name, s.began = name, time.Now()
	s.rep.Begin(name, streams)
}

// done reports the step that was begun last and records how long it
// took.
func (s *steps) done(ctx context.Context, format string, args ...any) {
	took := time.Since(s.began)
	s.taken = append(s.taken, state.Step{Name: s.name, Millis: state.Millis(took)})

	// The narration rounds short steps away; the log does not, which
	// is the point of asking for it.
	slog.DebugContext(ctx, "step finished", "step", s.name, "took", took)
	s.rep.Done(format, args...)
}
