package runtime

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"text/template"

	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/secrets"
)

// Override is the generated compose file that makes a sandbox its own:
// the port it is reachable on, and the environment it runs with.
type Override struct {
	// Service is the compose service to publish.
	Service string
	// HostPort is the port pit assigned for this sandbox.
	HostPort int
	// ContainerPort is the port the service listens on inside.
	ContainerPort int
	// EnvFile is an absolute path to a file of variables, or empty.
	EnvFile string
	// Env are individual variables to set on the service.
	Env map[string]string
	// Secrets are variables of the service whose values Compose reads
	// from its own environment as it starts it: they are never in the
	// file.
	Secrets []string
	// Images names an image for a service, replacing whatever its
	// build would have produced. It is how a sandbox uses what a
	// pipeline has already built.
	Images map[string]string
	// Project is the sandbox's Compose project, which names its
	// containers.
	Project string
	// Renamed are services the compose file gives a fixed container
	// name: they get one in the project instead, or a second sandbox
	// of the same repository could not start.
	Renamed []string
	// Unpublished are services other than Service that bind a port on
	// the host: the binding goes, for the same reason. Inside the
	// project's network they are reached as before.
	Unpublished []string
}

// RenderOverride produces the override file's contents.
//
// The `!override` tag on ports is the point of the whole file. Compose
// merges sequences by appending, so without it the base file's own
// published ports would still be bound -- and two sandboxes of the same
// project would fight over them. Replacing the list means pit's port is
// the only one.
func RenderOverride(o Override) ([]byte, error) {
	if o.Service == "" {
		return nil, errs.New("the override needs a service to publish")
	}
	if o.HostPort <= 0 || o.ContainerPort <= 0 {
		return nil, errs.New("the override needs both a host and a container port, got %d and %d",
			o.HostPort, o.ContainerPort)
	}
	if o.EnvFile != "" && !filepath.IsAbs(o.EnvFile) {
		// Compose resolves a relative env_file against the file that
		// declares it, and this file lives outside the worktree.
		return nil, errs.New("the env file path %q must be absolute", o.EnvFile)
	}

	var b bytes.Buffer
	if err := overrideTemplate.Execute(&b, overrideView{Services: blocks(o), SecretPrefix: secrets.EnvPrefix}); err != nil {
		return nil, errs.Wrap(err, "cannot write the compose override")
	}
	return b.Bytes(), nil
}

// blocks turns the override into one entry per service: the published
// one first, because it is the one a reader is looking for, then
// whatever else has an image of its own.
func blocks(o Override) []serviceBlock {
	web := serviceBlock{
		Name:    o.Service,
		Ports:   strconv.Itoa(o.HostPort) + ":" + strconv.Itoa(o.ContainerPort),
		EnvFile: o.EnvFile,
		EnvKeys: sortedKeys(o.Env),
		Env:     o.Env,
		Secrets: slices.Sorted(slices.Values(o.Secrets)),
		Image:   o.Images[o.Service],
	}

	out := []serviceBlock{web}
	index := map[string]int{o.Service: 0}
	block := func(name string) *serviceBlock {
		if i, ok := index[name]; ok {
			return &out[i]
		}
		index[name] = len(out)
		out = append(out, serviceBlock{Name: name})
		return &out[len(out)-1]
	}
	for _, name := range sortedKeys(o.Images) {
		block(name).Image = o.Images[name]
	}
	for _, name := range o.Renamed {
		block(name).ContainerName = o.Project + "-" + name
	}
	for _, name := range o.Unpublished {
		if name != o.Service {
			block(name).ResetPorts = true
		}
	}
	return out
}

// WriteOverride renders the override and writes it to path.
func WriteOverride(path string, o Override) error {
	data, err := RenderOverride(o)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return errs.Wrap(err, "cannot create %s", filepath.Dir(path))
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return errs.Wrap(err, "cannot write %s", path)
	}
	return nil
}

type overrideView struct {
	Services []serviceBlock
	// SecretPrefix is before the name of the variable Compose reads a
	// secret from.
	SecretPrefix string
}

// serviceBlock is one service's entry in the generated file.
type serviceBlock struct {
	Name          string
	Ports         string
	EnvFile       string
	EnvKeys       []string
	Env           map[string]string
	Secrets       []string
	Image         string
	ContainerName string
	ResetPorts    bool
}

// sortedKeys keeps the generated file stable between runs, so a diff of
// it shows what actually changed.
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

var overrideTemplate = template.Must(template.New("override").Parse(
	`# Generated by pit. Do not edit; it is rewritten on every run and
# removed when the sandbox is torn down.
services:
{{- range .Services }}
  {{ .Name }}:
{{- if .Ports }}
    # !override replaces the base file's ports instead of adding to
    # them. Compose merges sequences by appending, so without this the
    # original published port would still be bound and two sandboxes of
    # the same project would collide on it.
    ports: !override
      - "{{ .Ports }}"
{{- end }}
{{- if .ResetPorts }}
    # Bound on the host, a second sandbox of the project could not
    # start; inside the project's network it is reached as before.
    ports: !reset []
{{- end }}
{{- if .Image }}
    image: {{ .Image }}
{{- end }}
{{- if .ContainerName }}
    # A fixed name would be one container for every sandbox.
    container_name: {{ .ContainerName }}
{{- end }}
{{- if .EnvFile }}
    env_file:
      - {{ .EnvFile }}
{{- end }}
{{- if or .EnvKeys .Secrets }}
    environment:
{{- $env := .Env }}
{{- range .EnvKeys }}
      {{ . }}: "{{ index $env . }}"
{{- end }}
{{- range .Secrets }}
      {{ . }}: "{{ printf "${%s%s:-}" $.SecretPrefix . }}"
{{- end }}
{{- end }}
{{- end }}
`))

// OverridePath is where the generated file for a pull request lives.
// It sits beside the worktree rather than inside it, so it never shows
// up in the reviewer's diff or their editor.
func OverridePath(repoDir string, pr int) string {
	return filepath.Join(repoDir, "pr-"+strconv.Itoa(pr)+".compose.override.yml")
}

// BaseOverridePath is OverridePath for a pull request's base.
func BaseOverridePath(repoDir string, pr int) string {
	return OverridePathIn(repoDir, pr, "base")
}

// OverridePathIn is OverridePath for one of a pull request's sandboxes.
func OverridePathIn(repoDir string, pr int, slot string) string {
	if slot == "" {
		return OverridePath(repoDir, pr)
	}
	return filepath.Join(repoDir, "pr-"+strconv.Itoa(pr)+"-"+slot+".compose.override.yml")
}

// DevcontainerPathIn is where the compose file pit writes from a
// devcontainer.json lives, beside the override.
func DevcontainerPathIn(repoDir string, pr int, slot string) string {
	name := "pr-" + strconv.Itoa(pr)
	if slot != "" {
		name += "-" + slot
	}
	return filepath.Join(repoDir, name+".devcontainer.yml")
}
