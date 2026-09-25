// Package report turns what a reviewer noted into a comment on the pull
// request: what is wrong, where, and how to see it again.
package report

import (
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/thannoz/pit/internal/inspect"
	"github.com/thannoz/pit/internal/notes"
)

// Input is what a comment is made from.
type Input struct {
	// PR is the pull request the comment goes on.
	PR int
	// Notes are what the reviewer found, in the order they are told.
	Notes []notes.Note
	// Head is the commit the pull request is at now, when known: notes
	// taken on an earlier one say so.
	Head string
	// Pictures are where each note's screenshot can be seen, by the
	// note's number; a note without one is told without it.
	Pictures map[int]string
}

// maxText is as long as a message from the page gets; a stack trace or
// a page of HTML in an error helps nobody in a comment.
const maxText = 300

// Home is where a reader who does not know pit finds out what it is.
const Home = "https://github.com/thannoz/pit"

// Comment writes the comment, in the Markdown GitHub reads.
//
// It is written for the pull request's author, who has neither the
// reviewer's sandbox nor its address: pages are paths, and seeing it
// again is a command, not "on my machine". What all notes have in
// common -- the commit, the data -- is told once, above them; each
// note tells only where it differs.
func Comment(in Input) string {
	list := sorted(in.Notes)
	if len(list) == 0 {
		return ""
	}
	sameSHA := same(list, func(n notes.Note) string { return n.SHA })
	sameScenario := same(list, func(n notes.Note) string { return n.Scenario })
	sameCaveats := same(list, func(n notes.Note) string { return strings.Join(caveats(n), " ") })

	var b strings.Builder
	b.WriteString("### Review notes\n\n")
	if len(list) == 1 {
		b.WriteString("One thing I found while trying ")
	} else {
		fmt.Fprintf(&b, "%d things I found while trying ", len(list))
	}
	if sameSHA && list[0].SHA != "" {
		fmt.Fprintf(&b, "out %s locally.", short(list[0].SHA))
		if moved(list[0].SHA, in.Head) {
			b.WriteString(" The pull request has moved on since; some of this may be fixed already.")
		}
		b.WriteString("\n")
	} else {
		b.WriteString("this out locally.\n")
	}
	if sameScenario {
		lead := "To see them yourself"
		if len(list) == 1 {
			lead = "To see it yourself"
		}
		writeCommand(&b, lead, in.PR, list[0].Scenario)
	}
	if sameCaveats {
		writeCaveats(&b, list[0])
	}

	for _, n := range list {
		fmt.Fprintf(&b, "\n#### %d. %s\n\n", n.ID, oneLine(n.Text))
		page := pathOf(n.URL, n.URL)
		fmt.Fprintf(&b, "Page: %s", code(page))
		if !sameSHA {
			fmt.Fprintf(&b, " · commit %s", shaRef(n.SHA, in.Head))
		}
		b.WriteString("\n")
		writeFindings(&b, n)
		writeLogs(&b, n)
		if !sameScenario {
			writeCommand(&b, "To see it yourself", in.PR, n.Scenario)
		}
		if !sameCaveats {
			writeCaveats(&b, n)
		}
		if picture := in.Pictures[n.ID]; picture != "" {
			fmt.Fprintf(&b, "\n![%s](%s)\n", "Screenshot of "+escapeAlt(page), picture)
		}
	}
	return b.String()
}

// same reports whether every note has the same of something.
func same(list []notes.Note, of func(notes.Note) string) bool {
	for _, n := range list[1:] {
		if of(n) != of(list[0]) {
			return false
		}
	}
	return true
}

// writeCommand is the command that brings the pull request up with the
// data a note was seen on.
func writeCommand(b *strings.Builder, lead string, pr int, scenario string) {
	command := "pit " + strconv.Itoa(pr)
	if scenario != "" {
		command += " --scenario=" + scenario
	}
	fmt.Fprintf(b, "\n%s, with [pit](%s):\n\n```sh\n%s\n```\n", lead, Home, command)
}

// caveats are what the command cannot bring back: data that was not
// the scenario's.
func caveats(n notes.Note) []string {
	var out []string
	if n.Snapshot != "" {
		out = append(out, "The data came from a snapshot of mine, "+code(n.Snapshot)+
			", which only I have; the scenario is the one it was taken on.")
	}
	if n.Edited {
		out = append(out, "I had changed the data by hand before this; the scenario alone may not show it.")
	}
	return out
}

