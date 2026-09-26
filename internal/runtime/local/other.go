//go:build !unix

package local

import (
	"context"
	"io"
	"time"

	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/runtime"
)

// Runner runs processes on macOS and Linux only: it needs process
// groups and signals to stop what it started.
type Runner struct {
	Root       string
	Supervisor []string
}

var _ runtime.Runtime = Runner{}

var errUnsupported = errs.New("pit runs processes on macOS and Linux only")

// Supervise is not available here.
func Supervise(context.Context, string) error { return errUnsupported }

func (Runner) Up(context.Context, runtime.Sandbox, []string, io.Writer, io.Writer) error {
	return errUnsupported
}
func (Runner) Build(context.Context, runtime.Sandbox, []string, io.Writer, io.Writer) error {
	return errUnsupported
}
func (Runner) Pull(context.Context, runtime.Sandbox, string, io.Writer, io.Writer) error {
	return errUnsupported
}
func (Runner) Down(context.Context, runtime.Sandbox, io.Writer, io.Writer) error {
	return errUnsupported
}
func (Runner) Services(context.Context, runtime.Sandbox) ([]string, error) {
	return nil, errUnsupported
}
func (Runner) Port(context.Context, runtime.Sandbox, string, int) (string, error) {
	return "", errUnsupported
}
func (Runner) Logs(context.Context, runtime.Sandbox, string, int) ([]byte, error) {
	return nil, errUnsupported
}
func (Runner) LogsSince(context.Context, runtime.Sandbox, string, time.Time) ([]runtime.LogLine, error) {
	return nil, errUnsupported
}
func (Runner) Status(context.Context, runtime.Sandbox) ([]runtime.Status, error) {
	return nil, errUnsupported
}
func (Runner) Pause(context.Context, runtime.Sandbox, []string) error   { return errUnsupported }
func (Runner) Unpause(context.Context, runtime.Sandbox, []string) error { return errUnsupported }
func (Runner) Stop(context.Context, runtime.Sandbox, []string) error    { return errUnsupported }
func (Runner) WaitReady(context.Context, runtime.Sandbox, string, runtime.Probe) error {
	return errUnsupported
}
