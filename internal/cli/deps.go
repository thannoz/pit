package cli

import (
	"context"
	"os"
	"path/filepath"

	"github.com/thannoz/pit/internal/data"
	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/proc"
	"github.com/thannoz/pit/internal/runtime"
	"github.com/thannoz/pit/internal/runtime/kube"
	"github.com/thannoz/pit/internal/runtime/local"
	"github.com/thannoz/pit/internal/sandbox"
	"github.com/thannoz/pit/internal/state"
	"github.com/thannoz/pit/internal/workspace"
)

// manager builds the sandbox manager the commands work through.
//
// It is a function rather than a field so that a test can replace it
// wholesale: every command below goes through here, and nothing in
// this package reaches for Docker or git directly.
var manager = realManager

// currentRepo answers which repository the command was run in. It is a
// variable for the same reason as manager: so a test can say so
// without building a git repository first.
var currentRepo = realCurrentRepo

func realCurrentRepo(ctx context.Context) (workspace.Repo, error) {
	repo, err := workspace.Discover(ctx, proc.Exec{}, ".")
	if err != nil {
		// The hint used to name `pit down --all`, which is nonsense for
		// `pit open` or `pit logs`. Every command goes through here, so
		// the advice has to fit all of them.
		return workspace.Repo{}, errs.Wrap(err, "cannot tell which repository this is").
			WithHint("a pull request number means nothing without its repository; run this from inside one (`pit ls` works anywhere)")
	}
	return repo, nil
}

func realManager() (*sandbox.Manager, error) {
	dir, err := workspace.StateDir()
	if err != nil {
		return nil, err
	}
	store, err := state.Open(dir)
	if err != nil {
		return nil, err
	}

	self, err := os.Executable()
	if err != nil {
		return nil, errs.Wrap(err, "cannot tell where pit is")
	}
	x := proc.Exec{}
	return &sandbox.Manager{
		Store: store,
		Runtime: runtime.Either{
			Compose:   runtime.Compose{Runner: x},
			Processes: local.Runner{Root: filepath.Join(dir, "processes"), Supervisor: []string{self, superviseCommand}},
			// A sandbox in Kubernetes forwards its port with a process
			// of its own, supervised like a Procfile's.
			Kubernetes: kube.Runtime{Runner: x, Forward: local.Runner{Root: filepath.Join(dir, "kube", "forward"), Supervisor: []string{self, superviseCommand}}},
		},
		Git:      x,
		Proc:     x,
		Data:     data.Commands{Runner: x},
		StateDir: dir,
	}, nil
}
