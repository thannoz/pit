package routes

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/thannoz/pit/internal/analysis"
	"github.com/thannoz/pit/internal/diff"
)

const kitPackage = `{"devDependencies": {"@sveltejs/kit": "^2.27.0", "svelte": "^5.0.0"}}`

// kitApp builds a SvelteKit project. A package.json gets a dependency
// on @sveltejs/kit unless it is given content of its own; a file given
// without content is empty.
func kitApp(files map[string]string) fstest.MapFS {
	fsys := fstest.MapFS{}
	for name, content := range files {
		if content == "" && (name == "package.json" || strings.HasSuffix(name, "/package.json")) {
			content = kitPackage
		}
		fsys[name] = &fstest.MapFile{Data: []byte(content)}
	}
	return fsys
}

// kitRoutesOf lists the routes as "kind method path file lines", with
// "-" for no method and "*" for the whole file.
func kitRoutesOf(t *testing.T, fsys fstest.MapFS) []string {
	t.Helper()
	routes, err := SvelteKit{}.Routes(context.Background(), fsys)
	if err != nil {
		t.Fatalf("Routes: %v", err)
	}
	var out []string
	for _, r := range routes {
		out = append(out, fmt.Sprintf("%s %s %s %s %s", r.Kind, cmpOr(r.Method, "-"), r.Path, r.File, rng(r.Lines)))
	}
	slices.Sort(out)
	return out
}

