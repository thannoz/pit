package sandbox

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/thannoz/pit/internal/config"
	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/runtime"
	"github.com/thannoz/pit/internal/suggest"
)

// selection is the part of a project a review needs.
//
// A project of eight services usually has four that decide what a
// screen looks like; the queue worker, the mail catcher and the
// analytics sink cost minutes of build and answer nothing a reviewer
// asked. Naming the four is the whole feature.
type selection struct {
	// names are the services to start, in the order the compose file
	// declares them, and already including whatever they depend on.
	// Empty means every service there is.
	names []string
	// total is how many the project declares, so the narration can
	// say four of eight rather than just four.
	total int
}

// whole reports whether the selection is the whole project.
func (s selection) whole() bool { return len(s.names) == 0 }

// has reports whether a service is part of the selection.
func (s selection) has(name string) bool {
	return s.whole() || slices.Contains(s.names, name)
}

// describe says what is being started, for the step line.
func (s selection) describe(project string) string {
	if s.whole() {
		return project
	}
	return fmt.Sprintf("%s, %d of %d services", project, len(s.names), s.total)
}

// selectServices works out which services to bring up.
//
// The configured names are entry points, not a complete list: what
// they depend on is added here rather than left to Compose, because
// what is started is also what has to be built, and pit decides that
// before Compose is asked anything.
func selectServices(c *config.Config, worktree string) (selection, error) {
	if len(c.Compose.Services) == 0 {
		return selection{}, nil
	}

	declared, err := readProject(c, worktree)
	if err != nil {
		// Without the file there is nothing to check a name against,
		// and starting everything is the harmless answer.
		return selection{}, nil
	}

	needed := map[string]bool{}
	for _, name := range c.Compose.Services {
		if _, ok := declared.byName[name]; !ok {
			return selection{}, unknownService(name, declared)
		}
		addWithDependencies(name, declared, needed)
	}

	// The compose file's order, so that two runs of pit read the same
	// and a diff of the narration means something.
	var names []string
	for _, s := range declared.names {
		if needed[s] {
			names = append(names, s)
		}
	}

	if !needed[c.Web.Service] {
		return selection{}, errs.New("compose.services does not include %q, which is the service a reviewer opens", c.Web.Service).
			WithHint("add %q to compose.services, or point web.service at one of %s",
				c.Web.Service, strings.Join(names, ", "))
	}
	return selection{names: names, total: len(declared.names)}, nil
}

// project is what the compose files declare, in the order they
// declare it.
type project struct {
	names  []string
	byName map[string]runtime.Service
}

// readProject reads every service of every configured compose file.
func readProject(c *config.Config, worktree string) (project, error) {
	p := project{byName: map[string]runtime.Service{}}

	for _, file := range c.Compose.Files {
		declared, err := runtime.ReadServices(filepath.Join(worktree, file))
		if err != nil {
			return project{}, err
		}
		for _, s := range declared {
			if _, seen := p.byName[s.Name]; !seen {
				p.names = append(p.names, s.Name)
			}
			p.byName[s.Name] = s
		}
	}
	return p, nil
}

// addWithDependencies adds a service and everything it cannot run
// without. The visited set is what keeps a cycle -- which Compose
// rejects, but which can be written -- from becoming an endless walk.
func addWithDependencies(name string, declared project, into map[string]bool) {
	if into[name] {
		return
	}
	into[name] = true

	for _, dep := range declared.byName[name].DependsOn {
		if _, ok := declared.byName[dep]; ok {
			addWithDependencies(dep, declared, into)
		}
	}
}

func unknownService(name string, declared project) error {
	e := errs.New("compose.services names %q, which the compose file does not declare", name)

	if near := suggest.Closest(name, declared.names); near != "" {
		return e.WithHint("did you mean %q? the file declares: %s", near, strings.Join(declared.names, ", "))
	}
	return e.WithHint("the file declares: %s", strings.Join(declared.names, ", "))
}
