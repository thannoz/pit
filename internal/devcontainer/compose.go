package devcontainer

import (
	"bytes"
	"encoding/json"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/thannoz/pit/internal/errs"
)

// Service is the name of the container a devcontainer.json without
// compose files describes, in the compose file pit writes for it.
const Service = "dev"

// LogFile is where the start command writes, inside the container. The
// container's own process shows it, so that it is in the container's
// logs like any service's output.
const LogFile = "/tmp/pit-start.log"

// Input is what translating needs besides the file.
type Input struct {
	// Path is the devcontainer.json, relative to the worktree.
	Path string
	// Worktree is where the pull request is checked out.
	Worktree string
	// Start is the command that starts the app, from .pit.yaml.
	Start string
}

// Setup is a devcontainer.json in the terms of a sandbox.
type Setup struct {
	// Service is the one worked in.
	Service string
	// Files are the devcontainer's own compose files, relative to the
	// worktree; the generated file goes after them.
	Files []string
	// Compose is the generated file: the container itself, or what
	// pit adds to the compose files' description of it.
	Compose []byte
	// Services are the ones to start, when the file names them.
	Services []string
	// Ports are the ones the file forwards or describes.
	Ports []int
	// Workdir, User and Env are where, as whom and with what the
	// lifecycle commands and the start command run.
	Workdir string
	User    string
	Env     map[string]string
	// Create run once, in a new container; Update each time the code
	// in it is new; Started each time it starts.
	Create, Update, Started Commands
	// Waits says the container does nothing but wait: nothing in the
	// file starts the app, and no start command does either.
	Waits bool
	// Ignored says what of the file pit does not do.
	Ignored []string

	// chosen says the file itself says who to run as, which the
	// image's metadata then does not change.
	chosen bool
}

// Translate says what a devcontainer.json describes.
func Translate(f *File, in Input) (Setup, error) {
	dir := path.Dir(filepath.ToSlash(in.Path))
	s := Setup{
		Service: f.Service,
		Ports:   f.Ports(),
		Workdir: f.WorkspaceFolder,
		User:    orElse(f.RemoteUser, f.ContainerUser),
		chosen:  f.RemoteUser != "" || f.ContainerUser != "",
		Env:     f.RemoteEnv,
		Create: join(phase(f.OnCreateCommand, "onCreateCommand"), phase(f.UpdateContentCommand, "updateContentCommand"),
			phase(f.PostCreateCommand, "postCreateCommand")),
		Update:  phase(f.UpdateContentCommand, "updateContentCommand"),
		Started: phase(f.PostStartCommand, "postStartCommand"),
	}
	svc := service{
		User:        esc(f.ContainerUser),
		Environment: escMap(f.ContainerEnv),
		Init:        f.Init,
		Privileged:  f.Privileged != nil && *f.Privileged,
		CapAdd:      f.CapAdd,
		SecurityOpt: f.SecurityOpt,
	}
	volumes := map[string]struct{}{}

	var override bool
	if f.Compose() {
		for _, c := range f.ComposeFiles {
			s.Files = append(s.Files, path.Clean(path.Join(dir, c)))
		}
		if len(f.RunServices) > 0 {
			s.Services = append([]string{f.Service}, without(f.RunServices, f.Service)...)
		}
		override = f.OverrideCommand != nil && *f.OverrideCommand
	} else {
		s.Service = Service
		override = f.OverrideCommand == nil || *f.OverrideCommand
		if f.Build != nil && f.Build.Dockerfile != "" {
			context := filepath.Join(in.Worktree, filepath.FromSlash(dir), filepath.FromSlash(f.Build.Context))
			svc.Build = &build{
				Context:    esc(context),
				Dockerfile: esc(filepath.Join(in.Worktree, filepath.FromSlash(dir), filepath.FromSlash(f.Build.Dockerfile))),
				Args:       escMap(f.Build.Args),
				Target:     esc(f.Build.Target),
			}
		} else {
			svc.Image = esc(f.Image)
		}
		svc.WorkingDir = esc(f.WorkspaceFolder)
		ws := Mount{Type: "bind", Source: in.Worktree, Target: f.WorkspaceFolder}
		if f.WorkspaceMount != "" {
			m, err := ParseMount(f.WorkspaceMount)
			if err != nil {
				return s, errs.Wrap(err, "workspaceMount")
			}
			ws = m
		}
		svc.Volumes = append(svc.Volumes, volumeOf(ws, volumes))
	}
	for _, m := range f.Mounts {
		svc.Volumes = append(svc.Volumes, volumeOf(m, volumes))
	}

	switch {
	case in.Start != "":
		svc.Entrypoint = []string{"/bin/sh", "-c", esc(showLog)}
	case override:
		svc.Entrypoint = []string{"/bin/sh", "-c", esc(wait)}
		s.Waits = true
	}
	// The command a compose file gives would follow the entrypoint as
	// its arguments.
	if svc.Entrypoint != nil {
		svc.Command = &reset{}
	}

	ignored := runArgs(f.RunArgs, &svc)
	if len(f.Features) > 0 {
		names := make([]string, 0, len(f.Features))
		for n := range f.Features {
			names = append(names, n)
		}
		sort.Strings(names)
		s.Ignored = append(s.Ignored, "features ("+strings.Join(names, ", ")+"): the container runs without them")
	}
	if len(f.InitializeCommand) > 0 {
		s.Ignored = append(s.Ignored, "initializeCommand: it would run on this machine, outside the container")
	}
	if len(f.PostAttachCommand) > 0 {
		s.Ignored = append(s.Ignored, "postAttachCommand: it runs when an editor attaches, and pit does not")
	}
	if len(ignored) > 0 {
		s.Ignored = append(s.Ignored, "runArgs "+strings.Join(ignored, " "))
	}

	out := composeFile{Services: map[string]service{s.Service: svc}}
	if len(volumes) > 0 {
		out.Volumes = volumes
	}
	var b bytes.Buffer
	b.WriteString("# Generated by pit from " + in.Path + ". Do not edit; it is rewritten\n# on every run and removed when the sandbox is torn down.\n")
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(out); err != nil {
		return s, errs.Wrap(err, "cannot write the compose file for %s", in.Path)
	}
	s.Compose = b.Bytes()
	return s, nil
}

