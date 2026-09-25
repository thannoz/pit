// Package devcontainer reads a devcontainer.json -- the file editors and
// Codespaces bring a project's development environment up from -- and
// says what it describes in the terms pit runs a sandbox in: Compose.
//
// A project that has one has already written down how it runs: which
// image, which ports, what to install once the container is there. pit
// reads that rather than asking for a compose file nobody would keep
// up to date beside it.
package devcontainer

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/thannoz/pit/internal/errs"
)

// Places are where the specification looks for the file, in its order.
// A folder of several below .devcontainer is found by Find as well.
var Places = []string{".devcontainer/devcontainer.json", ".devcontainer.json"}

// Find is the devcontainer.json of the project in dir, relative to it.
func Find(dir string) (string, bool) {
	for _, p := range Places {
		if isFile(filepath.Join(dir, p)) {
			return p, true
		}
	}
	// One configuration of several: .devcontainer/<name>/devcontainer.json.
	matches, _ := filepath.Glob(filepath.Join(dir, ".devcontainer", "*", "devcontainer.json"))
	sort.Strings(matches)
	for _, m := range matches {
		if rel, err := filepath.Rel(dir, m); err == nil {
			return filepath.ToSlash(rel), true
		}
	}
	return "", false
}

func isFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// File is what pit reads of a devcontainer.json. The rest -- an editor's
// customizations, host requirements -- is for the tools that open it.
type File struct {
	Name string `json:"name"`
	// Image is what the container runs, unless Build builds it.
	Image string `json:"image"`
	Build *Build `json:"build"`
	// ComposeFiles, relative to the file, describe the container and
	// what it runs with; Service is the one worked in.
	ComposeFiles Strings  `json:"dockerComposeFile"`
	Service      string   `json:"service"`
	RunServices  []string `json:"runServices"`

	WorkspaceFolder string  `json:"workspaceFolder"`
	WorkspaceMount  string  `json:"workspaceMount"`
	Mounts          []Mount `json:"-"`

	ForwardPorts    []Port                     `json:"-"`
	AppPort         []Port                     `json:"-"`
	PortsAttributes map[string]json.RawMessage `json:"portsAttributes"`

	ContainerEnv  map[string]string `json:"containerEnv"`
	RemoteEnv     map[string]string `json:"remoteEnv"`
	ContainerUser string            `json:"containerUser"`
	RemoteUser    string            `json:"remoteUser"`

	RunArgs         []string `json:"runArgs"`
	Init            *bool    `json:"init"`
	Privileged      *bool    `json:"privileged"`
	CapAdd          []string `json:"capAdd"`
	SecurityOpt     []string `json:"securityOpt"`
	OverrideCommand *bool    `json:"overrideCommand"`

	InitializeCommand    Commands `json:"initializeCommand"`
	OnCreateCommand      Commands `json:"onCreateCommand"`
	UpdateContentCommand Commands `json:"updateContentCommand"`
	PostCreateCommand    Commands `json:"postCreateCommand"`
	PostStartCommand     Commands `json:"postStartCommand"`
	PostAttachCommand    Commands `json:"postAttachCommand"`

	Features map[string]json.RawMessage `json:"features"`
}

// Build is how the container's image is built.
type Build struct {
	// Dockerfile and Context are relative to the devcontainer.json.
	Dockerfile string            `json:"dockerfile"`
	Context    string            `json:"context"`
	Args       map[string]string `json:"args"`
	Target     string            `json:"target"`
}

// Compose says the file describes its container with compose files.
func (f *File) Compose() bool { return len(f.ComposeFiles) > 0 }

// Strings is a string or a list of them.
type Strings []string

