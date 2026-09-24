package routes

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/thannoz/pit/internal/analysis"
	"github.com/thannoz/pit/internal/diff"
)

func tsLinks(t *testing.T, files map[string]string) []analysis.Link {
	t.Helper()
	fsys := fstest.MapFS{}
	for name, src := range files {
		fsys[name] = &fstest.MapFile{Data: []byte(src)}
	}
	links, err := TypeScript{}.Links(context.Background(), fsys)
	if err != nil {
		t.Fatalf("Links: %v", err)
	}
	return links
}

func rng(r diff.Range) string {
	if r == (diff.Range{}) {
		return "*"
	}
	return fmt.Sprintf("%d-%d", r.Start, r.End())
}

// edges are the links as "from:at -> to:target", sorted.
func edges(links []analysis.Link) []string {
	var out []string
	for _, l := range links {
		out = append(out, fmt.Sprintf("%s:%s -> %s:%s", l.From, rng(l.At), l.To, rng(l.Target)))
	}
	slices.Sort(out)
	return out
}

// files are the file-to-file pairs, whatever the lines.
func files(links []analysis.Link) []string {
	var out []string
	for _, l := range links {
		if l.From == l.To {
			continue
		}
		e := l.From + " -> " + l.To
		if !slices.Contains(out, e) {
			out = append(out, e)
		}
	}
	slices.Sort(out)
	return out
}

func expectFiles(t *testing.T, links []analysis.Link, want ...string) {
	t.Helper()
	slices.Sort(want)
	if got := files(links); !slices.Equal(got, want) {
		t.Errorf("links\n got %s\nwant %s", strings.Join(got, "\n     "), strings.Join(want, "\n     "))
	}
}

// Every way a bundler finds a file from a relative import.
func TestRelativeImportsAreResolved(t *testing.T) {
	links := tsLinks(t, map[string]string{
		"src/app.tsx": `import { a } from "./lib/a";
import { b } from "./lib/b.js";
import { c } from "./lib/c";
import styles from "./app.module.css";
import logo from "./logo.svg?react";
import { missing } from "./nowhere";
import React from "react";
import { readFile } from "node:fs";
const Lazy = React.lazy(() => import("./lazy"));

export function App() {
  return a + b + c + styles + logo + missing + readFile + Lazy;
}
`,
		"src/lib/a.ts":        "export const a = 1;\n",
		"src/lib/b.ts":        "export const b = 2;\n",
		"src/lib/c/index.tsx": "export const c = 3;\n",
		"src/app.module.css":  ".x {}\n",
		"src/logo.svg":        "<svg/>\n",
		"src/lazy.tsx":        "export default function Lazy() {}\n",
	})
	expectFiles(t, links,
		"src/app.tsx -> src/lib/a.ts",
		"src/app.tsx -> src/lib/b.ts", // "./lib/b.js" in TypeScript is b.ts
		"src/app.tsx -> src/lib/c/index.tsx",
		"src/app.tsx -> src/app.module.css",
		"src/app.tsx -> src/logo.svg",
		"src/app.tsx -> src/lazy.tsx",
	)
}

// A name leads to the declaration it names, from the lines it is used
// on. That is what keeps a new helper next to an old one from reaching
// everything the old one reaches.
func TestANameLeadsToItsDeclaration(t *testing.T) {
	links := tsLinks(t, map[string]string{
		"lib/log.ts": `import { db } from "./db";

export const trackActivityLog = async (input: string) => {
  await db.write(input);
};

// Same as trackActivityLog, but inside a transaction.
export const trackActivityLogsTx = async (tx: unknown) => {
  return tx;
};
`,
		"lib/db.ts": "export const db = { write: async (s: string) => s };\n",
		"app/a/route.ts": `import { trackActivityLog } from "@/lib/log";

export async function POST() {
  await trackActivityLog("a");
}
`,
		"app/b/route.ts": `import { trackActivityLogsTx, trackActivityLog } from "@/lib/log";

export async function POST() {
  await trackActivityLogsTx(null);
}
`,
		"tsconfig.json": `{ "compilerOptions": { "paths": { "@/*": ["./*"] } } }`,
	})
	want := []string{
		// The comment above a declaration is part of it.
		"app/a/route.ts:4-4 -> lib/log.ts:3-5",
		"app/b/route.ts:4-4 -> lib/log.ts:7-10",
		// Imported and never used is no use.
		"lib/log.ts:4-4 -> lib/db.ts:1-1",
	}
	if got := edges(links); !slices.Equal(got, want) {
		t.Errorf("links\n got %s\nwant %s", strings.Join(got, "\n     "), strings.Join(want, "\n     "))
	}
}

