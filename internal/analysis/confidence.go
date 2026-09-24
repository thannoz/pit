package analysis

// Confidence is how sure the guide is that an address shows a change.
type Confidence int

// The levels, least sure first. Each says what would have to be true for
// the reviewer to be misled.
const (
	// Uncertain: part of how the address is served could not be read
	// -- a prefix set at run time, middleware that rewrites requests.
	// The change may show here, elsewhere, or under another address.
	Uncertain Confidence = iota
	// Likely: the address is right, but whether the change is visible
	// there depends on something the files do not say -- how another
	// file uses the changed one, whether a loading screen is caught.
	Likely
	// Certain: the changed file serves this address, as its framework
	// reads it.
	Certain
)

func (c Confidence) String() string {
	switch c {
	case Uncertain:
		return "uncertain"
	case Likely:
		return "likely"
	case Certain:
		return "certain"
	default:
		return "unknown"
	}
}

// Doubt is a reason not to be certain, and how much it costs.
type Doubt struct {
	Confidence Confidence
	// Reason says it in words a reviewer can check.
	Reason string
}
