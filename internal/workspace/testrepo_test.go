package workspace

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/thannoz/pit/internal/proc"
)

// testRepo is a clone with a local "remote" that carries pull request
// refs, so the fetch path can be exercised against the real git binary
// without a network or a GitHub account.
type testRepo struct {
	t      *testing.T
	Repo   Repo
	Remote string // the repository origin points at
}

// newTestRepo builds an upstream repository with one commit on main and
// a clone of it.
func newTestRepo(t *testing.T) *testRepo {
	t.Helper()
	requireGit(t)

	base := t.TempDir()
	upstream := filepath.Join(base, "upstream")
	clone := filepath.Join(base, "clone")

	mkdir(t, upstream)
	gitInit(t, upstream)
	configureIdentity(t, upstream)
	writeFile(t, filepath.Join(upstream, "README.md"), "upstream\n")
	git(t, upstream, "add", "README.md")
	git(t, upstream, "commit", "--quiet", "-m", "initial commit")

	git(t, base, "clone", "--quiet", upstream, clone)
	configureIdentity(t, clone)

	return &testRepo{
		t:      t,
		Remote: upstream,
		Repo: Repo{
			Root:     clone,
			Identity: Identity{Host: LocalHost, Owner: filepath.Dir(upstream), Name: "upstream"},
		},
	}
}

// PublishPullRequest creates refs/pull/<pr>/head upstream, carrying a
// commit that changes one file. It returns the commit's SHA.
func (r *testRepo) PublishPullRequest(pr int, content string) string {
	r.t.Helper()

	branch := "pit-test-pr"
	git(r.t, r.Remote, "checkout", "--quiet", "-B", branch)
	writeFile(r.t, filepath.Join(r.Remote, "feature.txt"), content)
	git(r.t, r.Remote, "add", "feature.txt")
	git(r.t, r.Remote, "commit", "--quiet", "-m", "pull request change")

	sha := r.rev(r.Remote, "HEAD")
	// This is what a hosting service exposes for a pull request.
	git(r.t, r.Remote, "update-ref", RemotePullRef(LocalHost, pr), sha)
	git(r.t, r.Remote, "checkout", "--quiet", "main")

	return sha
}

// Branches lists the local branches of the clone.
func (r *testRepo) Branches() []string {
	r.t.Helper()
	out := r.run(r.Repo.Root, "branch", "--format=%(refname:short)")
	return strings.Fields(out)
}

// Status returns the porcelain status of the clone's working tree.
func (r *testRepo) Status() string {
	r.t.Helper()
	return strings.TrimSpace(r.run(r.Repo.Root, "status", "--porcelain"))
}

// Head returns the commit the clone's HEAD points at.
func (r *testRepo) Head() string { return r.rev(r.Repo.Root, "HEAD") }

func (r *testRepo) rev(dir, ref string) string {
	r.t.Helper()
	return strings.TrimSpace(r.run(dir, "rev-parse", ref))
}

func (r *testRepo) run(dir string, args ...string) string {
	r.t.Helper()
	out, err := proc.Exec{}.Output(r.t.Context(), proc.Command{Name: "git", Args: args, Dir: dir})
	if err != nil {
		r.t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return string(out)
}
