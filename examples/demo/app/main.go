// Teashop is the application in pit's demo: three pages, a form, one
// database.
package main

import (
	"database/sql"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"

	_ "github.com/jackc/pgx/v5/stdlib"
)

type order struct {
	ID    int
	Item  string
	Cents int
}

func main() {
	db, err := sql.Open("pgx", os.Getenv("DATABASE_URL"))
	if err != nil {
		log.Fatal(err)
	}
	s := &shop{db: db}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.home)
	mux.HandleFunc("GET /orders", s.orders)
	mux.HandleFunc("GET /orders/{id}", s.order)
	mux.HandleFunc("POST /orders", s.place)

	log.Print("listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", logRequests(mux)))
}

type shop struct{ db *sql.DB }

func (s *shop) home(w http.ResponseWriter, r *http.Request) {
	var n int
	_ = s.db.QueryRowContext(r.Context(), "SELECT count(*) FROM orders").Scan(&n)
	render(w, `<h1>Teashop</h1><p>{{.}} orders so far. <a href="/orders">See them</a></p>`+
		`<form method="post" action="/orders"><input name="item" placeholder="Gyokuro, 50 g"> <button>Order it</button></form>`, n)
}

// place takes an order from the form on the home page, the one thing a
// visitor can change in the shop.
func (s *shop) place(w http.ResponseWriter, r *http.Request) {
	item := strings.TrimSpace(r.FormValue("item"))
	if item == "" {
		http.Error(w, "an order needs an item", http.StatusBadRequest)
		return
	}
	var id int
	err := s.db.QueryRowContext(r.Context(),
		"INSERT INTO orders (id, item, cents) SELECT coalesce(max(id), 1000) + 1, $1, 1000 FROM orders RETURNING id", item).Scan(&id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/orders/%d", id), http.StatusSeeOther)
}

func (s *shop) orders(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.QueryContext(r.Context(), "SELECT id, item, cents FROM orders ORDER BY id")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	var list []order
	for rows.Next() {
		var o order
		if err := rows.Scan(&o.ID, &o.Item, &o.Cents); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		list = append(list, o)
	}
	render(w, `<h1>Orders</h1>{{range .}}<p><a href="/orders/{{.ID}}">#{{.ID}}</a> {{.Item}}: {{price .Cents}}</p>{{else}}<p>No orders yet.</p>{{end}}`, list)
}

func (s *shop) order(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(r.PathValue("id"))
	var o order
	err := s.db.QueryRowContext(r.Context(), "SELECT id, item, cents FROM orders WHERE id = $1", id).Scan(&o.ID, &o.Item, &o.Cents)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	render(w, `<h1>Order #{{.ID}}</h1><p>{{.Item}}: {{price .Cents}}</p>`, o)
}

func render(w http.ResponseWriter, page string, data any) {
	t := template.Must(template.New("page").Funcs(template.FuncMap{"price": price}).Parse(page))
	if err := t.Execute(w, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// logRequests writes a line per request, as development servers do.
func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &status{ResponseWriter: w, code: http.StatusOK}
		next.ServeHTTP(rec, r)
		fmt.Printf("%s %s %d\n", r.Method, r.URL.Path, rec.code)
	})
}

type status struct {
	http.ResponseWriter
	code int
}

func (s *status) WriteHeader(code int) {
	s.code = code
	s.ResponseWriter.WriteHeader(code)
}
