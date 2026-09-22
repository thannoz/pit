package cli

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestQuietByDefault(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(newLogHandler(&buf, false))

	log.Debug("a debug line")
	log.Info("an info line")
	log.Warn("a warning")
	log.Error("an error")

	if buf.Len() != 0 {
		t.Errorf("logging without --verbose wrote %q, want nothing", buf.String())
	}
}

func TestVerboseLogsDownToDebug(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(newLogHandler(&buf, true))

	log.Debug("a debug line")
	log.Info("an info line")

	got := buf.String()
	for _, want := range []string{"a debug line", "an info line"} {
		t.Run(want, func(t *testing.T) {
			if !strings.Contains(got, want) {
				t.Errorf("verbose output = %q, missing %q", got, want)
			}
		})
	}
}
