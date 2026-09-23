package sandbox

import (
	"context"
	"log/slog"
	"time"

	"github.com/thannoz/pit/internal/config"
	"github.com/thannoz/pit/internal/data"
	"github.com/thannoz/pit/internal/runtime"
	"github.com/thannoz/pit/internal/state"
)

// reuseProbe is how long a sandbox has to prove it still works before
// pit gives up on it and builds a new one.
//
// Short on purpose: the whole value of reusing is that it is nearly
// instant, and a sandbox that has to be coaxed for half a minute is
// not the thing the reviewer asked for. Rebuilding is slow but always
// works, so the cheap attempt should fail fast.
const reuseProbe = 3 * time.Second

// answers asks the sandbox itself rather than the record.
//
// Every cheaper question -- is it recorded, are its containers listed,
// does the runtime call it running -- can be answered yes by something
// a reviewer cannot use. The only question worth asking is the one the
// reviewer will ask in a moment.
func (m *Manager) answers(ctx context.Context, box state.Sandbox, c *config.Config) bool {
	probe := runtime.Probe{
		URL:          box.URL,
		ExpectStatus: c.Healthcheck.ExpectStatus,
		Timeout:      reuseProbe,
		Interval:     c.Healthcheck.Interval.Duration(),
	}

	err := m.Runtime.WaitReady(ctx, RuntimeSandbox(box), box.WebService, probe)
	if err != nil {
		slog.DebugContext(ctx, "the sandbox does not answer, building instead", "error", err)
		return false
	}
	return true
}

// reuse hands back a sandbox that is already running, loading the data
// again only when a different state was asked for.
//
// The containers are left alone even then: they are the expensive
// part, and the data is seconds.
func (m *Manager) reuse(ctx context.Context, box state.Sandbox, sc data.Scenario, st *steps) (state.Sandbox, error) {
	st.begin("reuse", quiet)
	st.done(ctx, "already running, %s old", shortAge(box.Age()))

	if sc.Empty() || sc.Name == box.Scenario {
		return box, nil
	}

	if err := m.ResetData(ctx, box, sc, st.rep); err != nil {
		return state.Sandbox{}, err
	}
	box.Scenario = sc.Name
	return box, nil
}

// shortAge says how old something is the way a person would.
func shortAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "seconds"
	case d < time.Hour:
		return d.Round(time.Minute).String()
	default:
		return d.Round(time.Hour).String()
	}
}