func writeCaveats(b *strings.Builder, n notes.Note) {
	if c := caveats(n); len(c) > 0 {
		b.WriteString("\n" + strings.Join(c, " ") + "\n")
	}
}

// writeFindings lists what went wrong on the page, the way the
// browser's developer tools would have shown it.
func writeFindings(b *strings.Builder, n notes.Note) {
	if !n.Captured() {
		return
	}
	if len(n.Problems) == 0 && len(n.Pending) == 0 {
		b.WriteString("\nThe browser reported no errors on the page.\n")
		return
	}
	b.WriteString("\n")
	for _, p := range n.Problems {
		fmt.Fprintf(b, "- %s\n", describe(p, n.URL))
	}
	for _, p := range n.Pending {
		method, address, _ := strings.Cut(p, " ")
		fmt.Fprintf(b, "- **No answer:** %s was still waiting after several seconds\n", code(method+" "+pathOf(address, n.URL)))
	}
}

// describe is one problem, in a line.
func describe(p inspect.Problem, page string) string {
	level := "error"
	if p.Level == "warning" {
		level = "warning"
	}
	switch p.Kind {
	case inspect.Request:
		request := code(p.Method + " " + pathOf(p.URL, page))
		if p.Status == 0 {
			return fmt.Sprintf("**Failed request:** %s: %s", request, clip(p.Text))
		}
		return fmt.Sprintf("**Failed request:** %s answered %d %s", request, p.Status, clip(p.Text))
	case inspect.Exception:
		return fmt.Sprintf("**Uncaught exception:** %s%s", code(clip(p.Text)), at(p.Source, page))
	case inspect.Console:
		return fmt.Sprintf("**Console %s:** %s%s", level, code(clip(p.Text)), at(p.Source, page))
	default:
		return fmt.Sprintf("**Browser %s:** %s%s", level, code(clip(p.Text)), at(orElse(p.Source, p.URL), page))
	}
}

func at(source, page string) string {
	if source == "" {
		return ""
	}
	return " at " + code(pathOf(source, page))
}

// pathOf is an address of the sandbox without the sandbox: its host
// and port are the reviewer's and mean nothing to anyone else. An
// address elsewhere is left whole.
func pathOf(address, page string) string {
	u, err := url.Parse(address)
	if err != nil || u.Host == "" {
		return address
	}
	if p, err := url.Parse(page); err == nil && p.Host != "" && p.Host != u.Host {
		return address
	}
	path := u.RequestURI()
	if u.Fragment != "" {
		path += "#" + u.Fragment
	}
	// A source keeps its line: http://localhost:4000/app.js:12 parses
	// with the line in the path.
	return path
}

// shaRef is a commit the way GitHub links it, and whether the pull
// request has moved on since.
func shaRef(sha, head string) string {
	if sha == "" {
		return "a commit pit did not record"
	}
	if moved(sha, head) {
		return short(sha) + " (the pull request has moved on since)"
	}
	return short(sha)
}

// moved reports whether the pull request is at another commit now than
// the one a note was taken on.
func moved(sha, head string) bool {
	return sha != "" && head != "" && !strings.HasPrefix(head, sha) && !strings.HasPrefix(sha, head)
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// code sets text as code, with a fence of more backticks than it has
// in a row: a message can have backticks of its own.
func code(s string) string {
	fence := strings.Repeat("`", longestRun(s, '`')+1)
	if strings.HasPrefix(s, "`") || strings.HasSuffix(s, "`") {
		s = " " + s + " "
	}
	return fence + s + fence
}

// clip keeps a message to one line and a length a comment can carry.
func clip(s string) string {
	s = oneLine(s)
	if r := []rune(s); len(r) > maxText {
		return string(r[:maxText]) + "…"
	}
	return s
}

// oneLine joins what spans lines into one, which a heading and a list
// item need.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func escapeAlt(s string) string {
	return strings.NewReplacer("[", `\[`, "]", `\]`).Replace(s)
}

func orElse(s, fallback string) string {
	if s != "" {
		return s
	}
	return fallback
}

// sorted is the notes by number, the order they were taken in.
func sorted(list []notes.Note) []notes.Note {
	out := slices.Clone(list)
	slices.SortFunc(out, func(a, b notes.Note) int { return a.ID - b.ID })
	return out
}
