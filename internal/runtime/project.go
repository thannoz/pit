package runtime

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/thannoz/pit/internal/errs"
)

// ProjectPrefix marks every project pit creates, so a stray container
// can be recognised as pit's and cleaned up.
const ProjectPrefix = "pit-"

// maxProjectName keeps the derived container names within what Docker
// and the tools around it handle comfortably. Compose appends the
// service name and an index to this.
const maxProjectName = 60

// composeName is what Docker Compose accepts as a project name:
// lowercase letters, digits, dashes and underscores, starting with a
// letter or a digit.
var composeName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// ProjectName derives the Compose project name for a pull request.
//
// The project name is the only isolation pit needs: Docker derives the
// network and volume names from it, so two sandboxes that differ here
// share nothing. repoRef therefore carries the repository's hash as
// well as its slug -- two clones of the same project, or two projects
// with the same name on different hosts, must not end up in one place.
func ProjectName(repoRef string, pr int) (string, error) {
	if pr <= 0 {
		return "", errs.New("%d is not a pull request number", pr)
	}
	// An empty reference would yield "pit--1" for every repository, so
	// every project would silently share one set of networks and
	// volumes. That is the failure this whole function exists to
	// prevent, so it is worth a guard even though it means a bug
	// upstream.
	if strings.TrimSpace(repoRef) == "" {
		return "", errs.New("the repository reference is empty").
			WithHint("this is a bug in pit; please report it")
	}

	name := ProjectPrefix + strings.ToLower(repoRef) + "-" + strconv.Itoa(pr)
	if len(name) > maxProjectName {
		return "", errs.New("the project name %q is too long (%d characters, at most %d)", name, len(name), maxProjectName).
			WithHint("the repository name is unusually long; shorten it or report this")
	}
	if !composeName.MatchString(name) {
		return "", errs.New("%q is not a usable Docker Compose project name", name).
			WithHint("it may contain lowercase letters, digits, dashes and underscores only")
	}
	return name, nil
}

// IsPitProject reports whether a Compose project belongs to pit. Used
// when cleaning up after a crash, where the only thing left is a name.
func IsPitProject(name string) bool {
	return strings.HasPrefix(name, ProjectPrefix)
}

// PullRequestOf extracts the pull request number from a project name
// pit created.
func PullRequestOf(project string) (int, bool) {
	if !IsPitProject(project) {
		return 0, false
	}
	i := strings.LastIndex(project, "-")
	if i < 0 {
		return 0, false
	}
	pr, err := strconv.Atoi(project[i+1:])
	if err != nil || pr <= 0 {
		return 0, false
	}
	return pr, true
}

// NetworkName is the network Compose creates for a project. pit does
// not create it; knowing the name is what lets a cleanup verify that
// nothing was left behind.
func NetworkName(project string) string {
	return fmt.Sprintf("%s_default", project)
}
