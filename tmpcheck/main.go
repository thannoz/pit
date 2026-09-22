package main

import (
	"context"
	"fmt"
	"os"

	"github.com/thannoz/pit/internal/forge"
	"github.com/thannoz/pit/internal/proc"
	"github.com/thannoz/pit/internal/ui"
)

func main() {
	host, repo := os.Args[1], os.Args[2]
	ctx, p := context.Background(), ui.Std()

	f, err := forge.For(host, repo, proc.Exec{})
	if err != nil {
		p.Error(err)
		os.Exit(1)
	}
	if gh, ok := f.(forge.GitHub); ok {
		if err := gh.Check(ctx); err != nil {
			p.Error(err)
			os.Exit(1)
		}
	}
	pr, err := f.PullRequest(ctx, 9000)
	if err != nil {
		p.Error(err)
		os.Exit(1)
	}
	fmt.Println("  ✓", pr.Describe())
}
