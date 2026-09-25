package sandbox

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/thannoz/pit/internal/config"
	"github.com/thannoz/pit/internal/devcontainer"
	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/proc"
	"github.com/thannoz/pit/internal/runtime"
)

// fromDevcontainer reads the devcontainer.json the configuration names,
// in the pull request's worktree, writes the compose file it becomes
// to generated, and returns the configuration that uses it.
func fromDevcontainer(req UpRequest, worktree, project, generated string, rep Reporter) (*config.Config, *devcontainer.Setup, error) {
	c := *req.Config
	file := c.Devcontainer.File
	f, err := devcontainer.Load(filepath.Join(worktree, file), devcontainer.Vars{
		Workspace: worktree,
		Basename:  filepath.Base(req.Repo.Root),
		ID:        project,
		Env:       os.Getenv,
	})
	if err != nil {
		return nil, nil, errs.Wrap(err, "#%d's dev container cannot be brought up", req.PR.Number)
	}
	setup, err := devcontainer.Translate(f, devcontainer.Input{Path: file, Worktree: worktree, Start: c.Devcontainer.Start})
	if err != nil {
		return nil, nil, err
	}
	if err := os.MkdirAll(filepath.Dir(generated), 0o755); err != nil {
		return nil, nil, errs.Wrap(err, "cannot create %s", filepath.Dir(generated))
	}
	if err := os.WriteFile(generated, setup.Compose, 0o600); err != nil {
		return nil, nil, errs.Wrap(err, "cannot write %s", generated)
	}
	for _, what := range setup.Ignored {
		rep.Note("pit leaves out the %s's %s", file, what)
	}

	c.Compose.Files = append(append([]string{}, setup.Files...), generated)
	if len(c.Compose.Services) == 0 {
		c.Compose.Services = setup.Services
	}
	return &c, &setup, nil
}

// devLifecycle runs a dev container's lifecycle commands, and then what
// starts the app. A new container gets the commands that set it up;
// one that already ran them is started over, so that what runs in it
// is the new commit's, and gets those for new code.
func (m *Manager) devLifecycle(ctx context.Context, box runtime.Sandbox, d *devcontainer.Setup, start string, rep Reporter) (string, error) {
	run := func(args []string, stdout, stderr io.Writer) error {
		return m.Proc.Stream(ctx, runtime.ComposeCommand(box, args...), stdout, stderr)
	}
	if err := m.adoptMetadata(ctx, box, d); err != nil {
		return "", err
	}
	fresh := run([]string{"exec", "-T", d.Service, "test", "-e", devcontainer.Marker}, io.Discard, io.Discard) != nil

	cmds := d.Update
	if fresh {
		cmds = d.Create
	} else if err := run([]string{"restart", d.Service}, rep.Stdout(), rep.Stderr()); err != nil {
		return "", errs.Wrap(err, "cannot start %s over", d.Service)
	}
	cmds = append(append(devcontainer.Commands{}, cmds...), d.Started...)

	for _, c := range cmds {
		if err := run(d.Exec(c), rep.Stdout(), rep.Stderr()); err != nil {
			what := c.Phase
			if c.Name != "" {
				what += " " + c.Name
			}
			return "", errs.Wrap(err, "the dev container's %s failed", what).
				WithHint("it ran %s", c)
		}
	}
	if fresh {
		if err := run([]string{"exec", "-T", d.Service, "touch", devcontainer.Marker}, io.Discard, rep.Stderr()); err != nil {
			return "", errs.Wrap(err, "cannot mark %s as set up", d.Service)
		}
	}

	var said []string
	if len(cmds) > 0 {
		said = append(said, plural(len(cmds), "command", "commands"))
	}
	switch {
	case start != "":
		if err := run(d.StartExec(start), io.Discard, rep.Stderr()); err != nil {
			return "", errs.Wrap(err, "cannot start the app in %s", d.Service).
				WithHint("devcontainer.start in %s is what ran: %s", config.FileName, start)
		}
		said = append(said, "started: "+start)
	case d.Waits:
		rep.Note("the dev container only waits: nothing in it starts the app; devcontainer.start in %s says how", config.FileName)
	}
	if len(said) == 0 {
		return "nothing to run", nil
	}
	return strings.Join(said, ", "), nil
}

// adoptMetadata reads what the dev container's image says of itself.
func (m *Manager) adoptMetadata(ctx context.Context, box runtime.Sandbox, d *devcontainer.Setup) error {
	var id, label bytes.Buffer
	if err := m.Proc.Stream(ctx, runtime.ComposeCommand(box, "ps", "-q", d.Service), &id, io.Discard); err != nil {
		return errs.Wrap(err, "cannot find the container of %s", d.Service)
	}
	container := strings.TrimSpace(id.String())
	if container == "" {
		return errs.New("%s has no container", d.Service).
			WithHint("`pit logs` shows why it stopped")
	}
	read := proc.Command{Name: "docker", Args: []string{"inspect", "--format", `{{index .Config.Labels "devcontainer.metadata"}}`, container}}
	if err := m.Proc.Stream(ctx, read, &label, io.Discard); err != nil {
		return errs.Wrap(err, "cannot read the image of %s", d.Service)
	}
	if l := strings.TrimSpace(label.String()); l != "<no value>" {
		return d.Adopt(l)
	}
	return nil
}
