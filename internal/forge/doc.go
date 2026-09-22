// Package forge abstracts the hosting platform a pull request lives on.
// It exposes pull request metadata, diffs and comments; implementations
// exist per platform (GitHub first, GitLab later).
package forge
