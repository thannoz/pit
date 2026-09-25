package config

import (
	"strings"
	"testing"
)

var promoted = Scenario{
	Name:        "voucher",
	Description: "Saved in #482 at 1a2b3c4",
	Snapshot:    ScenarioSnapshot{File: "fixtures/voucher.sql"},
}

func TestAddScenarioEditsOnlyWhereItAdds(t *testing.T) {
	const head = "version: 1\n\nweb:\n  service: web\n  port: 8080\n\n"
	cases := []struct {
		name, src, want string
	}{
		{
			name: "after the last scenario, before what follows it",
			src: head + `data:
  # Scenarios, smallest first.
  scenarios:
    - name: empty
      description: "Only the schema"
    - name: standard
      apply: ["compose exec -T db psql -f /fixtures/standard.sql"]   # three orders
      params:
        id: "1001"
  default: standard

  # pit snap save writes what save prints.
  snapshot:
    save: >-
      compose exec -T db pg_dump
    restore: compose exec -T db psql
`,
			want: head + `data:
  # Scenarios, smallest first.
  scenarios:
    - name: empty
      description: "Only the schema"
    - name: standard
      apply: ["compose exec -T db psql -f /fixtures/standard.sql"]   # three orders
      params:
        id: "1001"
    - name: voucher
      description: "Saved in #482 at 1a2b3c4"
      snapshot: fixtures/voucher.sql
  default: standard

  # pit snap save writes what save prints.
  snapshot:
    save: >-
      compose exec -T db pg_dump
    restore: compose exec -T db psql
`,
		},
		{
			name: "a list at the indentation of its key",
			src: head + `data:
  scenarios:
  - name: standard
    apply:
    - compose exec -T db psql -f /fixtures/standard.sql
  default: standard
`,
			want: head + `data:
  scenarios:
  - name: standard
    apply:
    - compose exec -T db psql -f /fixtures/standard.sql
  - name: voucher
    description: "Saved in #482 at 1a2b3c4"
    snapshot: fixtures/voucher.sql
  default: standard
`,
		},
		{
			name: "scenarios kept apart by blank lines, the last one a folded block with a blank line in it",
			src: head + `data:
    scenarios:
        -   name: empty

        -   name: standard
            description: >
                Three orders,

                one refunded
# the end
`,
			want: head + `data:
    scenarios:
        -   name: empty

        -   name: standard
            description: >
                Three orders,

                one refunded

        -   name: voucher
            description: "Saved in #482 at 1a2b3c4"
            snapshot: fixtures/voucher.sql
# the end
`,
		},
		{
			name: "data without scenarios",
			src: head + `data:
  migrate: ["compose exec -T db migrate"]
  snapshot:
    save: compose exec -T db pg_dump
    restore: compose exec -T db psql
    # a comment that belongs to the snapshot

# Hooks run after up.
hooks:
  after_up: []
`,
			want: head + `data:
  migrate: ["compose exec -T db migrate"]
  snapshot:
    save: compose exec -T db pg_dump
    restore: compose exec -T db psql
    # a comment that belongs to the snapshot
  scenarios:
    - name: voucher
      description: "Saved in #482 at 1a2b3c4"
      snapshot: fixtures/voucher.sql

# Hooks run after up.
hooks:
  after_up: []
`,
		},
		{
			name: "no data at all, and no newline at the end",
			src:  strings.TrimSuffix(head, "\n\n"),
			want: strings.TrimSuffix(head, "\n") + `
data:
  scenarios:
    - name: voucher
      description: "Saved in #482 at 1a2b3c4"
      snapshot: fixtures/voucher.sql
`,
		},
		{
			name: "an empty data",
			src:  head + "data:\n# nothing yet\nhooks: {}\n",
			want: head + "data:\n  scenarios:\n    - name: voucher\n      description: \"Saved in #482 at 1a2b3c4\"\n      snapshot: fixtures/voucher.sql\n# nothing yet\nhooks: {}\n",
		},
		{
			name: "an empty list of scenarios",
			src:  head + "data:\n  scenarios:\n  default: \"\"\n",
			want: head + "data:\n  scenarios:\n    - name: voucher\n      description: \"Saved in #482 at 1a2b3c4\"\n      snapshot: fixtures/voucher.sql\n  default: \"\"\n",
		},
		{
			name: "Windows line endings",
			src:  strings.ReplaceAll(head+"data:\n  scenarios:\n    - name: empty\n", "\n", "\r\n"),
			want: strings.ReplaceAll(head+"data:\n  scenarios:\n    - name: empty\n    - name: voucher\n      description: \"Saved in #482 at 1a2b3c4\"\n      snapshot: fixtures/voucher.sql\n", "\n", "\r\n"),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := AddScenario([]byte(tc.src), promoted)
			if err != nil {
				t.Fatalf("AddScenario: %v", err)
			}
			if string(got) != tc.want {
				t.Errorf("got:\n%s\nwant:\n%s", got, tc.want)
			}
			c, err := Parse(got)
			if err != nil {
				t.Fatal(err)
			}
			if sc, ok := c.Scenario("voucher"); !ok || sc.Snapshot.File != "fixtures/voucher.sql" {
				t.Errorf("the scenario did not read back: %+v", sc)
			}
		})
	}
}

