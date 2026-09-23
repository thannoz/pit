package runtime_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"slices"
	"testing"
	"time"

	"github.com/thannoz/pit/internal/runtime"
	"github.com/thannoz/pit/internal/runtime/runtimetest"
)

// bringUp is the sequence pit performs for a review, written against
// the interface alone. When the real command exists it does this; here
// it stands in for it, which is what makes the Fake worth having.
func bringUp(ctx context.Context, rt runtime.Runtime, s runtime.Sandbox, out io.Writer) (string, error) {
	services, err := rt.Services(ctx, s)
	if err != nil {
		return "", err
	}
	if len(services) == 0 {
		return "", errors.New("the compose files declare no services")
	}

	if err := rt.Up(ctx, s, out, out); err != nil {
		return "", err
	}

	web := services[0]
	probe := runtime.Probe{URL: "http://localhost/", ExpectStatus: 200, Timeout: time.Second, Interval: time.Millisecond}
	if err := rt.WaitReady(ctx, s, web, probe); err != nil {
		return "", err
	}
	return rt.Port(ctx, s, web, 80)
}

func sandbox() runtime.Sandbox {
	return runtime.Sandbox{
		Project: "pit-acme-shop-c56680-482",
		Dir:     "/state/pit/acme-shop-c56680/pr-482",
		Files:   []string{"docker-compose.yml"},
	}
}

// TestFullSandboxAgainstTheFake is the acceptance criterion for T-304.
func TestFullSandboxAgainstTheFake(t *testing.T) {
	f := runtimetest.New("web", "api", "db")
	f.Published["web:80"] = 49580

	var out bytes.Buffer
	port, err := bringUp(t.Context(), f, sandbox(), &out)
	if err != nil {
		t.Fatalf("bringUp: %v", err)
	}

	if port != "49580" {
		t.Errorf("port = %q, want 49580", port)
	}
	if !f.IsUp(sandbox().Project) {
		t.Error("the sandbox is not running after bringUp")
	}
	if !bytes.Contains(out.Bytes(), []byte("Started")) {
		t.Errorf("the runtime's output was not forwarded: %q", out.String())
	}

	want := []string{"Services", "Up", "WaitReady", "Port"}
	if got := f.Methods(); !slices.Equal(got, want) {
		t.Errorf("the sequence was %v, want %v", got, want)
	}

	// And the sandbox can be reported on and taken down again.
	statuses, err := f.Status(t.Context(), sandbox())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	for _, s := range statuses {
		if !s.Running() {
			t.Errorf("%s is %q, want it running", s.Service, s.State)
		}
	}

	if err := f.Down(t.Context(), sandbox(), io.Discard, io.Discard); err != nil {
		t.Fatalf("Down: %v", err)
	}
	if f.IsUp(sandbox().Project) {
		t.Error("the sandbox is still running after Down")
	}
}

// TestSandboxThatNeverBecomesReady is why the Fake models failure: a
// test that only ever sees success proves very little.
func TestSandboxThatNeverBecomesReady(t *testing.T) {
	f := runtimetest.New()
	f.ReadyAfter = 99 // never, within one bringUp

	if _, err := bringUp(t.Context(), f, sandbox(), io.Discard); err == nil {
		t.Fatal("want an error when the service never becomes ready")
	}
	// It was started, so cleanup still has something to do.
	if !f.IsUp(sandbox().Project) {
		t.Error("the sandbox was not started at all")
	}
}

func TestPortIsUnavailableBeforeUp(t *testing.T) {
	// Docker answers nothing for a service that is not running, and
	// code above must not assume otherwise.
	f := runtimetest.New()

	if _, err := f.Port(t.Context(), sandbox(), "web", 80); err == nil {
		t.Error("Port answered although the sandbox was never started")
	}
}

func TestStatusAfterTheServicesStop(t *testing.T) {
	f := runtimetest.New("web", "db")
	if err := f.Up(t.Context(), sandbox(), io.Discard, io.Discard); err != nil {
		t.Fatalf("Up: %v", err)
	}
	f.Stop(sandbox().Project)

	statuses, err := f.Status(t.Context(), sandbox())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(statuses) != 2 {
		t.Fatalf("got %d statuses, want the containers that are still there", len(statuses))
	}
	for _, s := range statuses {
		if s.Running() {
			t.Errorf("%s reports as running after it stopped", s.Service)
		}
	}
}

func TestStatusAfterTheContainersAreRemoved(t *testing.T) {
	// A down removes the containers, so compose reports nothing at
	// all -- which is a different answer from "they exited", and the
	// difference decides whether anything is left to keep.
	f := runtimetest.New("web", "db")
	if err := f.Up(t.Context(), sandbox(), io.Discard, io.Discard); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if err := f.Down(t.Context(), sandbox(), io.Discard, io.Discard); err != nil {
		t.Fatalf("Down: %v", err)
	}

	statuses, err := f.Status(t.Context(), sandbox())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(statuses) != 0 {
		t.Errorf("got %d statuses, want none for containers that no longer exist", len(statuses))
	}
}

func TestScriptedFailureReachesTheCaller(t *testing.T) {
	boom := errors.New("the daemon is not running")
	f := runtimetest.New()
	f.Fail["Up"] = boom

	_, err := bringUp(t.Context(), f, sandbox(), io.Discard)
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want the scripted failure", err)
	}
}