// A namespace import leads each use to what it names: api.del to del,
// not to everything api.js declares. sveltejs/realworld imports its API
// client this way in every route; by file, a change to del reached the
// login form. Passing the namespace on as a whole uses the whole file.
func TestANamespaceMemberLeadsToItsDeclaration(t *testing.T) {
	links := tsLinks(t, map[string]string{
		"src/lib/api.js": `export function get(path) {
	return fetch(path);
}

export function del(path) {
	return fetch(path, { method: 'DELETE' });
}
`,
		"src/routes/login.js": `import * as api from './../lib/api.js';

export const load = () => api.get('user');
`,
		"src/routes/article.js": `import * as api from '../lib/api.js';

export const remove = () => api.del('a');
export const either = () => api ?.get('b') ?? api?.del('c');
export const client = () => wrap(api);
`,
	})
	want := []string{
		"src/routes/article.js:3-3 -> src/lib/api.js:5-7",
		"src/routes/article.js:4-4 -> src/lib/api.js:1-3",
		"src/routes/article.js:4-4 -> src/lib/api.js:5-7",
		"src/routes/article.js:5-5 -> src/lib/api.js:*",
		"src/routes/login.js:3-3 -> src/lib/api.js:1-3",
	}
	if got := edges(links); !slices.Equal(got, want) {
		t.Errorf("links\n got %s\nwant %s", strings.Join(got, "\n     "), strings.Join(want, "\n     "))
	}
}

// A declaration a file uses from itself is a step too: a table that
// changed reaches the exported function that reads it.
func TestDeclarationsOfOneFileUseEachOther(t *testing.T) {
	links := tsLinks(t, map[string]string{
		"lib/workflows.ts": `const workflowPathMap = {
  "a-workflow": "/api/workflows/a",
} as const;

export async function sendWorkflow(name: keyof typeof workflowPathMap) {
  return workflowPathMap[name];
}
`,
	})
	// The signature names it too; both lines are inside sendWorkflow.
	want := []string{
		"lib/workflows.ts:5-5 -> lib/workflows.ts:1-3",
		"lib/workflows.ts:6-6 -> lib/workflows.ts:1-3",
	}
	if got := edges(links); !slices.Equal(got, want) {
		t.Errorf("links %v, want %v", got, want)
	}
}

// Barrel files re-export; an import through one leads to where the
// declaration is, not to the barrel.
func TestImportsThroughBarrels(t *testing.T) {
	links := tsLinks(t, map[string]string{
		"ui/index.ts": `export { Button } from "./button";
export * from "./card";
export * as icons from "./icons";
`,
		"ui/button.tsx": "export function Button() {\n  return null;\n}\n",
		"ui/card.tsx":   "export function Card() {\n  return null;\n}\n",
		"ui/icons.tsx":  "export function Plus() {\n  return null;\n}\n",
		"app/page.tsx": `import { Button, Card, icons } from "../ui";

export default function Page() {
  return [Button, Card, icons.Plus];
}
`,
	})
	want := []string{
		"app/page.tsx:4-4 -> ui/button.tsx:1-3",
		"app/page.tsx:4-4 -> ui/card.tsx:1-3",
		"app/page.tsx:4-4 -> ui/icons.tsx:*", // a namespace is the whole module
		// The barrel passes on what it re-exports, for the imports
		// that end at it.
		"ui/index.ts:* -> ui/button.tsx:1-3",
		"ui/index.ts:* -> ui/card.tsx:*",
		"ui/index.ts:* -> ui/icons.tsx:*",
	}
	if got := edges(links); !slices.Equal(got, want) {
		t.Errorf("links\n got %s\nwant %s", strings.Join(got, "\n     "), strings.Join(want, "\n     "))
	}
}

