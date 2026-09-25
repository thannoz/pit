package runtime

import (
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/thannoz/pit/internal/errs"
)

// Service is what pit needs to know about a compose service in order to
// help someone configure pit for the first time.
type Service struct {
	// Name is the key under `services:`.
	Name string
	// Image is what it runs, when it is not built from source.
	Image string
	// Ports are the container ports the file mentions, in the order
	// they appear. They are a suggestion, not the truth: a service can
	// listen on a port it never declares.
	Ports []int
	// Context is the directory Docker builds it from, relative to the
	// compose file, and empty when the service is not built at all.
	// It is what decides whether a changed file can have changed this
	// service.
	Context string
	// DependsOn are the services it cannot run without. Starting a
	// subset of a project means starting these too, which is why pit
	// reads them rather than leaving it to Compose: what is started
	// is also what has to be built.
	DependsOn []string
	// Named says the file gives its container a fixed name, which a
	// second sandbox of the project could not have too.
	Named bool
	// Publishes says the file binds one of its ports on the host.
	Publishes bool
}

// composeFile is the sliver of the Compose schema pit reads directly.
// Everything else is Compose's business.
type composeFile struct {
	Services map[string]composeService `yaml:"services"`
}

type composeService struct {
	Image         string      `yaml:"image"`
	ContainerName string      `yaml:"container_name"`
	Ports         []yaml.Node `yaml:"ports"`
	Expose        []yaml.Node `yaml:"expose"`
	Build         yaml.Node   `yaml:"build"`
	DependsOn     yaml.Node   `yaml:"depends_on"`
}

// dependencies reads the services a service cannot run without.
//
// Compose accepts a list of names and a mapping whose keys are the
// names and whose values say how to wait for them; only the names
// matter here.
func dependencies(n yaml.Node) []string {
	switch n.Kind {
	case yaml.SequenceNode:
		out := make([]string, 0, len(n.Content))
		for _, item := range n.Content {
			if item.Value != "" {
				out = append(out, item.Value)
			}
		}
		return out
	case yaml.MappingNode:
		out := make([]string, 0, len(n.Content)/2)
		for i := 0; i+1 < len(n.Content); i += 2 {
			out = append(out, n.Content[i].Value)
		}
		return out
	default:
		return nil
	}
}

// buildContext reads the directory a service is built from.
//
// Compose accepts a bare path and a mapping; only the context is of
// interest here, because everything Docker is handed to build with
// lives inside it. A Dockerfile outside the context is possible and
// rare, and treating it as part of the context would be wrong in the
// other direction.
func buildContext(n yaml.Node) string {
	switch n.Kind {
	case yaml.ScalarNode:
		return n.Value
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			if n.Content[i].Value == "context" {
				return n.Content[i+1].Value
			}
		}
		// A mapping without a context means the compose file's own
		// directory, which is what Compose defaults to.
		return "."
	default:
		return ""
	}
}

// ReadServices lists the services a compose file declares, without
// starting Docker. `pit init` runs before anyone has a sandbox, and
// requiring a running daemon to write a configuration file would be a
// poor first impression.
func ReadServices(path string) ([]Service, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, errs.Wrap(err, "cannot read %s", path).
			WithHint("run `pit init` from a directory holding a compose file")
	}

	var f composeFile
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, errs.Wrap(err, "cannot read %s as a compose file", path).
			WithHint("check it with `docker compose config`")
	}
	if len(f.Services) == 0 {
		return nil, errs.New("%s declares no services", path).
			WithHint("a compose file needs a top level `services:` block")
	}

	// yaml.v3 hands back map keys in document order, but Go map
	// iteration is random, so the order is rebuilt from the document.
	names, err := serviceOrder(data)
	if err != nil {
		return nil, err
	}

	services := make([]Service, 0, len(names))
	for _, name := range names {
		s := f.Services[name]
		services = append(services, Service{
			Name:      name,
			Image:     s.Image,
			Ports:     containerPorts(s),
			Context:   buildContext(s.Build),
			DependsOn: dependencies(s.DependsOn),
			Named:     s.ContainerName != "",
			Publishes: len(s.Ports) > 0,
		})
	}
	return services, nil
}

// serviceOrder reads the service names in the order the author wrote
// them. A list that reshuffles on every run is hard to trust.
func serviceOrder(data []byte) ([]string, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, errs.Wrap(err, "cannot read the compose file")
	}
	if len(root.Content) == 0 {
		return nil, nil
	}

	doc := root.Content[0]
	for i := 0; i+1 < len(doc.Content); i += 2 {
		if doc.Content[i].Value != "services" {
			continue
		}
		block := doc.Content[i+1]
		names := make([]string, 0, len(block.Content)/2)
		for j := 0; j+1 < len(block.Content); j += 2 {
			names = append(names, block.Content[j].Value)
		}
		return names, nil
	}
	return nil, nil
}

// containerPorts collects the ports a service declares. Compose accepts
// several spellings -- "8080:80", 80, and a mapping form -- and the
// container side is the one pit wants.
func containerPorts(s composeService) []int {
	var ports []int
	seen := map[int]bool{}

	add := func(p int) {
		if p > 0 && !seen[p] {
			ports = append(ports, p)
			seen[p] = true
		}
	}

	for _, n := range s.Ports {
		add(containerSide(n))
	}
	for _, n := range s.Expose {
		add(containerSide(n))
	}
	return ports
}

// containerSide extracts the port inside the container from one entry.
func containerSide(n yaml.Node) int {
	if n.Kind == yaml.MappingNode {
		// The long form: {target: 80, published: 8080}
		for i := 0; i+1 < len(n.Content); i += 2 {
			if n.Content[i].Value == "target" {
				return atoi(n.Content[i+1].Value)
			}
		}
		return 0
	}

	// The short form: "8080:80", "127.0.0.1:8080:80", "80", "80/tcp".
	v := n.Value
	if i := strings.Index(v, "/"); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ":")
	return atoi(parts[len(parts)-1])
}

func atoi(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return n
}
