package config

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// ByteSize is an amount of data written the way people write one:
// "500MB", "2GB", "750 kB". The units are decimal, like the sizes pit
// prints. A bare number is refused: "limit: 500" could mean anything.
type ByteSize int64

var byteUnits = map[string]int64{
	"b": 1, "kb": 1e3, "mb": 1e6, "gb": 1e9, "tb": 1e12,
}

// ParseByteSize reads a size such as "500MB".
func ParseByteSize(s string) (ByteSize, error) {
	t := strings.ToLower(strings.TrimSpace(s))
	if t == "0" {
		return 0, nil
	}
	i := strings.IndexFunc(t, func(r rune) bool { return (r < '0' || r > '9') && r != '.' })
	if i <= 0 {
		return 0, fmt.Errorf("%q is not a size; write it like \"500MB\" or \"2GB\"", s)
	}
	unit, ok := byteUnits[strings.TrimSpace(t[i:])]
	n, err := strconv.ParseFloat(t[:i], 64)
	if !ok || err != nil || n*float64(unit) > math.MaxInt64 {
		return 0, fmt.Errorf("%q is not a size; write it like \"500MB\" or \"2GB\"", s)
	}
	return ByteSize(n * float64(unit)), nil
}

// UnmarshalYAML accepts a size with its unit, or 0.
func (b *ByteSize) UnmarshalYAML(node *yaml.Node) error {
	parsed, err := ParseByteSize(node.Value)
	if err != nil || node.Kind != yaml.ScalarNode {
		return &yaml.TypeError{Errors: []string{fmt.Sprintf("line %d: %v", node.Line, err)}}
	}
	*b = parsed
	return nil
}

// MarshalYAML writes the size back in a unit that reads well.
func (b ByteSize) MarshalYAML() (any, error) { return b.String(), nil }

// String renders the size: 500MB, 1.5GB.
func (b ByteSize) String() string {
	for _, u := range []struct {
		name string
		size int64
	}{{"TB", 1e12}, {"GB", 1e9}, {"MB", 1e6}, {"kB", 1e3}} {
		if int64(b) >= u.size {
			return strconv.FormatFloat(float64(b)/float64(u.size), 'f', -1, 64) + u.name
		}
	}
	return strconv.FormatInt(int64(b), 10) + "B"
}

// SnapshotMax is the most a snapshot may hold, in bytes; 0 is no limit.
func (d Data) SnapshotMax() int64 {
	if d.SnapshotLimit == nil {
		return int64(DefaultSnapshotLimit)
	}
	return int64(*d.SnapshotLimit)
}
