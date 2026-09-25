package report

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/thannoz/pit/internal/notes"
)

// maxLogText is as long as a line of a log gets in a comment.
const maxLogText = 400

// writeLogs adds what the services logged around a note, folded away:
// the author opens it when the page's own errors do not explain enough.
// The lines of all services are told in the order they were written,
// which is the order cause and effect come in.
func writeLogs(b *strings.Builder, n notes.Note) {
	if len(n.Logs) == 0 {
		return
	}
	type line struct {
		at      time.Time
		service string
		text    string
	}
	var lines []line
	var names, skipped []string
	width := 0
	for _, l := range n.Logs {
		names = append(names, l.Service)
		width = max(width, len(l.Service))
		if l.Skipped > 0 {
			skipped = append(skipped, fmt.Sprintf("%d earlier lines of %s", l.Skipped, l.Service))
		}
		for _, x := range l.Lines {
			lines = append(lines, line{x.At, l.Service, x.Text})
		}
	}
	slices.SortStableFunc(lines, func(a, b line) int { return a.at.Compare(b.at) })

	var text strings.Builder
	if len(skipped) > 0 {
		fmt.Fprintf(&text, "(%s left out)\n", strings.Join(skipped, ", "))
	}
	for _, l := range lines {
		text.WriteString(l.at.UTC().Format("15:04:05"))
		if len(n.Logs) > 1 {
			fmt.Fprintf(&text, " %-*s", width, l.service)
		}
		text.WriteString("  " + redact(clipLog(l.text)) + "\n")
	}
	body := strings.TrimSuffix(text.String(), "\n")
	fence := strings.Repeat("`", max(3, longestRun(body, '`')+1))
	fmt.Fprintf(b, "\n<details>\n<summary>What %s logged around this (times in UTC)</summary>\n\n%stext\n%s\n%s\n\n</details>\n",
		strings.Join(names, " and "), fence, body, fence)
}

// ansi is a terminal's colour and cursor codes, which a service writes
// for a terminal and a comment shows as noise.
var ansi = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)

// clipLog makes a log line fit a comment: no terminal codes, no
// control characters, not longer than a line should be.
func clipLog(s string) string {
	s = ansi.ReplaceAllString(s, "")
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '\t':
			return ' '
		case r < 0x20 || r == 0x7f:
			return -1
		}
		return r
	}, s)
	s = strings.TrimRight(s, " ")
	if r := []rune(s); len(r) > maxLogText {
		return string(r[:maxLogText]) + "…"
	}
	return s
}

// secrets are what a log should not carry into a pull request: a
// password, a token, a key -- in a header, a query, a config dump or a
// connection URL.
var secrets = []struct {
	re   *regexp.Regexp
	with string
}{
	{regexp.MustCompile(`(?i)\b(authorization|proxy-authorization|cookie|set-cookie|x-api-key|x-auth-token)(\s*[:=]\s*)[^\n]+`), "${1}${2}[redacted]"},
	{regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/=-]+`), "Bearer [redacted]"},
	{regexp.MustCompile(`(?i)\b((?:[a-z0-9]+[_-])*(?:password|passwd|pwd|secret|token|api[_-]?key|access[_-]?key|private[_-]?key|credentials?)(?:[_-][a-z0-9]+)*)("?'?\s*[:=]\s*"?'?)[^\s"'&,;}]+`), "${1}${2}[redacted]"},
	{regexp.MustCompile(`(?i)\b([a-z][a-z0-9+.-]*://[^\s:/@]*:)[^\s@/]+@`), "${1}[redacted]@"},
}

// redact takes out what looks like a secret. It errs towards taking
// out too much: a comment is read by more people than a log.
func redact(s string) string {
	for _, r := range secrets {
		s = r.re.ReplaceAllString(s, r.with)
	}
	return s
}

func longestRun(s string, c rune) int {
	run, longest := 0, 0
	for _, r := range s {
		if r == c {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}
	return longest
}
