package routes

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/thannoz/pit/internal/analysis"
)

// program builds a module from source files. go.mod is added.
func goProgram(files map[string]string) fstest.MapFS {
	fsys := fstest.MapFS{"go.mod": {Data: []byte("module example.com/shop\n\ngo 1.24\n")}}
	for name, src := range files {
		fsys[name] = &fstest.MapFile{Data: []byte(src)}
	}
	return fsys
}

func goRoutes(t *testing.T, fsys fstest.MapFS) []analysis.Route {
	t.Helper()
	routes, err := Go{}.Routes(context.Background(), fsys)
	if err != nil {
		t.Fatalf("Routes: %v", err)
	}
	return routes
}

// addresses is what a test usually asserts: method and path of every
// route, each once.
func addresses(routes []analysis.Route) []string {
	var out []string
	for _, r := range routes {
		a := strings.TrimSpace(r.Method + " " + r.Path)
		if !slices.Contains(out, a) {
			out = append(out, a)
		}
	}
	slices.Sort(out)
	return out
}

// served lists, for one address, the files and lines that lead there.
func servedBy(routes []analysis.Route, method, path string) []string {
	var out []string
	for _, r := range routes {
		if r.Method == method && r.Path == path {
			out = append(out, fmt.Sprintf("%s:%d-%d", r.File, r.Lines.Start, r.Lines.End()))
		}
	}
	slices.Sort(out)
	return out
}

func expectAddresses(t *testing.T, routes []analysis.Route, want ...string) {
	t.Helper()
	slices.Sort(want)
	if got := addresses(routes); !slices.Equal(got, want) {
		t.Errorf("addresses\n got %s\nwant %s", strings.Join(got, "\n     "), strings.Join(want, "\n     "))
	}
}

func TestGin(t *testing.T) {
	routes := goRoutes(t, goProgram(map[string]string{
		"server/routes.go": `package server

import (
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
	"example.com/shop/middleware"
	"example.com/shop/paths"
)

const blobs = "/api/blobs"

func (s *Server) Routes() (http.Handler, error) {
	r := gin.Default()
	r.GET("/", func(c *gin.Context) { c.String(200, "running") })
	r.POST("/api/pull", s.PullHandler)
	r.HEAD("/api/tags", s.ListHandler)
	r.DELETE("/api/user/keys/:encodedKey", s.SignoutHandler)
	r.POST(blobs+"/:digest", s.CreateBlobHandler)
	r.Any(paths.Proxy+"/*path", s.ListHandler)
	r.Handle("PATCH", "/api/models", s.ListHandler)
	r.POST("/v1/chat/completions", middleware.ChatMiddleware(), s.ChatHandler)

	api := r.Group("/api/v2")
	{
		users := api.Group("/users")
		users.GET("/:id", s.ListHandler)
		registerAdmin(api.Group("/admin"))
	}

	r.GET(computed(), s.ListHandler)  // read at run time: "…", and why
	slog.Any("inference", 1)          // not a route, though it looks like one to a regex
	return r, nil
}

func registerAdmin(g *gin.RouterGroup) {
	g.POST("/reindex", reindex)
}

func computed() string { return "/x" }
func reindex(c *gin.Context) {}

func Serve(s *Server) {
	h, _ := s.Routes()
	http.Handle("/", h)
}
`,
		"server/handlers.go": `package server

import "github.com/gin-gonic/gin"

type Server struct{}

// PullHandler pulls.
func (s *Server) PullHandler(c *gin.Context) {}
func (s *Server) ListHandler(c *gin.Context) {}
func (s *Server) SignoutHandler(c *gin.Context) {}
func (s *Server) CreateBlobHandler(c *gin.Context) {}
func (s *Server) ChatHandler(c *gin.Context) {}
`,
		"middleware/chat.go": `package middleware

import "github.com/gin-gonic/gin"

func ChatMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {}
}
`,
		"paths/paths.go": `package paths

const Proxy = "/proxy"
`,
		// The same method names in another package, the way several
		// servers in one repository each have a ListHandler.
		"desktop/handlers.go": `package desktop

type App struct{}

func (a *App) ListHandler() {}
func (a *App) ChatHandler() {}
`,
	}))

	expectAddresses(t, routes,
		"/",
		"GET /",
		"POST /api/pull",
		"HEAD /api/tags",
		"DELETE /api/user/keys/{encodedKey}",
		"POST /api/blobs/{digest}",
		"/proxy/{path...}",
		"PATCH /api/models",
		"POST /v1/chat/completions",
		"GET /api/v2/users/{id}",
		"POST /api/v2/admin/reindex",
		"GET /…",
	)
	if got := doubtsOf(routes, "GET", "/…"); !slices.Equal(got, []string{"uncertain: the path is computed(), which pit cannot read"}) {
		t.Errorf("GET /… doubts %q", got)
	}
	if got := doubtsOf(routes, "POST", "/api/pull"); len(got) != 0 {
		t.Errorf("a literal route has doubts: %q", got)
	}

	// The registration's own line, and the handler with its comment.
	if got, want := servedBy(routes, "POST", "/api/pull"), []string{
		"server/handlers.go:7-8", "server/routes.go:17-17",
	}; !slices.Equal(got, want) {
		t.Errorf("POST /api/pull served by %v, want %v", got, want)
	}
	// Without types, s.ListHandler is the method of that name in the
	// package that registers it, not the one in desktop/.
	if got, want := servedBy(routes, "HEAD", "/api/tags"), []string{
		"server/handlers.go:9-9", "server/routes.go:18-18",
	}; !slices.Equal(got, want) {
		t.Errorf("HEAD /api/tags served by %v, want %v", got, want)
	}
	// Middleware is part of the route: a change to it is a change to
	// every route it wraps.
	if got := servedBy(routes, "POST", "/v1/chat/completions"); !slices.Contains(got, "middleware/chat.go:5-7") {
		t.Errorf("middleware not followed: %v", got)
	}
}

