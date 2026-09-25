package review

import (
	"context"
	"os"

	"github.com/thannoz/pit/internal/analysis/routes"
	"github.com/thannoz/pit/internal/config"
	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/proc"
	"github.com/thannoz/pit/internal/state"
	"github.com/thannoz/pit/internal/workspace"
)

// Load gathers the input for one sandbox: the change from the branch it
// goes into to the commit the sandbox runs, both trees, and the
// configuration the sandbox was built with.
//
// The tree after the change is the sandbox's worktree -- what is
// running, not what the branch has moved on to since.
func Load(ctx context.Context, git workspace.Runner, box state.Sandbox) (Input, error) {
	repo := workspace.Repo{Root: box.RepoRoot}
	base := workspace.BaseRef(box.PR)
	if _, err := git.Output(ctx, proc.Command{
		Name: "git", Args: []string{"rev-parse", "--verify", "--quiet", base}, Dir: repo.Root,
	}); err != nil {
		return Input{}, errs.New("pit does not know which branch #%d goes into", box.PR).
			WithHint("`pit %d` fetches it; this sandbox was set up before pit did", box.PR)
	}
	if _, err := os.Stat(box.Worktree); err != nil {
		return Input{}, errs.New("the worktree of #%d is gone: %s", box.PR, box.Worktree).
			WithHint("`pit %d` sets it up again", box.PR)
	}

	d, err := workspace.Changes(ctx, git, repo, base, box.SHA)
	if err != nil {
		return Input{}, err
	}
	before, err := workspace.Tree(ctx, git, repo, d.Base)
	if err != nil {
		return Input{}, err
	}

	// The pull request's own configuration governs its sandbox (T-411);
	// without one, the reviewer's did.
	cfg, _, err := config.LoadFrom(box.Worktree)
	if err != nil {
		if cfg, _, err = config.LoadFrom(box.RepoRoot); err != nil {
			return Input{}, err
		}
	}
	analyzers, err := routes.For(cfg.Review.Routes.Framework)
	if err != nil {
		return Input{}, err
	}

	// A scenario the pull request's file does not have was loaded from
	// the reviewer's (T-706), and its example values are there too.
	var params map[string]string
	if _, ok := cfg.Scenario(box.Scenario); !ok && box.Scenario != "" {
		if mine, _, err := config.LoadFrom(box.RepoRoot); err == nil {
			params, _ = mine.Params(box.Scenario)
		}
	}

	return Input{
		Diff: d, Base: before, Head: os.DirFS(box.Worktree), Config: cfg,
		Scenario: box.Scenario, Params: params, URL: box.URL,
		Analyzers: analyzers, Linkers: routes.Linkers(),
	}, nil
}
