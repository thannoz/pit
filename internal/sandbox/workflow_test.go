package sandbox_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/thannoz/pit/internal/sandbox"
)

// The example workflow and the example configuration are two files a
// reader copies separately, and nothing at runtime checks that they
// agree: an image pit cannot find just means building. So the check
// happens here instead.
const examplePattern = "ghcr.io/acme/shop-{service}:{sha}"

// workflow is the sliver of the Actions schema this test reads.
type workflow struct {
	Jobs map[string]struct {
		Strategy struct {
			Matrix struct {
				Include []struct {
					Service string `yaml:"service"`
					Context string `yaml:"context"`
					Image   string `yaml:"image"`
				} `yaml:"include"`
			} `yaml:"matrix"`
		} `yaml:"strategy"`
		Steps []struct {
			Uses string            `yaml:"uses"`
			With map[string]string `yaml:"with"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
}

func readWorkflow(t *testing.T) (string, workflow) {
	t.Helper()

	path := filepath.Join("..", "..", "examples", "github-actions", "prebuild-images.yml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cannot read the shipped workflow: %v", err)
	}

	var w workflow
	if err := yaml.Unmarshal(data, &w); err != nil {
		t.Fatalf("the shipped workflow is not valid YAML: %v", err)
	}
	return string(data), w
}

// TestTheShippedWorkflowPushesWhatPitAsksFor is the acceptance
// criterion for T-507, at the level that can be checked without
// GitHub: the two names have to be the same string.
func TestTheShippedWorkflowPushesWhatPitAsksFor(t *testing.T) {
	const sha = "3e0eb5afd32f782bf3ec667aff57d66143aec8cd"

	_, w := readWorkflow(t)
	job, ok := w.Jobs["images"]
	if !ok {
		t.Fatalf("the workflow has no images job: %+v", w.Jobs)
	}
	if len(job.Strategy.Matrix.Include) == 0 {
		t.Fatal("the workflow builds no services")
	}

	for _, entry := range job.Strategy.Matrix.Include {
		pushed := entry.Image + ":" + sha
		wanted := sandbox.ExpandImage(examplePattern, entry.Service, 482, sha)

		if pushed != wanted {
			t.Errorf("the workflow pushes %q, pit asks for %q", pushed, wanted)
		}
	}
}

func TestTheShippedWorkflowTagsTheCommitUnderReview(t *testing.T) {
	// On a pull_request event github.sha is the merge commit GitHub
	// makes for the run. pit fetches refs/pull/<n>/head, so an image
	// tagged with github.sha is one no review will ever ask for -- and
	// the failure is silent, because a missing image just means
	// building.
	raw, w := readWorkflow(t)

	if strings.Contains(raw, "${{ github.sha }}") {
		t.Error("the workflow uses github.sha, which is the merge commit on a pull_request event")
	}

	var tagged, checkedOut bool
	for _, step := range w.Jobs["images"].Steps {
		if step.With["tags"] == "${{ matrix.image }}:${{ github.event.pull_request.head.sha }}" {
			tagged = true
		}
		if step.With["ref"] == "${{ github.event.pull_request.head.sha }}" {
			checkedOut = true
		}
	}
	if !tagged {
		t.Error("no step tags the image with the pull request's head commit")
	}
	if !checkedOut {
		t.Error("the checkout does not name the pull request's head commit")
	}
}

func TestTheShippedWorkflowPinsEveryAction(t *testing.T) {
	// A tag can be moved; a commit cannot. The template is copied into
	// other people's repositories, so it has to set the example.
	_, w := readWorkflow(t)

	for _, step := range w.Jobs["images"].Steps {
		if step.Uses == "" {
			continue
		}
		_, ref, found := strings.Cut(step.Uses, "@")
		if !found || len(ref) != 40 {
			t.Errorf("%q is not pinned to a commit", step.Uses)
		}
	}
}