func TestChi(t *testing.T) {
	routes := goRoutes(t, goProgram(map[string]string{
		"api/router.go": `package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"example.com/shop/admin"
)

type API struct{}

func (a *API) Routes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", a.index)
	r.Route("/orders", func(r chi.Router) {
		r.Get("/", a.listOrders)
		r.Route("/{orderID:[0-9]+}", func(r chi.Router) {
			r.Get("/", a.getOrder)
			r.With(a.auth).Delete("/", a.deleteOrder)
		})
	})
	r.Group(func(r chi.Router) {
		r.Post("/login", a.login)
	})
	r.Route("/users", a.userRoutes)

	files := chi.NewRouter()
	files.Get("/*", a.index)
	r.Mount("/files", files)

	r.Mount("/admin", admin.Router())
	r.Method("PUT", "/settings", http.HandlerFunc(a.index))
	return r
}

func (a *API) userRoutes(r chi.Router) {
	r.Get("/{id}", a.index)
}

func (a *API) index(w http.ResponseWriter, r *http.Request)       {}
func (a *API) listOrders(w http.ResponseWriter, r *http.Request)  {}
func (a *API) getOrder(w http.ResponseWriter, r *http.Request)    {}
func (a *API) deleteOrder(w http.ResponseWriter, r *http.Request) {}
func (a *API) login(w http.ResponseWriter, r *http.Request)       {}
func (a *API) auth(next http.Handler) http.Handler                { return next }
`,
		"admin/admin.go": `package admin

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

func Router() http.Handler {
	r := chi.NewRouter()
	r.Post("/reindex", reindex)
	return r
}

func reindex(w http.ResponseWriter, r *http.Request) {}
`,
		"main.go": `package main

import (
	"net/http"

	"example.com/shop/api"
)

func main() {
	a := &api.API{}
	srv := &http.Server{Addr: ":8080", Handler: a.Routes()}
	_ = srv.ListenAndServe()
}
`,
	}))

	expectAddresses(t, routes,
		"GET /",
		"GET /orders",
		"GET /orders/{orderID}",
		"DELETE /orders/{orderID}",
		"POST /login",
		"GET /users/{id}",
		"GET /files/{rest...}",
		"POST /admin/reindex",
		"PUT /settings",
	)
	// With(a.auth) wraps the route: the middleware leads there too,
	// and only to that route.
	if got, want := servedBy(routes, "DELETE", "/orders/{orderID}"), []string{
		"api/router.go:19-19", "api/router.go:43-43", "api/router.go:45-45",
	}; !slices.Equal(got, want) {
		t.Errorf("DELETE /orders/{orderID} served by %v, want %v", got, want)
	}
	if got := servedBy(routes, "GET", "/orders/{orderID}"); slices.Contains(got, "api/router.go:45-45") {
		t.Errorf("auth leads to a route it does not wrap: %v", got)
	}
}

