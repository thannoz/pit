package config

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// canonical loads the example that mirrors docs/02-architektur.md.
func canonical(t *testing.T) *Config {
	t.Helper()

	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "config", "full.yaml"))
	if err != nil {
		t.Fatalf("reading the canonical example: %v", err)
	}
	c, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return c
}

// TestCanonicalExampleParses is the acceptance criterion for T-201: the
// format the documentation promises has to be the format the code
// accepts, field for field.
func TestCanonicalExampleParses(t *testing.T) {
	c := canonical(t)

	t.Run("version", func(t *testing.T) {
		if c.Version != 1 {
			t.Errorf("Version = %d, want 1", c.Version)
		}
	})

	t.Run("compose", func(t *testing.T) {
		want := []string{"docker-compose.yml", "docker-compose.pit.yml"}
		if !slices.Equal(c.Compose.Files, want) {
			t.Errorf("Compose.Files = %v, want %v", c.Compose.Files, want)
		}
		if services := []string{"web", "api"}; !slices.Equal(c.Compose.Services, services) {
			t.Errorf("Compose.Services = %v, want %v", c.Compose.Services, services)
		}
	})

	t.Run("web", func(t *testing.T) {
		if c.Web.Service != "web" || c.Web.Port != 3000 {
			t.Errorf("Web = %+v, want {web 3000}", c.Web)
		}
	})

	t.Run("healthcheck", func(t *testing.T) {
		h := c.Healthcheck
		if h.URL != "http://{host}:{port}/" {
			t.Errorf("URL = %q", h.URL)
		}
		if h.ExpectStatus != 200 {
			t.Errorf("ExpectStatus = %d, want 200", h.ExpectStatus)
		}
		if h.Timeout.Duration() != 120*time.Second {
			t.Errorf("Timeout = %v, want 2m0s", h.Timeout)
		}
		if h.Interval.Duration() != 2*time.Second {
			t.Errorf("Interval = %v, want 2s", h.Interval)
		}
	})

	t.Run("hooks", func(t *testing.T) {
		if len(c.Hooks.AfterUp) != 1 {
			t.Fatalf("AfterUp = %v, want one command", c.Hooks.AfterUp)
		}
		if c.Hooks.AfterUp[0] != "compose exec -T api npm ci" {
			t.Errorf("AfterUp[0] = %q", c.Hooks.AfterUp[0])
		}
	})

	t.Run("build", func(t *testing.T) {
		if c.Build.Prebuilt != "ghcr.io/acme/shop-{service}:{sha}" {
			t.Errorf("Prebuilt = %q", c.Build.Prebuilt)
		}
	})

	t.Run("data", func(t *testing.T) {
		d := c.Data
		if d.Service != "db" {
			t.Errorf("Service = %q, want db", d.Service)
		}
		if len(d.Migrate) != 1 || d.Migrate[0] != "compose exec -T api npm run migrate" {
			t.Errorf("Migrate = %v, want the one migration command", d.Migrate)
		}
		if d.Default != "standard" {
			t.Errorf("Default = %q, want standard", d.Default)
		}
		if d.Snapshot.Save == "" || d.Snapshot.Restore == "" {
			t.Errorf("Snapshot = %+v, want both commands", d.Snapshot)
		}
		if want := []string{"leer", "standard", "teilerstattung"}; !slices.Equal(c.ScenarioNames(), want) {
			t.Errorf("ScenarioNames() = %v, want %v", c.ScenarioNames(), want)
		}
		if d.ProductionLike.TTL.Duration() != 24*time.Hour {
			t.Errorf("ProductionLike.TTL = %v, want 24h0m0s", d.ProductionLike.TTL)
		}
	})

	t.Run("scenario inheritance", func(t *testing.T) {
		s, ok := c.Scenario("teilerstattung")
		if !ok {
			t.Fatal("the scenario is missing")
		}
		if s.Extends != "standard" {
			t.Errorf("Extends = %q, want standard", s.Extends)
		}
		if len(s.Apply) != 1 {
			t.Errorf("Apply = %v, want one command", s.Apply)
		}
		params, err := c.Params("teilerstattung")
		if err != nil {
			t.Fatalf("Params: %v", err)
		}
		if want := map[string]string{"id": "1042", "slug": "acme"}; !maps.Equal(params, want) {
			t.Errorf("Params = %v, want %v", params, want)
		}
	})

	t.Run("review", func(t *testing.T) {
		if c.Review.Routes.Framework != "nextjs" {
			t.Errorf("Framework = %q, want nextjs", c.Review.Routes.Framework)
		}
		if want := []string{"**/*.test.ts", "docs/**"}; !slices.Equal(c.Review.Ignore, want) {
			t.Errorf("Ignore = %v, want %v", c.Review.Ignore, want)
		}
	})

	t.Run("env", func(t *testing.T) {
		if c.Env.FromFile != ".env.pit.example" {
			t.Errorf("FromFile = %q", c.Env.FromFile)
		}
		// A quoted "1" must stay a string; unquoted it would become a
		// number and reach the container as something else.
		if got := c.Env.Set["TELEMETRY_DISABLED"]; got != "1" {
			t.Errorf("TELEMETRY_DISABLED = %q, want %q", got, "1")
		}
	})
}