// Monorepos: aliases from a tsconfig that extends a shared one, written
// with comments and trailing commas, and packages of the same repository
// whose entry points at a dist/ that has not been built.
func TestMonorepoResolution(t *testing.T) {
	links := tsLinks(t, map[string]string{
		"packages/tsconfig/nextjs.json": `{
  // shared settings
  "compilerOptions": { "strict": true, },
}`,
		"packages/tsconfig/package.json": `{"name": "tsconfig"}`,
		"apps/web/tsconfig.json": `{
  "extends": "tsconfig/nextjs.json",
  "compilerOptions": {
    "baseUrl": ".",
    /* aliases */
    "paths": {
      "@/lib/*": ["lib/*"],
      "@/ui/*": ["ui/*"],
    },
  },
}`,
		"apps/web/app/page.tsx": `import { format } from "@/lib/format";
import { Modal } from "@/ui/modal";
import { Button } from "@acme/ui";
import { Plus } from "@acme/ui/icons";
import { cn } from "@acme/utils";

export default function Page() {
  return [format, Modal, Button, Plus, cn];
}
`,
		"apps/web/lib/format.ts":         "export function format() {}\n",
		"apps/web/ui/modal.tsx":          "export function Modal() {}\n",
		"packages/ui/package.json":       `{"name": "@acme/ui", "main": "./dist/index.js", "exports": {".": {"types": "./dist/index.d.ts", "import": "./dist/index.mjs"}, "./icons": "./src/icons/index.ts"}}`,
		"packages/ui/src/index.tsx":      "export function Button() {}\n",
		"packages/ui/src/icons/index.ts": "export function Plus() {}\n",
		"packages/utils/package.json":    `{"name": "@acme/utils"}`,
		"packages/utils/src/index.ts":    "export function cn() {}\n",
		"node_modules/react/index.js":    "module.exports = {}\n",
		// Nx and friends keep the aliases in a base file the apps extend.
		"tsconfig.base.json":       `{"compilerOptions": {"baseUrl": ".", "paths": {"@shared/*": ["libs/shared/*"]}}}`,
		"apps/admin/tsconfig.json": `{"extends": "../../tsconfig.base.json"}`,
		"apps/admin/app/page.tsx": `import { money } from "@shared/money";
import { Dialog } from "@acme/kit/dialog";

export default function Page() {
  return [money, Dialog];
}
`,
		"libs/shared/money.ts": "export function money() {}\n",
		// An export that points into dist/ at a name src/ does not
		// share with the import.
		"packages/kit/package.json":              `{"name": "@acme/kit", "exports": {"./dialog": "./dist/components/dialog.js"}}`,
		"packages/kit/src/components/dialog.tsx": "export function Dialog() {}\n",
	})
	expectFiles(t, links,
		"apps/web/app/page.tsx -> apps/web/lib/format.ts",
		"apps/web/app/page.tsx -> apps/web/ui/modal.tsx",
		"apps/web/app/page.tsx -> packages/ui/src/index.tsx",
		"apps/web/app/page.tsx -> packages/ui/src/icons/index.ts",
		"apps/web/app/page.tsx -> packages/utils/src/index.ts",
		"apps/admin/app/page.tsx -> libs/shared/money.ts",
		"apps/admin/app/page.tsx -> packages/kit/src/components/dialog.tsx",
	)
}

// What does not change a page leads nowhere.
func TestTypesAreNotUses(t *testing.T) {
	links := tsLinks(t, map[string]string{
		"lib/types.ts": "export type Order = { id: string };\nexport const zero = 0;\nexport interface Item { n: number }\nexport function total(i: Item) {\n  return i.n;\n}\n",
		"app/page.tsx": `import type { Order } from "../lib/types";
import { type Order as O, zero } from "../lib/types";
import { Item } from "../lib/types";

export default function Page(o: Order, p: O, i: Item) {
  return zero;
}
`,
	})
	// Only zero: a type imported without the keyword is still a type,
	// and total naming Item in its signature is not a use either.
	if got, want := edges(links), []string{"app/page.tsx:6-6 -> lib/types.ts:2-2"}; !slices.Equal(got, want) {
		t.Errorf("links %v, want %v", got, want)
	}
}

// Components written in a template language are one piece: their
// importers use all of them.
func TestSingleFileComponentsAreOnePiece(t *testing.T) {
	links := tsLinks(t, map[string]string{
		"src/Price.vue":   "<script setup>\nimport { format } from './format'\n</script>\n<template>{{ format(1) }}</template>\n",
		"src/format.ts":   "export function format(n: number) {\n  return n;\n}\n",
		"src/pages/a.vue": "<script setup>\nimport Price from '../Price.vue'\n</script>\n<template><Price /></template>\n",
	})
	want := []string{
		"src/Price.vue:4-4 -> src/format.ts:1-3",
		"src/pages/a.vue:4-4 -> src/Price.vue:*",
	}
	if got := edges(links); !slices.Equal(got, want) {
		t.Errorf("links\n got %s\nwant %s", strings.Join(got, "\n     "), strings.Join(want, "\n     "))
	}
}

func TestJSONWithComments(t *testing.T) {
	in := `{
  // a comment with "quotes" and a // inside
  "a": "http://example.com/*not a comment*/", /* block */
  "b": [1, 2,],
}`
	var got map[string]any
	if err := json.Unmarshal(jsonc([]byte(in)), &got); err != nil {
		t.Fatalf("not JSON after stripping: %v\n%s", err, jsonc([]byte(in)))
	}
	if got["a"] != "http://example.com/*not a comment*/" {
		t.Errorf("a string was changed: %q", got["a"])
	}
	if b, _ := got["b"].([]any); len(b) != 2 {
		t.Errorf("b = %v", got["b"])
	}
}
