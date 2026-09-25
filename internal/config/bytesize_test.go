package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestParseByteSize(t *testing.T) {
	for in, want := range map[string]ByteSize{
		"500MB": 500e6, "2GB": 2e9, "1.5 GB": 1.5e9, "750kb": 750e3, "10B": 10, "0": 0,
	} {
		if got, err := ParseByteSize(in); err != nil || got != want {
			t.Errorf("ParseByteSize(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"500", "MB", "-1GB", "5 parsecs", "", "1e3MB"} {
		if _, err := ParseByteSize(in); err == nil {
			t.Errorf("ParseByteSize(%q) accepted", in)
		}
	}
	for size, want := range map[ByteSize]string{500e6: "500MB", 1.5e9: "1.5GB", 999: "999B"} {
		if got := size.String(); got != want {
			t.Errorf("%d: %s, want %s", size, got, want)
		}
	}
}

func TestSnapshotLimit(t *testing.T) {
	load := func(line string) (*Config, error) {
		src := "version: 1\nweb: {service: web, port: 80}\ndata:\n" + line
		root := project(t, map[string]string{FileName: src})
		return Load(filepath.Join(root, FileName))
	}
	c, err := load("  migrate: []\n")
	if err != nil || c.Data.SnapshotMax() != 500e6 {
		t.Errorf("default: %d, %v", c.Data.SnapshotMax(), err)
	}
	if c, err = load("  snapshot_limit: 2GB\n"); err != nil || c.Data.SnapshotMax() != 2e9 {
		t.Errorf("2GB: %v", err)
	}
	if c, err = load("  snapshot_limit: 0\n"); err != nil || c.Data.SnapshotMax() != 0 {
		t.Errorf("0: %d, %v", c.Data.SnapshotMax(), err)
	}
	if _, err = load("  snapshot_limit: 500\n"); err == nil || !strings.Contains(err.Error(), `"500" is not a size`) {
		t.Errorf("a bare number: %v", err)
	}
}
