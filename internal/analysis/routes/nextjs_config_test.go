package routes

import (
	"context"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
)

// nextDoubts maps each address to its doubts, as "level: reason".
func nextDoubts(t *testing.T, fsys fstest.MapFS) map[string][]string {
	t.Helper()
	routes, err := NextJS{}.Routes(context.Background(), fsys)
	if err != nil {
		t.Fatalf("Routes: %v", err)
	}
	out := map[string][]string{}
	for _, r := range routes {
		key := r.Path + " " + r.File
		out[key] = nil
		for _, d := range r.Doubts {
			out[key] = append(out[key], d.Confidence.String()+": "+d.Reason)
		}
	}
	return out
}

func expectDoubts(t *testing.T, got map[string][]string, want map[string][]string) {
	t.Helper()
	for k, w := range want {
		g, ok := got[k]
		if !ok {
			t.Errorf("no route %q; have %v", k, keys(got))
			continue
		}
		if !slices.Equal(g, w) {
			t.Errorf("%s\n got %q\nwant %q", k, g, w)
		}
	}
}

func keys(m map[string][]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// dub's middleware, in the form the Next.js documentation recommends:
// it runs for everything but the API. Its dashboard pages are served on
// another host through a rewrite; its endpoints are not touched.
func TestMiddlewareMakesTheAddressesItRunsForUncertain(t *testing.T) {
	for _, name := range []string{"middleware.ts", "proxy.ts"} {
		got := nextDoubts(t, tree(
			"apps/web/package.json",
			"apps/web/app/app.dub.co/(dashboard)/[slug]/program/groups/page.tsx",
			"apps/web/app/(ee)/api/groups/route.ts",
			"apps/web/app/favicon.ico/route.ts",
			"apps/web/"+name+"="+`import { NextResponse } from "next/server";

export const config = {
  runtime: "nodejs",
  matcher: [
    /*
     * Match all paths except for:
     * 1. /api/ routes
     */
    "/((?!api/|_next/|_proxy/|favicon.ico|sitemap.xml|robots.txt|manifest.webmanifest).*)",
  ],
};

export default async function middleware(req) {
  return NextResponse.rewrite(new URL("/app.dub.co" + req.nextUrl.pathname, req.url));
}
`))
		expectDoubts(t, got, map[string][]string{
			"/app.dub.co/{slug}/program/groups apps/web/app/app.dub.co/(dashboard)/[slug]/program/groups/page.tsx": {
				"uncertain: apps/web/" + name + " runs for this address and can rewrite or redirect it",
			},
			"/api/groups apps/web/app/(ee)/api/groups/route.ts": nil,
			"/favicon.ico apps/web/app/favicon.ico/route.ts":    nil,
		})
	}
}

func TestMatchers(t *testing.T) {
	for _, tc := range []struct {
		src   string
		runs  []string
		skips []string
	}{
		{`export const config = { matcher: "/dashboard/:path*" }`,
			[]string{"/dashboard", "/dashboard/a/b"}, []string{"/", "/dash", "/api/x"}},
		{`export const config = { matcher: ["/about/:id", "/blog/:slug+"] }`,
			[]string{"/about/1", "/blog/a/b"}, []string{"/about", "/blog", "/about/1/2"}},
		{`export const config = { matcher: [{ source: "/app/:path*", has: [{ type: "header", key: "x" }] }] }`,
			[]string{"/app/x"}, []string{"/other"}},
		{`export const config = { matcher: ["/((?!api|_next/static|.*\\.png$).*)"] }`,
			[]string{"/", "/orders"}, []string{"/api/x", "/logo.png"}},
		// No matcher: every address.
		{`export default function middleware() {}`, []string{"/", "/api/x"}, nil},
		// A matcher pit cannot read is taken to run everywhere.
		{`const m = ["/x"]; export const config = { matcher: m }`, []string{"/", "/y"}, nil},
	} {
		c := nextConfig{middleware: "middleware.ts", matchers: matchersOf(tc.src)}
		for _, a := range tc.runs {
			if !c.applies(a) {
				t.Errorf("%s: does not run for %s", tc.src, a)
			}
		}
		for _, a := range tc.skips {
			if c.applies(a) {
				t.Errorf("%s: runs for %s", tc.src, a)
			}
		}
	}
}

// What next.config says, read from its text.
func TestNextConfig(t *testing.T) {
	got := nextDoubts(t, tree(
		"package.json",
		"next.config.ts="+`import type { NextConfig } from "next";

const nextConfig = {
  // basePath: "/old",
  basePath: "/docs",
  i18n: { locales: ["en", "de"], defaultLocale: "en" },
  async redirects() {
    return [
      { source: "/installation", destination: "/installation/using-vite", permanent: false },
      { source: "/guides/:slug", destination: "/installation/:slug", permanent: false },
    ];
  },
  async rewrites() {
    return [{ source: "/plus/:path*", destination: "https://example.com/plus/:path*" }];
  },
} satisfies NextConfig;

export default nextConfig;
`,
		"app/installation/page.tsx",
		"app/installation/using-vite/page.tsx",
		"app/guides/[slug]/page.tsx",
		"app/plus/[[...path]]/page.tsx",
		"pages/legacy.tsx",
	))
	expectDoubts(t, got, map[string][]string{
		// The basePath comes first; the redirect is read without it,
		// as Next.js reads it.
		"/docs/installation app/installation/page.tsx": {
			"uncertain: next.config redirects /installation to /installation/using-vite",
		},
		"/docs/installation/using-vite app/installation/using-vite/page.tsx": nil,
		"/docs/guides/{slug} app/guides/[slug]/page.tsx": {
			"uncertain: next.config redirects /guides/:slug to /installation/:slug",
		},
		"/docs/plus app/plus/[[...path]]/page.tsx": {
			"uncertain: next.config rewrites /plus/:path* to https://example.com/plus/:path*",
		},
		// i18n is a Pages Router setting.
		"/docs/legacy pages/legacy.tsx": {
			"uncertain: next.config sets i18n; the address may start with a locale",
		},
	})
}

func TestNextConfigThatCannotBeRead(t *testing.T) {
	got := nextDoubts(t, tree(
		"package.json",
		"next.config.js="+`module.exports = {
  basePath: process.env.BASE_PATH,
  pageExtensions: ["page.tsx", "api.ts"],
};
`,
		"app/orders/page.tsx",
	))
	expectDoubts(t, got, map[string][]string{
		"/orders app/orders/page.tsx": {
			"uncertain: next.config.js sets a basePath pit cannot read; every address starts with it",
			`uncertain: next.config.js sets pageExtensions to names like "page.tsx", which pit does not follow`,
		},
	})
}

// tailwindcss.com sets pageExtensions to the usual ones and mdx, which
// is exactly what pit reads: no doubt.
func TestUsualPageExtensionsAreNoDoubt(t *testing.T) {
	got := nextDoubts(t, tree(
		"package.json",
		`next.config.ts=export default { pageExtensions: ["js", "jsx", "ts", "tsx", "mdx"] };`,
		"app/brand/page.mdx",
	))
	expectDoubts(t, got, map[string][]string{"/brand app/brand/page.mdx": nil})
}

func TestShownOnlySometimes(t *testing.T) {
	got := nextDoubts(t, tree(
		"package.json",
		"app/photo/[id]/page.tsx",
		"app/feed/page.tsx",
		"app/feed/@modal/(..)photo/[id]/page.tsx",
		"app/feed/loading.tsx",
		"app/feed/layout.tsx",
	))
	expectDoubts(t, got, map[string][]string{
		"/photo/{id} app/photo/[id]/page.tsx": nil,
		"/photo/{id} app/feed/@modal/(..)photo/[id]/page.tsx": {
			"likely: an intercepting route: it shows when the app navigates here from another page; opening the address directly shows the page it intercepts",
		},
		"/feed app/feed/loading.tsx": {"likely: a loading screen: it shows only while the page below it loads"},
		"/feed app/feed/layout.tsx":  nil,
	})
}

func TestCommentsDoNotConfigure(t *testing.T) {
	src := stripComments("a: 1, // basePath: \"/x\"\n/* i18n: {} */ b: \"// not a comment\"")
	if strings.Contains(src, "basePath") || strings.Contains(src, "i18n") || !strings.Contains(src, `"// not a comment"`) {
		t.Errorf("stripComments = %q", src)
	}
}

// dub's redirects are conditions on the host. pit serves a sandbox on
// localhost, where neither applies; one on a cookie applies sometimes.
// Sources under headers() move nothing.
func TestRedirectConditions(t *testing.T) {
	got := nextDoubts(t, tree(
		"package.json",
		"next.config.js="+`module.exports = {
  async redirects() {
    return [
      {
        source: "/api/:path*",
        missing: [{ type: "host", value: ".*(\\.dub\\.co|localhost)" }],
        destination: "/",
        permanent: false,
      },
      {
        source: "/:path*",
        has: [{ type: "host", value: "app.dub.sh" }],
        destination: "https://app.dub.co/:path*",
        permanent: true,
      },
      {
        source: "/beta/:path*",
        has: [{ type: "cookie", key: "beta" }],
        destination: "/new/:path*",
        permanent: false,
      },
    ];
  },
  async headers() {
    return [{ source: "/:path*", headers: [{ key: "X-Frame-Options", value: "DENY" }] }];
  },
};
`,
		"app/api/links/route.ts",
		"app/beta/orders/page.tsx",
	))
	expectDoubts(t, got, map[string][]string{
		"/api/links app/api/links/route.ts": nil,
		"/beta/orders app/beta/orders/page.tsx": {
			"likely: next.config redirects /beta/:path* to /new/:path*, on requests that meet its conditions",
		},
	})
}
