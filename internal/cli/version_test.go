package cli

import (
	"encoding/json"
	"runtime"
	"strings"
	"testing"
)

func TestBuildInfoString(t *testing.T) {
	b := buildInfo{
		Version:   "1.2.3",
		Commit:    "abc1234",
		BuildDate: "2026-09-22T10:00:00Z",
		GoVersion: "go1.27.0",
		Platform:  "darwin/arm64",
	}

	got := b.String()
	for _, want := range []string{"1.2.3", "abc1234", "2026-09-22T10:00:00Z", "go1.27.0", "darwin/arm64"} {
		if !strings.Contains(got, want) {
			t.Errorf("String() = %q, missing %q", got, want)
		}
	}
	if !strings.HasPrefix(got, "pit ") {
		t.Errorf("String() = %q, want it to start with %q", got, "pit ")
	}
}

func TestCurrentBuildFillsRuntimeFields(t *testing.T) {
	b := currentBuild()

	if b.GoVersion != runtime.Version() {
		t.Errorf("GoVersion = %q, want %q", b.GoVersion, runtime.Version())
	}
	if want := runtime.GOOS + "/" + runtime.GOARCH; b.Platform != want {
		t.Errorf("Platform = %q, want %q", b.Platform, want)
	}
	// The ldflags defaults must never be empty: an empty version in a
	// bug report is worse than a wrong one.
	if b.Version == "" || b.Commit == "" || b.BuildDate == "" {
		t.Errorf("currentBuild() left a field empty: %+v", b)
	}
}

func TestVersionCommandText(t *testing.T) {
	out, errOut, err := run(t, "version")
	if err != nil {
		t.Fatalf("version: %v (stderr %q)", err, errOut)
	}
	if !strings.HasPrefix(out, "pit ") {
		t.Errorf("stdout = %q, want it to start with %q", out, "pit ")
	}
	if strings.Contains(out, "{") {
		t.Errorf("stdout = %q, want text output without --json", out)
	}
}

func TestVersionCommandJSON(t *testing.T) {
	out, errOut, err := run(t, "version", "--json")
	if err != nil {
		t.Fatalf("version --json: %v (stderr %q)", err, errOut)
	}

	var got buildInfo
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\n%s", err, out)
	}
	if got.Version == "" || got.Platform == "" {
		t.Errorf("decoded %+v, want version and platform set", got)
	}
}

func TestVersionRejectsArguments(t *testing.T) {
	if _, _, err := run(t, "version", "extra"); err == nil {
		t.Error("version with an extra argument: got nil error, want one")
	}
}
