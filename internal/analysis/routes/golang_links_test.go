package routes

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/thannoz/pit/internal/analysis"
	"github.com/thannoz/pit/internal/diff"
)

func goLinks(t *testing.T, files map[string]string) []string {
	t.Helper()
	links, err := GoLinks{}.Links(context.Background(), goProgram(files))
	if err != nil {
		t.Fatalf("Links: %v", err)
	}
	return edges(links)
}

func TestGoLinksByDeclaration(t *testing.T) {
	got := goLinks(t, map[string]string{
		"server/chat.go": `package server

import (
	"net/http"

	"example.com/shop/openai"
)

type Server struct{}

// ChatHandler answers chats.
func (s *Server) ChatHandler(w http.ResponseWriter, r *http.Request) {
	req := openai.FromChat(r)
	s.respond(w, req)
	_ = openai.NewEncoder().Encode(req)
}

func (s *Server) respond(w http.ResponseWriter, v any) {
	_ = limit
}

const limit = 10
`,
		"openai/chat.go": `package openai

import "net/http"

type Request struct{ Model string }

// FromChat reads a chat request.
func FromChat(r *http.Request) Request {
	return Request{}
}

func NewEncoder() *Encoder { return &Encoder{} }

type Encoder struct{}

func (e *Encoder) Encode(v any) error { return nil }

func (e *Encoder) Close() error { return nil }
`,
		"openai/other.go": `package openai

type Reader struct{}

func (r *Reader) Close() error { return nil }

func done(r *Reader) { _ = r.Close() }
`,
	})
	want := []string{
		// A function of another package, with its doc comment.
		"server/chat.go:13-13 -> openai/chat.go:7-10",
		// On the same line: the call inside the selector, and the
		// method it is called on, the one Encode in the tree.
		"server/chat.go:15-15 -> openai/chat.go:12-12",
		"server/chat.go:15-15 -> openai/chat.go:16-16",
		// A method of the same package.
		"server/chat.go:14-14 -> server/chat.go:18-20",
		// A receiver names its type.
		"server/chat.go:12-12 -> server/chat.go:9-9",
		"server/chat.go:18-18 -> server/chat.go:9-9",
		// A constant.
		"server/chat.go:19-19 -> server/chat.go:22-22",
		// Types used by name.
		"openai/chat.go:8-8 -> openai/chat.go:5-5",
		"openai/chat.go:9-9 -> openai/chat.go:5-5",
		"openai/chat.go:12-12 -> openai/chat.go:14-14",
		"openai/chat.go:16-16 -> openai/chat.go:14-14",
		"openai/chat.go:18-18 -> openai/chat.go:14-14",
		"openai/other.go:5-5 -> openai/other.go:3-3",
		"openai/other.go:7-7 -> openai/other.go:3-3",
		// r.Close() on line 7 is not here: two types in openai have a
		// Close, and without types there is no telling which.
	}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("links\n got %s\nwant %s", strings.Join(got, "\n     "), strings.Join(want, "\n     "))
	}
}

// The whole way, as the guide walks it: a changed helper in one package
// reaches the route whose handler calls it, and not the route next to
// it in the same file.
func TestAChangedHelperReachesItsRouteOnly(t *testing.T) {
	fsys := goProgram(map[string]string{
		"server/routes.go": `package server

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

func Routes() http.Handler {
	r := gin.Default()
	r.POST("/v1/responses", Responses)
	r.POST("/v1/chat", Chat)
	return r
}

func Serve() { _ = http.ListenAndServe(":1", Routes()) }
`,
		"server/handlers.go": `package server

import (
	"github.com/gin-gonic/gin"

	"example.com/shop/openai"
)

func Responses(c *gin.Context) {
	openai.FromResponses()
}

func Chat(c *gin.Context) {
	openai.FromChat()
}
`,
		"openai/convert.go": `package openai

func FromResponses() {
	compact()
}

func FromChat() {}

func compact() {}
`,
	})
	for _, tc := range []struct {
		line int
		want []string
	}{
		{9, []string{"/v1/responses"}}, // compact, which FromResponses calls
		{7, []string{"/v1/chat"}},      // FromChat
	} {
		changed := analysis.Classify(diff.Diff{Files: []diff.File{{
			Path: "openai/convert.go", Change: diff.Modified,
			Hunks: []diff.Hunk{{New: diff.Range{Start: tc.line, Count: 1}}},
		}}}, nil)
		g, err := analysis.Entrypoints(context.Background(), fsys, changed, []analysis.Analyzer{Go{}}, []analysis.Linker{GoLinks{}})
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, e := range g.Entrypoints {
			got = append(got, e.Path)
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("a change at line %d reaches %v, want %v", tc.line, got, tc.want)
		}
	}
}