func TestAddScenarioWritesWhatReadsBack(t *testing.T) {
	src := "version: 1\nweb: {service: web, port: 80}\ndata:\n  scenarios:\n    - name: empty\n"
	sc := Scenario{
		Name:        "two-warehouses",
		Description: `The "big" one: 2 databases, # not a comment`,
		Snapshot: ScenarioSnapshot{Files: []SnapshotFile{
			{Service: "orders", File: "fixtures/two-warehouses.orders.sql"},
			{Service: "stock", File: "fixtures/two warehouses.stock.sql"},
		}},
		Params: map[string]string{"id": "007", "flag": "yes", "slug": "a: b"},
	}
	got, err := AddScenario([]byte(src), sc)
	if err != nil {
		t.Fatalf("AddScenario: %v\n", err)
	}
	c, err := Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	back, _ := c.Scenario(sc.Name)
	if !sameScenario(back, sc) {
		t.Errorf("read back as %+v, want %+v\nfile:\n%s", back, sc, got)
	}
}

func TestAddScenarioRefusesWhatItCannotEditSafely(t *testing.T) {
	cases := map[string]struct{ src, why string }{
		"flow list":          {"version: 1\ndata:\n  scenarios: [{name: empty}]\n", "not a list written in block style"},
		"flow data":          {"version: 1\ndata: {scenarios: [{name: empty}]}\n", "data is not a mapping written in block style"},
		"two documents":      {"version: 1\n---\nversion: 1\n", "more than one document"},
		"a document end":     {"version: 1\n...\n", "more than one document"},
		"flow last scenario": {"version: 1\ndata:\n  scenarios:\n    - {name: empty}\n", "last scenario is not a mapping written in block style"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := AddScenario([]byte(tc.src), promoted)
			if err == nil || !strings.Contains(err.Error(), "cannot add the scenario") || !strings.Contains(err.Error(), tc.why) {
				t.Errorf("got %v, want a refusal saying %s", err, tc.why)
			}
		})
	}

	// The line after a kept block scalar's text is still part of it.
	// Inserting there would change the description; reading the
	// result back is what notices.
	t.Run("a block that keeps its trailing blank line", func(t *testing.T) {
		src := "version: 1\ndata:\n  scenarios:\n    - name: empty\n      description: |+\n        Nothing.\n\n  default: empty\n"
		_, err := AddScenario([]byte(src), promoted)
		if err == nil || !strings.Contains(err.Error(), "changed more than the new scenario") {
			t.Errorf("got %v, want a refusal", err)
		}
	})

	t.Run("a name that is taken", func(t *testing.T) {
		_, err := AddScenario([]byte("data:\n  scenarios:\n    - name: voucher\n"), promoted)
		if err == nil || !strings.Contains(err.Error(), "already") {
			t.Errorf("got %v", err)
		}
	})
}

func TestScenarioYAMLIsWhatAddScenarioWrites(t *testing.T) {
	want := "    - name: voucher\n      description: \"Saved in #482 at 1a2b3c4\"\n      snapshot: fixtures/voucher.sql\n"
	if got := ScenarioYAML(promoted); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}