// UnmarshalJSON reads either form.
func (s *Strings) UnmarshalJSON(data []byte) error {
	var one string
	if json.Unmarshal(data, &one) == nil {
		*s = Strings{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(data, &many); err != nil {
		return errors.New("is neither a string nor a list of them")
	}
	*s = many
	return nil
}

// Port is a forwarded port: 3000, or "db:5432" for another service's.
type Port struct {
	Service string
	Number  int
}

// UnmarshalJSON reads either form.
func (p *Port) UnmarshalJSON(data []byte) error {
	var n int
	if json.Unmarshal(data, &n) == nil {
		*p = Port{Number: n}
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return errors.New("is neither a number nor a string")
	}
	host, port, found := strings.Cut(s, ":")
	if !found {
		host, port = "", s
	}
	n, err := strconv.Atoi(port)
	if err != nil {
		return errors.New("names no port: " + s)
	}
	*p = Port{Service: host, Number: n}
	return nil
}

// ports reads forwardPorts and appPort, which take one or a list.
func ports(raw json.RawMessage) ([]Port, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var many []Port
	if json.Unmarshal(raw, &many) == nil {
		return many, nil
	}
	var one Port
	if err := json.Unmarshal(raw, &one); err != nil {
		return nil, err
	}
	return []Port{one}, nil
}

// Mount is a volume or a directory of the machine in the container.
type Mount struct {
	Type   string
	Source string
	Target string
	// ReadOnly is the mount's "readonly" or "ro".
	ReadOnly bool
}

// UnmarshalJSON reads Docker's spelling and the object.
func (m *Mount) UnmarshalJSON(data []byte) error {
	var s string
	if json.Unmarshal(data, &s) == nil {
		parsed, err := ParseMount(s)
		*m = parsed
		return err
	}
	var o struct {
		Type   string `json:"type"`
		Source string `json:"source"`
		Target string `json:"target"`
	}
	if err := json.Unmarshal(data, &o); err != nil {
		return errors.New("is neither a string nor a mount")
	}
	*m = Mount{Type: o.Type, Source: o.Source, Target: o.Target}
	return nil
}

// ParseMount reads Docker's --mount spelling:
// "source=cache,target=/root/.cache,type=volume".
func ParseMount(s string) (Mount, error) {
	var m Mount
	for _, part := range strings.Split(s, ",") {
		key, value, _ := strings.Cut(strings.TrimSpace(part), "=")
		switch strings.ToLower(key) {
		case "type":
			m.Type = value
		case "source", "src":
			m.Source = value
		case "target", "destination", "dst":
			m.Target = value
		case "readonly", "ro":
			m.ReadOnly = value == "" || value == "true" || value == "1"
		}
	}
	if m.Target == "" {
		return m, errors.New("has no target: " + s)
	}
	if m.Type == "" {
		m.Type = "volume"
	}
	return m, nil
}

// Command is one lifecycle command: a line for a shell, or the words of
// one run without a shell.
type Command struct {
	// Phase is the setting it is from: postCreateCommand.
	Phase string
	// Name is its name in the parallel form, empty otherwise.
	Name  string
	Shell string
	Args  []string
}

func (c Command) String() string {
	if c.Shell != "" {
		return c.Shell
	}
	return strings.Join(c.Args, " ")
}

// Commands is a lifecycle command in any of its three forms: a string,
// a list of words, or an object of commands run side by side -- which
// pit runs one after the other, by name.
type Commands []Command

// UnmarshalJSON reads any of the three forms.
func (c *Commands) UnmarshalJSON(data []byte) error {
	one, err := command(data)
	if err == nil {
		if one.Shell != "" || len(one.Args) > 0 {
			*c = Commands{one}
		}
		return nil
	}
	var named map[string]json.RawMessage
	if err := json.Unmarshal(data, &named); err != nil {
		return errors.New("is neither a command nor an object of them")
	}
	names := make([]string, 0, len(named))
	for n := range named {
		names = append(names, n)
	}
	sort.Strings(names)
	out := Commands{}
	for _, n := range names {
		cmd, err := command(named[n])
		if err != nil {
			return errors.New(n + " is not a command")
		}
		if cmd.Shell == "" && len(cmd.Args) == 0 {
			continue
		}
		cmd.Name = n
		out = append(out, cmd)
	}
	*c = out
	return nil
}

func command(data []byte) (Command, error) {
	var s string
	if json.Unmarshal(data, &s) == nil {
		return Command{Shell: strings.TrimSpace(s)}, nil
	}
	var args []string
	if err := json.Unmarshal(data, &args); err != nil {
		return Command{}, err
	}
	return Command{Args: args}, nil
}

// Vars are what the file's ${...} variables stand for.
type Vars struct {
	// Workspace is the directory on this machine: the worktree.
	Workspace string
	// Basename stands for the workspace's name; the repository's, not
	// the worktree directory's, which is pit's.
	Basename string
	// ID stands for ${devcontainerId}: one per sandbox.
	ID string
	// Env reads this machine's environment.
	Env func(string) string
}

// Parse reads a devcontainer.json, with its variables filled in.
func Parse(data []byte, v Vars) (*File, error) {
	clean := standard(data)
	var tree any
	if err := json.Unmarshal(clean, &tree); err != nil {
		return nil, describe(err, clean)
	}
	top, ok := tree.(map[string]any)
	if !ok {
		return nil, errs.New("is not an object")
	}

	// The container's workspace first: the other variables can name it.
	folder, _ := top["workspaceFolder"].(string)
	folder = v.expand(folder, "")
	if folder == "" {
		if _, compose := top["dockerComposeFile"]; compose {
			folder = "/"
		} else {
			folder = "/workspaces/" + v.Basename
		}
	}
	top["workspaceFolder"] = folder
	tree = walk(tree, func(s string) string { return v.expand(s, folder) })

	again, err := json.Marshal(tree)
	if err != nil {
		return nil, err
	}
	var f File
	var odd struct {
		ForwardPorts json.RawMessage   `json:"forwardPorts"`
		AppPort      json.RawMessage   `json:"appPort"`
		Mounts       []json.RawMessage `json:"mounts"`
		DockerFile   string            `json:"dockerFile"`
		Context      string            `json:"context"`
	}
	if err := json.Unmarshal(again, &f); err != nil {
		return nil, culprit(top, err)
	}
	if err := json.Unmarshal(again, &odd); err != nil {
		return nil, describe(err, nil)
	}
	if f.ForwardPorts, err = ports(odd.ForwardPorts); err != nil {
		return nil, errs.Wrap(err, "forwardPorts")
	}
	if f.AppPort, err = ports(odd.AppPort); err != nil {
		return nil, errs.Wrap(err, "appPort")
	}
	for i, raw := range odd.Mounts {
		var m Mount
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, errs.Wrap(err, "mounts[%d]", i)
		}
		f.Mounts = append(f.Mounts, m)
	}
	// The older spelling, from before build had an object of its own.
	if f.Build == nil && odd.DockerFile != "" {
		f.Build = &Build{Dockerfile: odd.DockerFile, Context: odd.Context}
	}
	if f.Build != nil && f.Build.Context == "" {
		f.Build.Context = "."
	}

	switch {
	case f.Compose() && f.Service == "":
		return nil, errs.New("names compose files but no service to work in")
	case !f.Compose() && f.Image == "" && (f.Build == nil || f.Build.Dockerfile == ""):
		return nil, errs.New("says neither which image to run nor how to build one").
			WithHint("a devcontainer.json has an image, a build.dockerfile or a dockerComposeFile")
	}
	return &f, nil
}

// Load reads the devcontainer.json at path.
func Load(path string, v Vars) (*File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, errs.Wrap(err, "cannot read %s", path)
	}
	f, err := Parse(data, v)
	if err != nil {
		return nil, errs.Wrap(err, "cannot use %s", path)
	}
	return f, nil
}