// wait is what the container does when the file says to keep it
// running and nothing starts the app: the specification's own loop,
// ended at once by docker stop.
const wait = `trap "exit 0" TERM; while sleep 1000 & wait $!; do :; done`

// showLog keeps the container running and shows what the start command
// writes, as the container's own output.
const showLog = `: > ` + LogFile + `; chmod 666 ` + LogFile + ` 2>/dev/null; trap "exit 0" TERM; tail -f ` + LogFile + ` & wait $!`

type composeFile struct {
	Services map[string]service  `yaml:"services"`
	Volumes  map[string]struct{} `yaml:"volumes,omitempty"`
}

type service struct {
	Image       string            `yaml:"image,omitempty"`
	Build       *build            `yaml:"build,omitempty"`
	Entrypoint  []string          `yaml:"entrypoint,omitempty"`
	Command     *reset            `yaml:"command,omitempty"`
	User        string            `yaml:"user,omitempty"`
	WorkingDir  string            `yaml:"working_dir,omitempty"`
	Environment map[string]string `yaml:"environment,omitempty"`
	Volumes     []volume          `yaml:"volumes,omitempty"`
	Init        *bool             `yaml:"init,omitempty"`
	Privileged  bool              `yaml:"privileged,omitempty"`
	CapAdd      []string          `yaml:"cap_add,omitempty"`
	SecurityOpt []string          `yaml:"security_opt,omitempty"`
}

// reset is Compose's `!reset []`: nothing, whatever an earlier file
// said.
type reset struct{}

func (reset) MarshalYAML() (any, error) {
	return &yaml.Node{Kind: yaml.SequenceNode, Tag: "!reset", Style: yaml.FlowStyle}, nil
}

func (*reset) UnmarshalYAML(*yaml.Node) error { return nil }

type build struct {
	Context    string            `yaml:"context"`
	Dockerfile string            `yaml:"dockerfile,omitempty"`
	Args       map[string]string `yaml:"args,omitempty"`
	Target     string            `yaml:"target,omitempty"`
}

type volume struct {
	Type     string `yaml:"type"`
	Source   string `yaml:"source,omitempty"`
	Target   string `yaml:"target"`
	ReadOnly bool   `yaml:"read_only,omitempty"`
}

// volumeOf is a mount in Compose's terms. A named volume is declared,
// which makes it the sandbox's own: Compose puts the project's name in
// front of it.
func volumeOf(m Mount, named map[string]struct{}) volume {
	v := volume{Type: m.Type, Source: esc(m.Source), Target: esc(m.Target), ReadOnly: m.ReadOnly}
	if m.Type == "volume" && m.Source != "" {
		named[esc(m.Source)] = struct{}{}
	}
	return v
}

// runArgs takes what of docker run's options Compose can say, and
// returns the rest.
func runArgs(args []string, s *service) (ignored []string) {
	for i := 0; i < len(args); i++ {
		name, value, inline := strings.Cut(args[i], "=")
		next := func() string {
			if inline {
				return value
			}
			if i+1 < len(args) {
				i++
				return args[i]
			}
			return ""
		}
		switch name {
		case "--cap-add":
			s.CapAdd = append(s.CapAdd, next())
		case "--security-opt":
			s.SecurityOpt = append(s.SecurityOpt, next())
		case "--privileged":
			s.Privileged = true
		case "--init":
			yes := true
			s.Init = &yes
		case "-e", "--env":
			k, v, _ := strings.Cut(next(), "=")
			if s.Environment == nil {
				s.Environment = map[string]string{}
			}
			s.Environment[k] = esc(v)
		case "-u", "--user":
			s.User = esc(next())
		default:
			ignored = append(ignored, args[i])
		}
	}
	return ignored
}

