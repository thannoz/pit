package routes

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/thannoz/pit/internal/analysis"
)

const nextPackage = `{"dependencies": {"next": "16.2.6", "react": "19.2.6"}}`

// tree builds a project from paths. A package.json gets a dependency
// on next unless it is given content of its own.
func tree(files ...string) fstest.MapFS {
	fsys := fstest.MapFS{}
	for _, f := range files {
		name, content, ok := strings.Cut(f, "=")
		if !ok && (name == "package.json" || strings.HasSuffix(name, "/package.json")) {
			content = nextPackage
		}
		fsys[name] = &fstest.MapFile{Data: []byte(content)}
	}
	return fsys
}

func routesOf(t *testing.T, fsys fstest.MapFS) []string {
	t.Helper()
	routes, err := NextJS{}.Routes(context.Background(), fsys)
	if err != nil {
		t.Fatalf("Routes: %v", err)
	}
	var out []string
	for _, r := range routes {
		out = append(out, fmt.Sprintf("%s %s %s", r.Kind, r.Path, r.File))
	}
	slices.Sort(out)
	return out
}

func expect(t *testing.T, fsys fstest.MapFS, want ...string) {
	t.Helper()
	slices.Sort(want)
	if got := routesOf(t, fsys); !slices.Equal(got, want) {
		t.Errorf("routes\n got %s\nwant %s", strings.Join(got, "\n     "), strings.Join(want, "\n     "))
	}
}

// The routes of a real application, every line reviewed when it was
// frozen: route groups, a parallel slot, catch-alls, an mdx page, route
// handlers written in tsx, layouts, and a hundred images that are not
// routes.
func TestTheRoutesOfTailwindcssCom(t *testing.T) {
	var files []string
	read(t, filepath.Join("..", "..", "..", "testdata", "routes", "tailwindcss.com-2500.tree"), func(line string) {
		files = append(files, line)
	})
	var want []string
	read(t, filepath.Join("..", "..", "..", "testdata", "routes", "tailwindcss.com-2500.golden"), func(line string) {
		want = append(want, strings.Join(strings.Split(line, "\t"), " "))
	})
	expect(t, tree(files...), want...)
}

func read(t *testing.T, name string, each func(string)) {
	t.Helper()
	f, err := os.Open(name)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = f.Close() }()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if line := scanner.Text(); line != "" && !strings.HasPrefix(line, "#") {
			each(line)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read: %v", err)
	}
}

func TestAppRouterAddresses(t *testing.T) {
	expect(t, tree(
		"package.json",
		"app/page.tsx",
		"app/orders/page.tsx",
		"app/orders/[id]/page.tsx",
		"app/docs/[...slug]/page.mdx",
		"app/shop/[[...filters]]/page.jsx",
		"app/(marketing)/pricing/page.tsx",
		"app/(shop)/(nested)/cart/page.js",
		"app/api/orders/route.ts",
		"app/api/orders/[id]/route.tsx",
		"app/%5Finternal/page.tsx",
	),
		"page / app/page.tsx",
		"page /orders app/orders/page.tsx",
		"page /orders/{id} app/orders/[id]/page.tsx",
		"page /docs/{slug...} app/docs/[...slug]/page.mdx",
		// An optional catch-all answers without a value too, and that
		// address is the one that needs nothing filled in.
		"page /shop app/shop/[[...filters]]/page.jsx",
		"page /pricing app/(marketing)/pricing/page.tsx",
		"page /cart app/(shop)/(nested)/cart/page.js",
		"endpoint /api/orders app/api/orders/route.ts",
		"endpoint /api/orders/{id} app/api/orders/[id]/route.tsx",
		"page /_internal app/%5Finternal/page.tsx",
	)
}

// Next.js leaves out every part of the path that starts with an
// underscore, folders and files alike.
func TestPrivateFoldersAreNotRouted(t *testing.T) {
	expect(t, tree(
		"package.json",
		"app/orders/page.tsx",
		"app/_components/page.tsx",
		"app/orders/_parts/deep/page.tsx",
		"app/orders/_page.tsx",
	),
		"page /orders app/orders/page.tsx",
	)
}

