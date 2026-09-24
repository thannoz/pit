package analysis_test

import (
	"context"
	"io/fs"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/thannoz/pit/internal/analysis"
	"github.com/thannoz/pit/internal/diff"
)

// snapshot is a tree from name/content pairs.
func snapshot(files ...string) fstest.MapFS {
	fsys := fstest.MapFS{}
	for i := 0; i+1 < len(files); i += 2 {
		fsys[files[i]] = &fstest.MapFile{Data: []byte(files[i+1])}
	}
	return fsys
}

// change is a changed file with its hunks, classified.
func change(path string, how diff.Change, hunks ...diff.Hunk) diff.File {
	return diff.File{Path: path, Change: how, Hunks: hunks}
}

func hunk(oldStart, oldCount, newStart, newCount int) diff.Hunk {
	return diff.Hunk{Old: diff.Range{Start: oldStart, Count: oldCount}, New: diff.Range{Start: newStart, Count: newCount}}
}

func warn(t *testing.T, base, head fs.FS, changes ...diff.File) []analysis.Warning {
	t.Helper()
	ws, err := analysis.Warnings(context.Background(), base, head,
		analysis.Classify(diff.Diff{Files: changes}, []string{"legacy/**"}), []analysis.Analyzer{htmlPages{}})
	if err != nil {
		t.Fatalf("Warnings: %v", err)
	}
	return ws
}

func messages(ws []analysis.Warning) []string {
	var out []string
	for _, w := range ws {
		mark := "  "
		if w.Serious {
			mark = "! "
		}
		out = append(out, mark+string(w.Kind)+": "+w.Message)
	}
	return out
}

func expectWarnings(t *testing.T, ws []analysis.Warning, want ...string) {
	t.Helper()
	if got := messages(ws); !slices.Equal(got, want) {
		t.Errorf("warnings\n got %s\nwant %s", strings.Join(got, "\n     "), strings.Join(want, "\n     "))
	}
}

// The criterion of T-608: a pull request with a migration says so, and
// says what the migration does.
func TestAMigrationIsCalledOut(t *testing.T) {
	sql := "-- AlterTable\nALTER TABLE \"ApiToken\" ADD COLUMN     \"lastUsedAt\" TIMESTAMP(3);\n"
	path := "packages/prisma/migrations/20260818081941_add_api_token_last_used_property/migration.sql"
	ws := warn(t, snapshot(), snapshot(path, sql), change(path, diff.Added, hunk(0, 0, 1, 2)))
	expectWarnings(t, ws,
		`  migration: `+path+` changes the schema (ALTER TABLE "ApiToken" ADD COLUMN "lastUsedAt" TIMESTAMP(3)); check the data that exists before it runs`)
}

func TestMigrationsThatCanLoseData(t *testing.T) {
	drop := "ALTER TABLE orders DROP COLUMN vat_id;\nDROP TABLE refunds;\n"
	old := "CREATE TABLE refunds (id int);\n"
	ws := warn(t,
		snapshot("db/migrations/0041_refunds.sql", old, "db/migrations/0040_init.sql", "CREATE TABLE a (id int);\n"),
		snapshot("db/migrations/0042_drop_vat.sql", drop, "db/migrations/0041_refunds.sql", old+"CREATE INDEX r ON refunds (id);\n"),
		change("db/migrations/0042_drop_vat.sql", diff.Added, hunk(0, 0, 1, 2)),
		change("db/migrations/0041_refunds.sql", diff.Modified, hunk(1, 0, 2, 1)),
		change("db/migrations/0040_init.sql", diff.Deleted, hunk(1, 1, 0, 0)),
	)
	expectWarnings(t, ws,
		"! migration: db/migrations/0040_init.sql is deleted; databases that already ran it keep what it did, and new ones will not get it",
		"! migration: db/migrations/0041_refunds.sql is an existing migration and is changed; databases that already ran it will not run it again",
		"! migration: db/migrations/0042_drop_vat.sql changes the schema (ALTER TABLE orders DROP COLUMN vat_id; DROP TABLE refunds); check the data that exists before it runs; it can destroy data: ALTER TABLE orders DROP, DROP TABLE at lines 1–2",
	)
}

// A data model without a migration next to it may be fine -- some
// projects push the schema -- but it is worth saying.
func TestADataModelWithoutAMigration(t *testing.T) {
	schema := "model Discount {\n  id String @id\n}\n"
	alone := warn(t, snapshot("prisma/schema.prisma", schema), snapshot("prisma/schema.prisma", schema),
		change("prisma/schema.prisma", diff.Modified, hunk(2, 1, 2, 1)))
	expectWarnings(t, alone, "  migration: the data model prisma/schema.prisma changes, and no migration comes with it; check the data the sandbox already has")

	with := warn(t, snapshot("prisma/schema.prisma", schema), snapshot("prisma/schema.prisma", schema, "prisma/migrations/1/migration.sql", "ALTER TABLE x ADD y int;\n"),
		change("prisma/schema.prisma", diff.Modified, hunk(2, 1, 2, 1)),
		change("prisma/migrations/1/migration.sql", diff.Added, hunk(0, 0, 1, 1)))
	if strings.Contains(strings.Join(messages(with), "\n"), "no migration comes with it") {
		t.Errorf("a migration came with it: %q", messages(with))
	}
}