// esc keeps Compose from reading a "$" as its own variable: the file's
// variables are filled in already, and what is left is the shell's.
func esc(s string) string { return strings.ReplaceAll(s, "$", "$$") }

func escMap(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = esc(v)
	}
	return out
}

func phase(c Commands, name string) Commands {
	out := make(Commands, len(c))
	for i, cmd := range c {
		cmd.Phase = name
		out[i] = cmd
	}
	return out
}

func join(lists ...Commands) Commands {
	var out Commands
	for _, l := range lists {
		out = append(out, l...)
	}
	return out
}

func without(list []string, name string) []string {
	var out []string
	for _, s := range list {
		if s != name {
			out = append(out, s)
		}
	}
	return out
}

func orElse(s, fallback string) string {
	if s != "" {
		return s
	}
	return fallback
}

// Marker is a file a container has once its create commands ran: a
// container without it is new, whatever pit thinks it did.
const Marker = "/tmp/.pit-created"

// Exec is what `docker compose exec` is given to run a lifecycle
// command: as the file's user, in its workspace, with its environment.
func (s Setup) Exec(c Command) []string {
	if c.Shell != "" {
		return s.exec(false, s.exports()+c.Shell)
	}
	return s.exec(false, s.exports()+`exec "$@"`, append([]string{"sh"}, c.Args...)...)
}

// StartExec is what runs the start command, left running, with what it
// writes where the container shows it.
func (s Setup) StartExec(start string) []string {
	return s.exec(true, s.exports()+"exec >>"+LogFile+" 2>&1; "+start)
}

func (s Setup) exec(detach bool, script string, args ...string) []string {
	out := []string{"exec", "-T"}
	if detach {
		out = append(out, "-d")
	}
	if s.User != "" {
		out = append(out, "-u", s.User)
	}
	if s.Workdir != "" {
		out = append(out, "-w", s.Workdir)
	}
	out = append(out, s.Service, "/bin/sh", "-c", script)
	return append(out, args...)
}

// exports sets remoteEnv for a shell in the container, where a
// ${NAME} of the container's own environment is filled in.
func (s Setup) exports() string {
	keys := make([]string, 0, len(s.Env))
	for k := range s.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		v := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "`", "\\`").Replace(s.Env[k])
		b.WriteString("export " + k + `="` + v + `"; `)
	}
	return b.String()
}

// Adopt takes what an image says of itself, in its devcontainer.metadata
// label: the features it was built with and its author set a user to
// work as, an environment, commands of their own. The file's own say
// comes last, as the specification merges them.
func (s *Setup) Adopt(label string) error {
	label = strings.TrimSpace(label)
	if label == "" || label == "null" {
		return nil
	}
	type entry struct {
		RemoteUser           string            `json:"remoteUser"`
		ContainerUser        string            `json:"containerUser"`
		RemoteEnv            map[string]string `json:"remoteEnv"`
		OnCreateCommand      Commands          `json:"onCreateCommand"`
		UpdateContentCommand Commands          `json:"updateContentCommand"`
		PostCreateCommand    Commands          `json:"postCreateCommand"`
		PostStartCommand     Commands          `json:"postStartCommand"`
	}
	var entries []entry
	if err := json.Unmarshal([]byte(label), &entries); err != nil {
		var one entry
		if json.Unmarshal([]byte(label), &one) != nil {
			return errs.Wrap(err, "cannot read the image's devcontainer.metadata")
		}
		entries = []entry{one}
	}
	var create, update, started Commands
	env := map[string]string{}
	user := ""
	for _, e := range entries {
		if u := orElse(e.RemoteUser, e.ContainerUser); u != "" {
			user = u
		}
		for k, v := range e.RemoteEnv {
			env[k] = v
		}
		create = join(create, phase(e.OnCreateCommand, "onCreateCommand"), phase(e.UpdateContentCommand, "updateContentCommand"),
			phase(e.PostCreateCommand, "postCreateCommand"))
		update = join(update, phase(e.UpdateContentCommand, "updateContentCommand"))
		started = join(started, phase(e.PostStartCommand, "postStartCommand"))
	}
	if !s.chosen && user != "" {
		s.User = user
	}
	for k, v := range s.Env {
		env[k] = v
	}
	if len(env) > 0 {
		s.Env = env
	}
	s.Create = join(create, s.Create)
	s.Update = join(update, s.Update)
	s.Started = join(started, s.Started)
	return nil
}
