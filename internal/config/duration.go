package config

import (
	"time"

	"gopkg.in/yaml.v3"
)

// Duration is a time.Duration written the way a human writes one:
// "120s", "2m", "24h". yaml.v3 decodes a bare number into a
// time.Duration as nanoseconds, which would silently turn "timeout: 120"
// into 120ns, so the type insists on a string.
type Duration time.Duration

// UnmarshalYAML accepts Go's duration syntax.
func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	var s string
	if err := node.Decode(&s); err != nil {
		return &yaml.TypeError{Errors: []string{
			node.Value + " is not a duration; write it like \"120s\", \"2m\" or \"24h\"",
		}}
	}

	parsed, err := time.ParseDuration(s)
	if err != nil {
		return &yaml.TypeError{Errors: []string{
			s + " is not a duration; write it like \"120s\", \"2m\" or \"24h\"",
		}}
	}

	*d = Duration(parsed)
	return nil
}

// MarshalYAML writes the duration back in the same human form, so a
// generated file reads like a hand written one.
func (d Duration) MarshalYAML() (any, error) { return d.String(), nil }

// Duration returns the value as a time.Duration.
func (d Duration) Duration() time.Duration { return time.Duration(d) }

// String renders the duration in Go's syntax.
func (d Duration) String() string { return time.Duration(d).String() }

// IsZero reports whether the duration was left unset.
func (d Duration) IsZero() bool { return d == 0 }
