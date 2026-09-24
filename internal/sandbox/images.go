package sandbox

import (
	"context"
	"log/slog"
	"strconv"
	"strings"

	"golang.org/x/sync/errgroup"

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
func (m *Manager) prepare(ctx context.Context, req UpRequest, box runtime.Sandbox, work build, worktree, sha string, rep Reporter) (prepared, error) {
	pattern := req.Config.Build.Prebuilt
	if pattern == "" || work.nothing() {
		return prepared{build: work}, nil
	}

	// All at once: a pull is mostly waiting for a network, and four
	// services waiting one after another is four times the wait for no
	// reason. Measured on four images: 9.8 seconds in sequence, 3.4
	// together.
	images := make([]string, len(work.services))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(pullConcurrency)

	for i, name := range work.services {
		g.Go(func() error {
			image := ExpandImage(pattern, name, req.PR.Number, sha)

			if err := m.Runtime.Pull(gctx, box, image, rep.Stdout(), rep.Stderr()); err != nil {
				// A pull that finds nothing means building, but one
				// that was interrupted means stopping: carrying on
				// would build everything after a Ctrl+C.
				if gctx.Err() != nil {
					return gctx.Err()
				}
				slog.DebugContext(ctx, "no prebuilt image, building instead",
					"service", name, "image", image, "error", err)
				return nil
			}
			images[i] = image
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return prepared{}, err
	}

	// Collected afterwards, in the order the services were declared:
	// what finishes first is a property of the network, and a line
	// that reads differently every run is a line nobody trusts.
	out := prepared{images: map[string]string{}}
	for i, name := range work.services {
		if images[i] == "" {
			out.build.services = append(out.build.services, name)
			continue
		}
		out.images[name] = images[i]
		out.pulled = append(out.pulled, name)
	}
	return out, nil
}

// pullConcurrency bounds how many images are fetched at once. Enough
// that the waiting overlaps, few enough not to turn a project of
// twenty services into a denial of service against its own registry.
const pullConcurrency = 4

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
