package runtime_test

import (
	"io"
	"testing"
	"time"

	"github.com/thannoz/pit/internal/runtime"
	"github.com/thannoz/pit/internal/runtime/runtimetest"
)

// Every call goes to the runtime the sandbox was brought up with.
func TestEitherAsksTheSandboxsRuntime(t *testing.T) {
	compose, processes := runtimetest.New("web"), runtimetest.New("web")
	e := runtime.Either{Compose: compose, Processes: processes}
	ctx := t.Context()
	calls := func(s runtime.Sandbox) {
		_ = e.Up(ctx, s, nil, io.Discard, io.Discard)
		_ = e.Build(ctx, s, nil, io.Discard, io.Discard)
		_ = e.Pull(ctx, s, "img", io.Discard, io.Discard)
		_, _ = e.Services(ctx, s)
		_, _ = e.Port(ctx, s, "web", 80)
		_, _ = e.Logs(ctx, s, "web", 1)
		_, _ = e.LogsSince(ctx, s, "web", time.Time{})
		_, _ = e.Status(ctx, s)
		_ = e.Pause(ctx, s, []string{"web"})
		_ = e.Unpause(ctx, s, []string{"web"})
		_ = e.Stop(ctx, s, []string{"web"})
		_ = e.WaitReady(ctx, s, "web", runtime.Probe{})
		_ = e.Down(ctx, s, io.Discard, io.Discard)
	}
	calls(runtime.Sandbox{Project: "c"})
	calls(runtime.Sandbox{Project: "p", Processes: true})
	for name, f := range map[string]*runtimetest.Fake{"c": compose, "p": processes} {
		methods := map[string]bool{}
		for _, c := range f.Calls() {
			if c.Project != name {
				t.Errorf("%s was asked about %s: %+v", name, c.Project, c)
			}
			methods[c.Method] = true
		}
		if len(methods) != 13 {
			t.Errorf("%s: %d methods, want 13: %v", name, len(methods), methods)
		}
	}
}