// A router that leaves the function that makes it -- returned, even
// wrapped, or stored -- sits wherever it is mounted. Where the source
// does not say, the address starts with "…" and says why: a guessed
// root would be wrong, and leaving the route out would say its handler
// serves nothing. Checked against navidrome, whose routers are mounted
// by dependency injection under a path from its configuration.
func TestWhatCannotBeReadIsMarked(t *testing.T) {
	routes := goRoutes(t, goProgram(map[string]string{
		"jellyfin/api.go": `package jellyfin

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

type Router struct{ http.Handler }

func New() *Router {
	r := &Router{}
	r.Handler = r.routes()
	return r
}

func (api *Router) routes() http.Handler {
	inner := chi.NewRouter()
	inner.Get("/system/ping", api.ping)
	return caseInsensitive(inner)
}

func (api *Router) ping(w http.ResponseWriter, r *http.Request) {}
func caseInsensitive(h http.Handler) http.Handler                { return h }
`,
		"server/server.go": `package server

import (
	"net/http"
	"path"

	"github.com/go-chi/chi/v5"
)

type Server struct{ router chi.Router; base string }

func (s *Server) MountRouter(urlPath string, h http.Handler) {
	s.router.Mount(urlPath, h)
}

func (s *Server) routes() {
	s.router.Get("/*", s.redirect)
	s.router.Route(path.Join(s.base, "/auth"), func(r chi.Router) {
		r.Post("/login", s.redirect)
	})
}

func (s *Server) redirect(w http.ResponseWriter, r *http.Request) {}
`,
	}))

	expectAddresses(t, routes, "GET /{rest...}", "GET /…/system/ping", "POST /…/login")
	for address, want := range map[string][]string{
		"/{rest...}":     nil,
		"/…/system/ping": {"uncertain: the router made in (*Router).routes is handed on, and pit cannot see where it is mounted"},
		"/…/login":       {`uncertain: mounted under path.Join(s.base, "/auth"), which pit cannot read`},
	} {
		var got []string
		for _, m := range []string{"GET", "POST"} {
			got = append(got, doubtsOf(routes, m, address)...)
		}
		if !slices.Equal(got, want) {
			t.Errorf("%s doubts\n got %q\nwant %q", address, got, want)
		}
	}
}

// doubtsOf is what the registration of an address says about it.
func doubtsOf(routes []analysis.Route, method, path string) []string {
	for _, r := range routes {
		if r.Method == method && r.Path == path {
			var out []string
			for _, d := range r.Doubts {
				out = append(out, d.Confidence.String()+": "+d.Reason)
			}
			return out
		}
	}
	return nil
}

func TestNetHTTP(t *testing.T) {
	routes := goRoutes(t, goProgram(map[string]string{
		"ui/ui.go": `package ui

import "net/http"

type Server struct{}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/chat/{id}", http.HandlerFunc(s.getChat))
	mux.HandleFunc("POST /api/v1/chat/{id}", s.getChat)
	mux.HandleFunc("GET /files/{path...}", s.getChat)
	mux.HandleFunc("GET /{$}", s.getChat)
	mux.HandleFunc("example.com/static/", s.getChat)
	return mux
}

func (s *Server) getChat(w http.ResponseWriter, r *http.Request) {}
`,
		"cmd/app/main.go": `package main

import (
	"net/http"

	"example.com/shop/ui"
)

func main() {
	s := &ui.Server{}
	http.HandleFunc("/healthz", health)
	_ = http.ListenAndServe(":8080", s.Handler())
}

func health(w http.ResponseWriter, r *http.Request) {}
`,
	}))

	expectAddresses(t, routes,
		"GET /api/v1/chat/{id}",
		"POST /api/v1/chat/{id}",
		"GET /files/{path...}",
		"GET /",
		"/static/",
		"/healthz",
	)
}

func TestGorilla(t *testing.T) {
	routes := goRoutes(t, goProgram(map[string]string{
		"web/routes.go": `package web

import (
	"net/http"

	"github.com/gorilla/mux"
)

func Setup() {
	r := mux.NewRouter()
	r.HandleFunc("/feeds/{feedID:[0-9]+}", show).Methods("GET")
	api := r.PathPrefix("/v1").Subrouter()
	api.HandleFunc("/entries", show).Methods("POST")
	_ = http.ListenAndServe(":8080", r)
}

func show(w http.ResponseWriter, r *http.Request) {}
`,
	}))

	expectAddresses(t, routes, "GET /feeds/{feedID}", "POST /v1/entries")
}