// Files next to a page that Next.js does not route: components,
// styles, tests, type declarations.
func TestOnlyPagesAndRouteHandlersAreAddresses(t *testing.T) {
	expect(t, tree(
		"package.json",
		"app/orders/page.tsx",
		"app/orders/orders-table.tsx",
		"app/orders/page.module.css",
		"app/orders/page.test.tsx",
		"app/orders/page.d.ts",
		"app/orders/route.md",
	),
		"page /orders app/orders/page.tsx",
	)
}

// A page in a parallel slot is shown next to the page at its address;
// where there is none, opening the address shows no page. Checked
// against tailwindcss.com, whose @breadcrumb/installation/using-vite has
// no page beside it: the address answers with the not-found page.
func TestSlotsCountOnlyBesideAPage(t *testing.T) {
	expect(t, tree(
		"package.json",
		"app/(docs)/docs/[slug]/page.tsx",
		"app/(docs)/@breadcrumb/docs/[slug]/page.tsx",
		"app/(docs)/@breadcrumb/stale/page.tsx",
		"app/(docs)/@breadcrumb/[...catchAll]/page.tsx",
	),
		"page /docs/{slug} app/(docs)/docs/[slug]/page.tsx",
		"page /docs/{slug} app/(docs)/@breadcrumb/docs/[slug]/page.tsx",
	)
}

// Intercepting routes answer at the address they intercept, counted in
// segments of the address, not folders: groups and slots do not count.
func TestInterceptingRoutes(t *testing.T) {
	expect(t, tree(
		"package.json",
		"app/feed/page.tsx",
		"app/photo/[id]/page.tsx",
		"app/feed/(.)photo/[id]/page.tsx",
		"app/(group)/feed/(..)photo/[id]/page.tsx",
		// The modal pattern: a slot intercepts a page that exists.
		"app/cart/page.tsx",
		"app/shop/items/@modal/(..)(..)cart/page.tsx",
		"app/a/b/c/(...)login/page.tsx",
	),
		"page /feed app/feed/page.tsx",
		"page /photo/{id} app/photo/[id]/page.tsx",
		"page /feed/photo/{id} app/feed/(.)photo/[id]/page.tsx",
		"page /photo/{id} app/(group)/feed/(..)photo/[id]/page.tsx",
		"page /cart app/cart/page.tsx",
		"page /cart app/shop/items/@modal/(..)(..)cart/page.tsx",
		"page /login app/a/b/c/(...)login/page.tsx",
	)
}

// A layout is looked at on one page below it: the one that needs no
// value filled in, and then the shortest.
func TestLayoutsLeadToTheNearestPage(t *testing.T) {
	expect(t, tree(
		"package.json",
		"app/layout.tsx",
		"app/(shop)/layout.tsx",
		"app/(shop)/orders/[id]/page.tsx",
		"app/(shop)/orders/archive/page.tsx",
		"app/(shop)/orders/loading.tsx",
		"app/settings/profile/page.tsx",
		"app/settings/error.tsx",
		"app/empty/layout.tsx",
	),
		"page /orders/{id} app/(shop)/orders/[id]/page.tsx",
		"page /orders/archive app/(shop)/orders/archive/page.tsx",
		"page /settings/profile app/settings/profile/page.tsx",
		"page /orders/archive app/(shop)/layout.tsx",
		"page /orders/archive app/(shop)/orders/loading.tsx",
		// Two pages without a placeholder at the same depth: the first
		// in order, so the answer does not change from run to run.
		"page /orders/archive app/layout.tsx",
		// A layout with no page below it is shown nowhere, and an error
		// page is not shown by opening the page it belongs to.
	)

	// A deeper page that opens as it is beats a shallower one that
	// needs a value first.
	expect(t, tree(
		"package.json",
		"app/dashboard/layout.tsx",
		"app/dashboard/[team]/page.tsx",
		"app/dashboard/settings/general/page.tsx",
	),
		"page /dashboard/{team} app/dashboard/[team]/page.tsx",
		"page /dashboard/settings/general app/dashboard/settings/general/page.tsx",
		"page /dashboard/settings/general app/dashboard/layout.tsx",
	)
}

