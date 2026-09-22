package config

import (
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestDurationAcceptsHumanSyntax(t *testing.T) {
	tests := []struct {
		yaml string
		want time.Duration
	}{
		{"120s", 120 * time.Second},
		{"2m", 2 * time.Minute},
		{"24h", 24 * time.Hour},
		{"1h30m", 90 * time.Minute},
		{"500ms", 500 * time.Millisecond},
	}

	for _, tt := range tests {
		t.Run(tt.yaml, func(t *testing.T) {
			var got struct {
				D Duration `yaml:"d"`
			}
			if err := yaml.Unmarshal([]byte("d: "+tt.yaml), &got); err != nil {
				t.Fatalf("Unmarshal(%q): %v", tt.yaml, err)
			}
			if got.D.Duration() != tt.want {
				t.Errorf("got %v, want %v", got.D.Duration(), tt.want)
			}
		})
	}
}

// TestDurationRejectsBareNumbers guards a trap: yaml.v3 would decode a
// plain time.Duration from `timeout: 120` as 120 nanoseconds. The
// sandbox would then time out instantly and the reason would be
// invisible, so the type refuses the input instead.
func TestDurationRejectsBareNumbers(t *testing.T) {
	var got struct {
		D Duration `yaml:"d"`
	}

	err := yaml.Unmarshal([]byte("d: 120"), &got)
	if err == nil {
		t.Fatalf("a bare number was accepted as %v, want an error", got.D)
	}
	if !strings.Contains(err.Error(), "duration") {
		t.Errorf("error = %q, want it to explain how to write a duration", err)
	}
}

func TestDurationRejectsNonsense(t *testing.T) {
	for _, in := range []string{"soon", "12 seconds", "-"} {
		t.Run(in, func(t *testing.T) {
			var got struct {
				D Duration `yaml:"d"`
			}
			if err := yaml.Unmarshal([]byte("d: \""+in+"\""), &got); err == nil {
				t.Errorf("%q was accepted as %v, want an error", in, got.D)
			}
		})
	}
}

func TestDurationRoundTrips(t *testing.T) {
	// A generated file should read like a hand written one.
	in := struct {
		D Duration `yaml:"d"`
	}{D: Duration(90 * time.Second)}

	out, err := yaml.Marshal(in)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != "d: 1m30s" {
		t.Errorf("Marshal = %q, want %q", got, "d: 1m30s")
	}

	var back struct {
		D Duration `yaml:"d"`
	}
	if err := yaml.Unmarshal(out, &back); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if back.D != in.D {
		t.Errorf("round trip changed %v into %v", in.D, back.D)
	}
}