// Where Go ignores a file, so does the heuristic; and a program that
// imports no router is not parsed beyond its imports.
func TestWhatIsNotRead(t *testing.T) {
	route := `package %s

import "net/http"

func init() { http.HandleFunc("/%s", nil) }
`
	routes := goRoutes(t, goProgram(map[string]string{
		"app/app.go":              fmt.Sprintf(route, "app", "app"),
		"app/app_test.go":         fmt.Sprintf(route, "app", "test"),
		"vendor/lib/lib.go":       fmt.Sprintf(route, "lib", "vendored"),
		"testdata/fixture.go":     fmt.Sprintf(route, "fixture", "testdata"),
		"_old/old.go":             fmt.Sprintf(route, "old", "old"),
		".git/hooks/x.go":         fmt.Sprintf(route, "x", "hidden"),
		"broken/broken.go":        "package broken\n\nfunc (",
		"node_modules/x/x.go":     fmt.Sprintf(route, "x", "node"),
		"internal/plain/plain.go": "package plain\n\nfunc Get(p string) {}\nfunc use() { Get(\"/not-a-route\") }\n",
	}))
	expectAddresses(t, routes, "/app")

	none := goRoutes(t, goProgram(map[string]string{"lib/lib.go": "package lib\n\nfunc F() {}\n"}))
	if len(none) != 0 {
		t.Errorf("routes without a router: %v", none)
	}
}

// Pages and endpoints cannot be told apart by the registration; the
// guess is written down so that it can be questioned.
func TestKindIsAGuessByAddress(t *testing.T) {
	for _, tc := range []struct {
		method, path string
		want         analysis.Kind
	}{
		{"GET", "/orders", analysis.Page},
		{"", "/", analysis.Page},
		{"GET", "/api/orders", analysis.Endpoint},
		{"GET", "/v1/models", analysis.Endpoint},
		{"POST", "/login", analysis.Endpoint},
		{"HEAD", "/", analysis.Page},
	} {
		if got := kindOf(tc.method, tc.path); got != tc.want {
			t.Errorf("kindOf(%s %s) = %s, want %s", tc.method, tc.path, got, tc.want)
		}
	}
}

func TestPlaceholders(t *testing.T) {
	for in, want := range map[string]string{
		"/users/:id":          "/users/{id}",
		"/files/*filepath":    "/files/{filepath...}",
		"/files/*":            "/files/{rest...}",
		"/orders/{id:[0-9]+}": "/orders/{id}",
		"/files/{path...}":    "/files/{path...}",
		"/{$}":                "/",
		"":                    "/",
		"/plain":              "/plain",
	} {
		if got := placeholders(in); got != want {
			t.Errorf("placeholders(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestACancelledReadStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Go{}.Routes(ctx, goProgram(map[string]string{"a.go": "package a\n"}))
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

// An unreadable path counts only on a receiver that registers readable
// ones as well. In a package that imports chi, rdb.Get(ctx, key) is a
// cache lookup, not a route with a path pit cannot read.
func TestAnUnreadablePathNeedsARouter(t *testing.T) {
	routes := goRoutes(t, goProgram(map[string]string{
		"api/api.go": `package api

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"
)

type cache interface{ Get(ctx context.Context, key string) string }

var rdb cache

func Routes(r chi.Router, base string) {
	r.Get("/health", health)
	r.Get(base+"/items", health)
	_ = rdb.Get(context.Background(), base)
}

func health(w http.ResponseWriter, r *http.Request) {}
`,
		"main.go": `package main

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"example.com/shop/api"
)

func main() {
	r := chi.NewRouter()
	api.Routes(r, "")
	_ = http.ListenAndServe(":1", r)
}
`,
	}))
	expectAddresses(t, routes, "GET /health", "GET /…/items")
}

// Without types, x.Serve is every method of that name in the package.
// The guide still follows them all -- one of them is the handler -- but
// says it went by the name.
func TestAHandlerFoundByNameAloneSaysSo(t *testing.T) {
	routes := goRoutes(t, goProgram(map[string]string{
		"web/web.go": `package web

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

type Pages struct{}
type Files struct{}

func (p *Pages) Serve(w http.ResponseWriter, r *http.Request) {}
func (f *Files) Serve(w http.ResponseWriter, r *http.Request) {}

func Setup(p *Pages) {
	r := chi.NewRouter()
	r.Get("/pages", p.Serve)
	_ = http.ListenAndServe(":1", r)
}
`,
	}))
	var got []string
	for _, r := range routes {
		if r.File == "web/web.go" && r.Lines.Start == 12 {
			for _, d := range r.Doubts {
				got = append(got, d.Confidence.String()+": "+d.Reason)
			}
		}
	}
	want := []string{"likely: (*Pages).Serve is found by its name alone; 2 methods of that name are in its package"}
	if !slices.Equal(got, want) {
		t.Errorf("doubts %q, want %q", got, want)
	}
}