// An address the old tree answers at and the new one does not.
func TestRemovedAddresses(t *testing.T) {
	base := snapshot("site/sponsor.html", "<h1>Sponsor</h1>", "site/about.html", "<h1>About</h1>", "site/stays.html", "x")
	head := snapshot("site/partners.html", "<h1>Partners</h1>", "site/stays.html", "y")
	moved := change("site/partners.html", diff.Renamed, hunk(1, 1, 1, 1))
	moved.OldPath = "site/sponsor.html"

	ws := warn(t, base, head,
		moved,
		change("site/about.html", diff.Deleted, hunk(1, 1, 0, 0)),
		change("site/stays.html", diff.Modified, hunk(1, 1, 1, 1)),
	)
	expectWarnings(t, ws,
		"  removed: /about no longer answers: site/about.html is deleted",
		"  removed: /sponsor no longer answers: site/sponsor.html moved to site/partners.html, which answers at /partners",
	)

	// Without the old tree, there is nothing to compare.
	if ws := warn(t, nil, head, change("site/about.html", diff.Deleted, hunk(1, 1, 0, 0))); len(ws) != 0 {
		t.Errorf("warnings without a base: %q", messages(ws))
	}
}

func TestPermissionChecks(t *testing.T) {
	before := "export const DELETE = withWorkspace(async ({ group }) => {\n  if (group.slug === DEFAULT) {\n    throw new DubApiError({ code: \"forbidden\" });\n  }\n  await remove(group);\n});\n"
	after := "export const DELETE = withWorkspace(async ({ group }) => {\n  await deletePartnerGroup(group);\n});\n"
	helper := "export async function deletePartnerGroup(group) {\n  if (group.slug === DEFAULT) {\n    throw new DubApiError({ code: \"forbidden\" });\n  }\n}\n"
	ws := warn(t,
		snapshot("app/api/groups/route.ts", before, "lib/format.ts", "if (isAdmin(user)) {\n"),
		snapshot("app/api/groups/route.ts", after, "lib/delete-group.ts", helper, "lib/format.ts", "if (isAdmin(user))  {\n",
			"lib/chat.go", "if msg.Role == \"assistant\" {\n", "app/aria.tsx", "<div role=\"button\">\n"),
		change("app/api/groups/route.ts", diff.Modified, hunk(2, 4, 2, 1)),
		change("lib/delete-group.ts", diff.Added, hunk(0, 0, 1, 5)),
		// Reformatted: the check is on both sides, and is not removed.
		change("lib/format.ts", diff.Modified, hunk(1, 1, 1, 1)),
		// Roles that have nothing to do with access.
		change("lib/chat.go", diff.Added, hunk(0, 0, 1, 1)),
		change("app/aria.tsx", diff.Added, hunk(0, 0, 1, 1)),
	)
	var perms []string
	for _, m := range messages(ws) {
		if strings.Contains(m, "permissions:") {
			perms = append(perms, m)
		}
	}
	want := []string{
		"! permissions: app/api/groups/route.ts removes permission checks: authorization, at lines 3 as they were; the change adds the same kind of check in lib/delete-group.ts",
		"  permissions: lib/delete-group.ts changes permission checks: authorization, at lines 3",
		"  permissions: lib/format.ts changes permission checks: admin check, at lines 1",
	}
	if !slices.Equal(perms, want) {
		t.Errorf("permission warnings\n got %s\nwant %s", strings.Join(perms, "\n     "), strings.Join(want, "\n     "))
	}
}

func TestErrorHandling(t *testing.T) {
	var long strings.Builder
	for range 10 {
		long.WriteString("if err != nil {\n\treturn err\n}\n")
	}
	ws := warn(t, snapshot(),
		snapshot("server/new.go", long.String(), "app/page.tsx", "try {\n  load();\n} catch (e) {\n  notFound();\n}\n"),
		change("server/new.go", diff.Added, hunk(0, 0, 1, 30)),
		change("app/page.tsx", diff.Added, hunk(0, 0, 1, 5)),
	)
	expectWarnings(t, ws,
		"  errors: app/page.tsx changes error handling: try/catch, error status, at lines 1, 3–4",
		// Past a handful of places, the count.
		"  errors: server/new.go changes error handling: err check, at lines 10 places, the first 1",
	)
}

// Tests, docs and what review.ignore excludes are not the reviewer's
// business here either.
func TestOnlyChecklistFilesWarn(t *testing.T) {
	src := "if (!isAdmin(user)) throw new Error('forbidden');\n"
	ws := warn(t, snapshot(),
		snapshot("app/page.test.tsx", src, "docs/auth.md", src, "legacy/old.ts", src),
		change("app/page.test.tsx", diff.Added, hunk(0, 0, 1, 1)),
		change("docs/auth.md", diff.Added, hunk(0, 0, 1, 1)),
		change("legacy/old.ts", diff.Added, hunk(0, 0, 1, 1)),
	)
	if len(ws) != 0 {
		t.Errorf("warnings for files off the checklist: %q", messages(ws))
	}
}