func TestPagesRouterAddresses(t *testing.T) {
	expect(t, tree(
		"package.json",
		"pages/index.tsx",
		"pages/about.js",
		"pages/blog/index.tsx",
		"pages/blog/[slug].tsx",
		"pages/docs/[...path].tsx",
		"pages/shop/[[...filters]].tsx",
		"pages/404.tsx",
		"pages/llms.txt.tsx",
		"pages/api/orders.ts",
		"pages/api/orders/[id].ts",
		"pages/api.tsx",
		"pages/_app.tsx",
		"pages/_document.tsx",
		"pages/blog/styles.module.css",
	),
		"page / pages/index.tsx",
		"page /about pages/about.js",
		"page /blog pages/blog/index.tsx",
		"page /blog/{slug} pages/blog/[slug].tsx",
		"page /docs/{path...} pages/docs/[...path].tsx",
		"page /shop pages/shop/[[...filters]].tsx",
		"page /404 pages/404.tsx",
		"page /llms.txt pages/llms.txt.tsx",
		"endpoint /api/orders pages/api/orders.ts",
		"endpoint /api/orders/{id} pages/api/orders/[id].ts",
		// Only what is under pages/api/ is an API route.
		"page /api pages/api.tsx",
		// _app and _document wrap every page; the root is the nearest.
		"page / pages/_app.tsx",
		"page / pages/_document.tsx",
	)
}

// Next.js looks for app/ and pages/ next to package.json, then in
// src/, and each on its own.
func TestWhereTheRoutersAreLookedFor(t *testing.T) {
	t.Run("src", func(t *testing.T) {
		expect(t, tree("package.json", "src/app/page.tsx"), "page / src/app/page.tsx")
	})
	t.Run("the root wins over src", func(t *testing.T) {
		expect(t, tree("package.json", "app/page.tsx", "src/app/other/page.tsx"), "page / app/page.tsx")
	})
	t.Run("each router on its own", func(t *testing.T) {
		expect(t, tree("package.json", "pages/old.tsx", "src/app/new/page.tsx"),
			"page /old pages/old.tsx",
			"page /new src/app/new/page.tsx",
		)
	})
	t.Run("not below src/components", func(t *testing.T) {
		expect(t, tree("package.json", "src/components/pages/Home.tsx", "src/components/app/page.tsx"))
	})
}

// Every application of a monorepo, and nothing that is not one.
func TestApplicationsAreFoundByTheirPackageJSON(t *testing.T) {
	expect(t, tree(
		"package.json="+`{"private": true, "devDependencies": {"turbo": "2"}}`,
		"apps/web/package.json",
		"apps/web/app/(ee)/api/groups/[groupIdOrSlug]/route.ts",
		"apps/docs/package.json="+`{"devDependencies": {"next": "15"}}`,
		"apps/docs/pages/index.mdx",
		// A Remix app has app/routes, and page.tsx in it by chance.
		"apps/remix/package.json="+`{"dependencies": {"@remix-run/react": "2"}}`,
		"apps/remix/app/routes/page.tsx",
		"apps/remix/app/page.tsx",
		// Dependencies of the applications are not applications.
		"apps/web/node_modules/next/package.json",
		"apps/web/node_modules/next/app/page.tsx",
		// A package.json that is not JSON is passed over.
		"fixtures/broken/package.json={not json",
		"fixtures/broken/app/page.tsx",
	),
		"endpoint /api/groups/{groupIdOrSlug} apps/web/app/(ee)/api/groups/[groupIdOrSlug]/route.ts",
		"page / apps/docs/pages/index.mdx",
	)
}

func TestAProjectWithoutNextHasNoRoutes(t *testing.T) {
	expect(t, tree(
		"package.json="+`{"dependencies": {"astro": "4"}}`,
		"src/pages/index.astro",
		"src/pages/api/orders.ts",
		"go.mod",
		"app/page.go",
	))
}

func TestACancelledSearchStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NextJS{}.Routes(ctx, tree("package.json", "app/page.tsx"))
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

// The addresses the heuristic writes are the ones Fill understands.
func TestNextAddressesCanBeFilled(t *testing.T) {
	routes, err := NextJS{}.Routes(context.Background(), tree(
		"package.json",
		"app/partners/[slug]/page.tsx",
		"app/docs/[...path]/page.tsx",
	))
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]string{"slug": "cursor", "path": "installation/using-vite"}
	var got []string
	for _, r := range routes {
		filled, missing := analysis.Fill(r.Path, values)
		if len(missing) != 0 {
			t.Errorf("%s: missing %v", r.Path, missing)
		}
		got = append(got, filled)
	}
	slices.Sort(got)
	if want := []string{"/docs/installation/using-vite", "/partners/cursor"}; !slices.Equal(got, want) {
		t.Errorf("filled %v, want %v", got, want)
	}
}