func cmpOr(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func expectKit(t *testing.T, fsys fstest.MapFS, want ...string) {
	t.Helper()
	slices.Sort(want)
	if got := kitRoutesOf(t, fsys); !slices.Equal(got, want) {
		t.Errorf("routes\n got %s\nwant %s", strings.Join(got, "\n     "), strings.Join(want, "\n     "))
	}
}

// doubtsAt are the reasons of every route at an address, each once.
func doubtsAt(t *testing.T, fsys fstest.MapFS, address string) []string {
	t.Helper()
	routes, err := SvelteKit{}.Routes(context.Background(), fsys)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, r := range routes {
		if r.Path != address {
			continue
		}
		for _, d := range r.Doubts {
			if !slices.Contains(out, d.Reason) {
				out = append(out, d.Reason)
			}
		}
	}
	return out
}

func TestSvelteKitAddresses(t *testing.T) {
	expectKit(t, kitApp(map[string]string{
		"package.json":                                      "",
		"src/routes/+page.svelte":                           "",
		"src/routes/about/+page.svelte":                     "",
		"src/routes/about/Team.svelte":                      "", // the page's own component
		"src/routes/about/+page.test.ts":                    "",
		"src/routes/about/+page.stories.svelte":             "",
		"src/routes/blog/[slug]/+page.svelte":               "",
		"src/routes/blog/[slug]/+page.ts":                   "export const load = () => ({})\n",
		"src/routes/(app)/dashboard/+page.svelte":           "",
		"src/routes/(app)/(nested)/settings/+page.svelte":   "",
		"src/routes/[[lang]]/pricing/+page.svelte":          "",
		"src/routes/docs/[...path]/+page.svelte":            "",
		"src/routes/orders/[id=integer]/+page.svelte":       "",
		"src/routes/[x+2e]well-known/security/+page.svelte": "",
		"src/routes/a[x+2f]b/+page.svelte":                  "",
		"src/routes/admin/+page@.svelte":                    "",
		"src/routes/(app)/+error.svelte":                    "",
		"src/lib/Button.svelte":                             "",
	}),
		"page GET / src/routes/+page.svelte *",
		"page GET /about src/routes/about/+page.svelte *",
		"page GET /blog/{slug} src/routes/blog/[slug]/+page.svelte *",
		"page GET /blog/{slug} src/routes/blog/[slug]/+page.ts *",
		"page GET /dashboard src/routes/(app)/dashboard/+page.svelte *",
		"page GET /settings src/routes/(app)/(nested)/settings/+page.svelte *",
		"page GET /pricing src/routes/[[lang]]/pricing/+page.svelte *",
		"page GET /docs/{path...} src/routes/docs/[...path]/+page.svelte *",
		"page GET /orders/{id} src/routes/orders/[id=integer]/+page.svelte *",
		"page GET /.well-known/security src/routes/[x+2e]well-known/security/+page.svelte *",
		"page GET /a%2Fb src/routes/a[x+2f]b/+page.svelte *",
		"page GET /admin src/routes/admin/+page@.svelte *",
	)
}

// A value inside a segment is an address pit cannot make a link to:
// Fill fills whole segments. It is shown with "…" where the value goes,
// and says why.
func TestSvelteKitValueInsideASegment(t *testing.T) {
	fsys := kitApp(map[string]string{
		"package.json":                           "",
		"src/routes/blog/[slug].json/+server.ts": "export const GET = () => new Response()\n",
	})
	expectKit(t, fsys, "endpoint GET /blog/….json src/routes/blog/[slug].json/+server.ts 1-1")
	doubts := doubtsAt(t, fsys, "/blog/….json")
	if len(doubts) != 1 || !strings.Contains(doubts[0], "[slug].json") {
		t.Errorf("doubts = %q, want one naming [slug].json", doubts)
	}
}

// A change to a POST handler is looked at with POST: each method a
// +server file exports is its own route, over its own lines.
func TestSvelteKitEndpointsByMethod(t *testing.T) {
	expectKit(t, kitApp(map[string]string{
		"package.json": "",
		"src/routes/api/orders/+server.ts": `import { json } from '@sveltejs/kit';

export const prerender = false;

export async function GET() {
	return json(list());
}

export const POST = async ({ request }) => {
	return json(await request.json());
};

function list() {
	return [];
}

export const fallback = () => new Response(null, { status: 405 });
`,
		"src/routes/api/legacy/+server.js": `export { GET, POST } from '$lib/server/legacy';
`,
		"src/routes/api/opaque/+server.ts": "import handler from './handler';\nhandler();\n",
	}),
		"endpoint - /api/orders src/routes/api/orders/+server.ts 3-3",
		"endpoint GET /api/orders src/routes/api/orders/+server.ts 5-7",
		"endpoint POST /api/orders src/routes/api/orders/+server.ts 9-11",
		"endpoint - /api/orders src/routes/api/orders/+server.ts 17-17",
		"endpoint GET /api/legacy src/routes/api/legacy/+server.js *",
		"endpoint POST /api/legacy src/routes/api/legacy/+server.js *",
		"endpoint - /api/opaque src/routes/api/opaque/+server.ts *",
	)
}

// A form action answers POST at the page's address; the load function
// answers GET.
func TestSvelteKitFormActions(t *testing.T) {
	expectKit(t, kitApp(map[string]string{
		"package.json":                  "",
		"src/routes/login/+page.svelte": "<form method=\"POST\"></form>\n",
		"src/routes/login/+page.server.ts": `import { fail } from '@sveltejs/kit';

export const load = async ({ locals }) => {
	return { user: locals.user };
};

export const actions = {
	default: async ({ request }) => {
		return fail(400);
	}
};
`,
	}),
		"page GET /login src/routes/login/+page.svelte *",
		"page GET /login src/routes/login/+page.server.ts 3-5",
		"page POST /login src/routes/login/+page.server.ts 7-11",
	)
}

// A layout is seen on the pages below it; like Next.js's, it is listed
// at the one quickest to open. The error page is not seen by opening a
// page, and is left unplaced.
func TestSvelteKitLayouts(t *testing.T) {
	expectKit(t, kitApp(map[string]string{
		"package.json":                                 "",
		"src/routes/+layout.svelte":                    "",
		"src/routes/+layout.server.ts":                 "export const load = () => ({})\n",
		"src/routes/+error.svelte":                     "",
		"src/routes/+page.svelte":                      "",
		"src/routes/(shop)/+layout.svelte":             "",
		"src/routes/(shop)/cart/+page.svelte":          "",
		"src/routes/(shop)/products/[id]/+page.svelte": "",
		"src/routes/empty/+layout.ts":                  "",
	}),
		"page GET / src/routes/+page.svelte *",
		"page - / src/routes/+layout.svelte *",
		"page - / src/routes/+layout.server.ts *",
		"page GET /cart src/routes/(shop)/cart/+page.svelte *",
		"page - /cart src/routes/(shop)/+layout.svelte *",
		"page GET /products/{id} src/routes/(shop)/products/[id]/+page.svelte *",
	)
}

// src/params/integer.ts decides whether /orders/[id=integer] answers
// /orders/42; a change to it is looked at there.
func TestSvelteKitParamMatchers(t *testing.T) {
	expectKit(t, kitApp(map[string]string{
		"package.json":                                   "",
		"src/params/integer.ts":                          "export const match = (p) => /^\\d+$/.test(p);\n",
		"src/params/unused.ts":                           "",
		"src/routes/orders/[id=integer]/+page.svelte":    "",
		"src/routes/orders/[id=integer]/+page.server.ts": "export const load = () => ({})\n",
		"src/routes/items/[[page=integer]]/+page.svelte": "",
		"src/routes/api/orders/[id=integer]/+server.ts":  "export const GET = () => new Response()\n",
		"src/routes/orders/[id=integer]/+layout.svelte":  "",
	}),
		"page GET /orders/{id} src/routes/orders/[id=integer]/+page.svelte *",
		"page GET /orders/{id} src/routes/orders/[id=integer]/+page.server.ts 1-1",
		"page - /orders/{id} src/routes/orders/[id=integer]/+layout.svelte *",
		"page GET /items src/routes/items/[[page=integer]]/+page.svelte *",
		"endpoint GET /api/orders/{id} src/routes/api/orders/[id=integer]/+server.ts 1-1",
		"page - /orders/{id} src/params/integer.ts *",
		"page - /items/{page} src/params/integer.ts *",
		"endpoint - /api/orders/{id} src/params/integer.ts *",
	)
}

// The configuration of SvelteKit 2, in svelte.config.js: a base path,
// routes somewhere else, mdsvex pages.
func TestSvelteKitConfig(t *testing.T) {
	expectKit(t, kitApp(map[string]string{
		"package.json": "",
		"svelte.config.js": `import adapter from '@sveltejs/adapter-cloudflare';
import { mdsvex } from 'mdsvex';

/** @type {import('@sveltejs/kit').Config} */
const config = {
	extensions: ['.svelte', '.svx'],
	preprocess: [mdsvex({ extensions: ['.svx'] })],
	kit: {
		// paths: { base: '/old' },
		adapter: adapter({ routes: { include: ['/*'] } }),
		paths: { base: '/shop' },
		files: { routes: 'app/pages' }
	}
};

export default config;
`,
		"app/pages/+page.svelte":          "",
		"app/pages/guide/+page.svx":       "",
		"src/routes/ignored/+page.svelte": "",
	}),
		"page GET /shop app/pages/+page.svelte *",
		"page GET /shop/guide app/pages/guide/+page.svx *",
	)
}

// From 3.0 on the configuration is given to the sveltekit() Vite
// plugin.
func TestSvelteKit3Config(t *testing.T) {
	expectKit(t, kitApp(map[string]string{
		"package.json": "",
		"vite.config.ts": `import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig } from 'vite';

export default defineConfig({
	plugins: [sveltekit({ paths: { base: '/docs' } })]
});
`,
		"src/routes/intro/+page.svelte": "",
	}),
		"page GET /docs/intro src/routes/intro/+page.svelte *",
	)
}

// What pit cannot read is said on every address: a base path in code,
// a reroute hook.
func TestSvelteKitDoubts(t *testing.T) {
	fsys := kitApp(map[string]string{
		"package.json": "",
		"svelte.config.js": `const dev = process.argv.includes('dev');
export default { kit: { paths: { base: dev ? '' : process.env.BASE_PATH } } };
`,
		"src/hooks.ts":            "import { deLocalizeUrl } from '$lib/paraglide/runtime';\n\n/** @type {import('@sveltejs/kit').Reroute} */ export const reroute = (request) => deLocalizeUrl(request.url).pathname;\n",
		"src/routes/+page.svelte": "",
	})
	doubts := doubtsAt(t, fsys, "/")
	want := []string{
		"svelte.config.js sets paths.base in a way pit cannot read; every address starts with it",
		"src/hooks.ts reroutes requests in code: which route answers an address is decided there",
	}
	if !slices.Equal(doubts, want) {
		t.Errorf("doubts\n got %q\nwant %q", doubts, want)
	}

	// A hooks file without reroute, and a literal base, leave nothing
	// to doubt.
	fsys["src/hooks.ts"] = &fstest.MapFile{Data: []byte("/*\nexport const reroute = () => '/';\n*/\nexport const transport = {};\n")}
	fsys["svelte.config.js"] = &fstest.MapFile{Data: []byte("export default { kit: { paths: { base: '' } } };\n")}
	if doubts := doubtsAt(t, fsys, "/"); len(doubts) != 0 {
		t.Errorf("doubts = %q, want none", doubts)
	}
}

// A project whose package.json does not depend on @sveltejs/kit is not
// a SvelteKit application, whatever its folders are called.
func TestNotASvelteKitApp(t *testing.T) {
	expectKit(t, kitApp(map[string]string{
		"package.json":            `{"dependencies": {"svelte": "^5.0.0"}}`,
		"src/routes/+page.svelte": "",
	}))
}

// Each application of a monorepo has its own routes and configuration.
func TestSvelteKitMonorepo(t *testing.T) {
	expectKit(t, kitApp(map[string]string{
		"package.json":                      `{"private": true}`,
		"apps/web/package.json":             "",
		"apps/web/src/routes/+page.svelte":  "",
		"apps/docs/package.json":            "",
		"apps/docs/svelte.config.js":        "export default { kit: { paths: { base: '/docs' } } };\n",
		"apps/docs/src/routes/+page.svelte": "",
	}),
		"page GET / apps/web/src/routes/+page.svelte *",
		"page GET /docs apps/docs/src/routes/+page.svelte *",
	)
}

// $lib is set up by SvelteKit 2 in a tsconfig it generates, which is
// not in the repository; #lib by SvelteKit 3 in package.json's imports.
// The alias option adds names of the application's own.
func TestSvelteKitAliases(t *testing.T) {
	links := tsLinks(t, map[string]string{
		"apps/v2/package.json":  kitPackage,
		"apps/v2/tsconfig.json": `{"extends": "./.svelte-kit/tsconfig.json"}`,
		"apps/v2/svelte.config.js": `export default {
	kit: { alias: { $components: 'src/components', '$utils/*': 'src/shared/utils/*' } }
};
`,
		"apps/v2/src/routes/+page.svelte": `<script>
	import Card from '$lib/Card.svelte';
	import { format } from '$lib/format';
	import Nav from '$components/Nav.svelte';
	import { slug } from '$utils/slug';
	import { page } from '$app/state';
</script>
<Card>{format(1)}</Card><Nav />{slug('a')}
`,
		"apps/v2/src/lib/Card.svelte":       "<slot />\n",
		"apps/v2/src/lib/format.ts":         "export function format(n: number) {\n\treturn String(n);\n}\n",
		"apps/v2/src/components/Nav.svelte": "<nav />\n",
		"apps/v2/src/shared/utils/slug.ts":  "export const slug = (s: string) => s;\n",
		"apps/v3/package.json": `{"imports": {"#lib": "./src/lib/index.ts", "#lib/*": "./src/lib/*"},
 "devDependencies": {"@sveltejs/kit": "3.0.0-next.27"}}`,
		"apps/v3/src/routes/+page.svelte": "<script>\n\timport { total } from '#lib/cart.ts';\n\timport { hello } from '#lib';\n</script>\n{total()}{hello}\n",
		"apps/v3/src/lib/cart.ts":         "export const total = () => 0;\n",
		"apps/v3/src/lib/index.ts":        "export const hello = 'hi';\n",
	})
	expectFiles(t, links,
		"apps/v2/src/routes/+page.svelte -> apps/v2/src/lib/Card.svelte",
		"apps/v2/src/routes/+page.svelte -> apps/v2/src/lib/format.ts",
		"apps/v2/src/routes/+page.svelte -> apps/v2/src/components/Nav.svelte",
		"apps/v2/src/routes/+page.svelte -> apps/v2/src/shared/utils/slug.ts",
		"apps/v3/src/routes/+page.svelte -> apps/v3/src/lib/cart.ts",
		"apps/v3/src/routes/+page.svelte -> apps/v3/src/lib/index.ts",
	)
}

// The whole way, as the guide walks it: a changed function in $lib
// reaches the handler that calls it, with its method, and not the
// other handler in the same file; a changed component reaches the page
// that shows it.
func TestSvelteKitChangesReachTheirRoutes(t *testing.T) {
	fsys := kitApp(map[string]string{
		"package.json": "",
		"src/routes/api/orders/+server.ts": `import { json } from '@sveltejs/kit';
import { list, create } from '$lib/server/orders';

export const GET = () => json(list());

export const POST = async ({ request }) => json(create(await request.json()));
`,
		"src/lib/server/orders.ts": `export function list() {
	return [];
}

export function create(o: unknown) {
	return o;
}
`,
		"src/routes/cart/+page.svelte":     "<script>\n\timport Total from '$lib/Total.svelte';\n</script>\n<Total />\n",
		"src/routes/checkout/+page.svelte": "<p>pay</p>\n",
		"src/lib/Total.svelte":             "<b>0</b>\n",
	})
	for _, tc := range []struct {
		file string
		line int
		want string
	}{
		{"src/lib/server/orders.ts", 2, "/api/orders GET"},
		{"src/lib/server/orders.ts", 6, "/api/orders POST"},
		{"src/lib/Total.svelte", 1, "/cart GET"},
	} {
		changed := analysis.Classify(diff.Diff{Files: []diff.File{{
			Path: tc.file, Change: diff.Modified,
			Hunks: []diff.Hunk{{New: diff.Range{Start: tc.line, Count: 1}}},
		}}}, nil)
		g, err := analysis.Entrypoints(context.Background(), fsys, changed, []analysis.Analyzer{SvelteKit{}}, Linkers())
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, e := range g.Entrypoints {
			got = append(got, e.Path+" "+strings.Join(e.Methods, ","))
		}
		if !slices.Equal(got, []string{tc.want}) {
			t.Errorf("a change to %s:%d reaches %q, want %q", tc.file, tc.line, got, tc.want)
		}
	}
}