func TestScenarioLookupMisses(t *testing.T) {
	if _, ok := canonical(t).Scenario("does-not-exist"); ok {
		t.Error("an unknown scenario was reported as found")
	}
}

// TestMinimalConfigGetsUsefulDefaults covers the file a team writes on
// day one: a couple of lines, and pit fills in the rest.
func TestMinimalConfigGetsUsefulDefaults(t *testing.T) {
	c, err := Parse([]byte("web:\n  service: web\n  port: 3000\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	tests := []struct {
		name string
		got  any
		want any
	}{
		{"version", c.Version, Version},
		{"compose files", len(c.Compose.Files), 1},
		{"first compose file", c.Compose.Files[0], DefaultComposeFile},
		{"healthcheck url", c.Healthcheck.URL, DefaultHealthURL},
		{"expected status", c.Healthcheck.ExpectStatus, DefaultExpectStatus},
		{"timeout", c.Healthcheck.Timeout.Duration(), DefaultHealthTimeout},
		{"interval", c.Healthcheck.Interval.Duration(), DefaultHealthInterval},
		{"routes framework", c.Review.Routes.Framework, DefaultRoutesFramework},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("got %v, want %v", tt.got, tt.want)
			}
		})
	}
}

func TestExplicitValuesBeatDefaults(t *testing.T) {
	c, err := Parse([]byte("healthcheck:\n  expect_status: 204\n  timeout: 5s\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if c.Healthcheck.ExpectStatus != 204 {
		t.Errorf("ExpectStatus = %d, want the configured 204", c.Healthcheck.ExpectStatus)
	}
	if c.Healthcheck.Timeout.Duration() != 5*time.Second {
		t.Errorf("Timeout = %v, want the configured 5s", c.Healthcheck.Timeout)
	}
	// Untouched neighbours still get their defaults.
	if c.Healthcheck.Interval.Duration() != DefaultHealthInterval {
		t.Errorf("Interval = %v, want the default", c.Healthcheck.Interval)
	}
}

func TestProductionTTLOnlyDefaultsWhenADumpIsConfigured(t *testing.T) {
	// Defaulting it unconditionally would suggest a feature is in use
	// when it is not.
	withoutDump, err := Parse([]byte("data:\n  service: db\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !withoutDump.Data.ProductionLike.TTL.IsZero() {
		t.Errorf("TTL = %v with no dump configured, want unset", withoutDump.Data.ProductionLike.TTL)
	}

	withDump, err := Parse([]byte("data:\n  production_like:\n    fetch: \"cat dump.sql\"\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if withDump.Data.ProductionLike.TTL.Duration() != DefaultProductionTTL {
		t.Errorf("TTL = %v, want the default", withDump.Data.ProductionLike.TTL)
	}
}

func TestParseReportsBrokenYAML(t *testing.T) {
	_, err := Parse([]byte("web:\n  service: web\n port: nope\n"))
	if err == nil {
		t.Fatal("want an error for malformed YAML")
	}
}