func describe(err error, data []byte) error {
	var syntax *json.SyntaxError
	if errors.As(err, &syntax) && data != nil {
		line := 1 + bytes.Count(data[:min(int(syntax.Offset), len(data))], []byte("\n"))
		return errs.Wrap(err, "line %d", line)
	}
	return err
}

// culprit names the setting a decoding error is about: the errors of
// the settings that take several forms do not know their names.
func culprit(top map[string]any, err error) error {
	keys := make([]string, 0, len(top))
	for k := range top {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		one, _ := json.Marshal(map[string]any{k: top[k]})
		var f File
		if e := json.Unmarshal(one, &f); e != nil {
			return errs.Wrap(e, "%s", k)
		}
	}
	return err
}

// walk replaces every string in a decoded tree.
func walk(v any, f func(string) string) any {
	switch t := v.(type) {
	case string:
		return f(t)
	case []any:
		for i := range t {
			t[i] = walk(t[i], f)
		}
	case map[string]any:
		for k := range t {
			t[k] = walk(t[k], f)
		}
	}
	return v
}

// expand fills in the variables of one string. ${containerEnv:NAME} is
// the container's, which only a shell in it knows: it becomes ${NAME}.
func (v Vars) expand(s, folder string) string {
	if !strings.Contains(s, "${") {
		return s
	}
	var b strings.Builder
	for {
		start := strings.Index(s, "${")
		if start < 0 {
			b.WriteString(s)
			return b.String()
		}
		end := strings.IndexByte(s[start:], '}')
		if end < 0 {
			b.WriteString(s)
			return b.String()
		}
		b.WriteString(s[:start])
		b.WriteString(v.value(s[start+2:start+end], folder, s[start:start+end+1]))
		s = s[start+end+1:]
	}
}

func (v Vars) value(name, folder, written string) string {
	kind, rest, _ := strings.Cut(name, ":")
	switch kind {
	case "localWorkspaceFolder":
		return v.Workspace
	case "localWorkspaceFolderBasename":
		return v.Basename
	case "containerWorkspaceFolder":
		return folder
	case "containerWorkspaceFolderBasename":
		return filepath.Base(folder)
	case "devcontainerId":
		return v.ID
	case "localEnv", "env":
		key, fallback, _ := strings.Cut(rest, ":")
		value := ""
		if v.Env != nil {
			value = v.Env(key)
		}
		if value == "" {
			return fallback
		}
		return value
	case "containerEnv":
		key, _, _ := strings.Cut(rest, ":")
		return "${" + key + "}"
	}
	return written
}

// Ports are those of the service worked in, forwarded ones first: they are what
// the project says to open.
func (f *File) Ports() []int {
	var out []int
	add := func(n int) {
		if n > 0 && !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	for _, list := range [][]Port{f.ForwardPorts, f.AppPort} {
		for _, p := range list {
			if p.Service == "" || p.Service == f.Service {
				add(p.Number)
			}
		}
	}
	// The labels of ports the file only describes: a template's way of
	// saying which port the app listens on without forwarding it.
	var described []int
	for key := range f.PortsAttributes {
		if n, err := strconv.Atoi(key); err == nil {
			described = append(described, n)
		}
	}
	sort.Ints(described)
	for _, n := range described {
		add(n)
	}
	return out
}
