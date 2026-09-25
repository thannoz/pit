package devcontainer

// standard turns JSON with comments -- what devcontainer.json is written
// in -- into JSON: comments go, and so does a comma before a closing
// bracket. Strings are left alone, "//" in a URL included.
//
// What it removes is replaced by spaces, and newlines stay, so the
// offset of an error in the result is the offset in the file.
func standard(in []byte) []byte {
	out := make([]byte, len(in))
	copy(out, in)

	const (
		code = iota
		str
		line
		block
	)
	state := code
	// comma is where the last comma outside a string was, while only
	// space and comments followed it.
	comma := -1
	for i := 0; i < len(out); i++ {
		c := out[i]
		switch state {
		case str:
			switch c {
			case '\\':
				i++
			case '"':
				state = code
			}
		case line:
			if c == '\n' {
				state = code
			} else {
				out[i] = ' '
			}
		case block:
			if c == '*' && i+1 < len(out) && out[i+1] == '/' {
				out[i], out[i+1] = ' ', ' '
				i++
				state = code
			} else if c != '\n' {
				out[i] = ' '
			}
		default:
			switch {
			case c == '"':
				state, comma = str, -1
			case c == '/' && i+1 < len(out) && out[i+1] == '/':
				out[i], out[i+1] = ' ', ' '
				i++
				state = line
			case c == '/' && i+1 < len(out) && out[i+1] == '*':
				out[i], out[i+1] = ' ', ' '
				i++
				state = block
			case c == ',':
				comma = i
			case c == '}' || c == ']':
				if comma >= 0 {
					out[comma] = ' '
				}
				comma = -1
			case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			default:
				comma = -1
			}
		}
	}
	return out
}
