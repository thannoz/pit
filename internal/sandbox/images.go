package sandbox

import (
	"context"
	"log/slog"
	"strconv"
	"strings"

	"github.com/thannoz/pit/internal/runtime"
)

// prepared is where a sandbox's images come from: the ones a pipeline
// had already published, and the ones this machine has to build.
type prepared struct {
	// images maps a service to the image that replaces its build.
	images map[string]string
	// build is what is left to build here.
	build build
	// pulled is what did not have to be, in the order it was asked
	// for, for the line the reviewer reads.
	pulled []string
}

// prepare fetches every image a pipeline has already built for this
// commit, and reports what is left over.
//
// A pull that fails is an ordinary answer, not an error: it means
// nobody published that image for this commit, and building it is
// exactly what pit would have done anyway.
func (m *Manager) prepare(ctx context.Context, req UpRequest, box runtime.Sandbox, work build, worktree, sha string, rep Reporter) prepared {
	pattern := req.Config.Build.Prebuilt
	if pattern == "" || work.nothing() {
		return prepared{build: work}
	}

	out := prepared{images: map[string]string{}, build: build{}}
	for _, name := range work.services {
		image := ExpandImage(pattern, name, req.PR.Number, sha)

		if err := m.Runtime.Pull(ctx, box, image, rep.Stdout(), rep.Stderr()); err != nil {
			slog.DebugContext(ctx, "no prebuilt image, building instead", "service", name, "image", image, "error", err)
			out.build.services = append(out.build.services, name)
			continue
		}
		out.images[name] = image
		out.pulled = append(out.pulled, name)
	}
	return out
}

// ExpandImage fills a configured image name in.
//
// {sha} is the commit, which is what makes the name safe to trust:
// the image it points at is either this pull request's code or it does
// not exist. {pr} is accepted so that a name can be read by a person,
// but it never identifies the contents on its own.
func ExpandImage(pattern, service string, pr int, sha string) string {
	return strings.NewReplacer(
		"{service}", service,
		"{sha}", sha,
		"{pr}", strconv.Itoa(pr),
	).Replace(pattern)
}

// summarise says what the build step did, because "build" alone hides
// the answer to the question someone waiting for it has.
func (p prepared) summarise(project string) string {
	var parts []string
	if len(p.pulled) > 0 {
		parts = append(parts, strings.Join(p.pulled, ", ")+" pulled")
	}
	switch {
	case p.build.all:
		parts = append(parts, project+" built")
	case len(p.build.services) > 0:
		parts = append(parts, strings.Join(p.build.services, ", ")+" built")
	}

	if len(parts) == 0 {
		return "nothing to rebuild"
	}
	return strings.Join(parts, ", ")
}
