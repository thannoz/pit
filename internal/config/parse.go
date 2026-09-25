package config

import (
	"time"

	"gopkg.in/yaml.v3"

	"github.com/thannoz/pit/internal/errs"
)

// Defaults that apply when a field is left out. They are the values a
// team would have written anyway, so a minimal .pit.yaml stays minimal.
const (
	DefaultComposeFile     = "docker-compose.yml"
	DefaultExpectStatus    = 200
	DefaultHealthTimeout   = 120 * time.Second
	DefaultHealthInterval  = 2 * time.Second
	DefaultHealthURL       = "http://{host}:{port}/"
	DefaultRoutesFramework = "auto"
	DefaultProductionTTL   = 24 * time.Hour
	DefaultSnapshotLimit   = 500 * ByteSize(1e6)
)

// Parse reads a .pit.yaml and fills in the defaults. It does not check
// whether the result makes sense; that is validation, and it happens
// separately so the error messages can be about meaning rather than
// syntax.
func Parse(data []byte) (*Config, error) {
	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, errs.Wrap(err, "cannot read .pit.yaml").
			WithHint("check the file's indentation and quoting")
	}

	c.applyDefaults()
	return &c, nil
}

// applyDefaults fills every field that was left out. It runs after
// decoding, so an explicit value always wins.
func (c *Config) applyDefaults() {
	if c.Version == 0 {
		c.Version = Version
	}
	if len(c.Compose.Files) == 0 {
		c.Compose.Files = []string{DefaultComposeFile}
	}

	h := &c.Healthcheck
	if h.URL == "" {
		h.URL = DefaultHealthURL
	}
	if h.ExpectStatus == 0 {
		h.ExpectStatus = DefaultExpectStatus
	}
	if h.Timeout.IsZero() {
		h.Timeout = Duration(DefaultHealthTimeout)
	}
	if h.Interval.IsZero() {
		h.Interval = Duration(DefaultHealthInterval)
	}

	if c.Review.Routes.Framework == "" {
		c.Review.Routes.Framework = DefaultRoutesFramework
	}

	// Only meaningful once a dump is configured; defaulting it
	// otherwise would suggest a feature that is not in use.
	if c.Data.ProductionLike.Fetch != "" && c.Data.ProductionLike.TTL.IsZero() {
		c.Data.ProductionLike.TTL = Duration(DefaultProductionTTL)
	}
}
