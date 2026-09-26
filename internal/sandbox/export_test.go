package sandbox

import "github.com/thannoz/pit/internal/runtime/kube"

// SetKubeRunner makes a test's runner the one that asks kind and
// kubectl, until the test ends.
func SetKubeRunner(cleanup func(func()), r kube.Runner) {
	previous := kubeRunner
	kubeRunner = r
	cleanup(func() { kubeRunner = previous })
}

// ImageTag is the tag pit gives an image it builds for a sandbox.
var ImageTag = imageTag
