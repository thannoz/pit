package runtime

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/thannoz/pit/internal/errs"
)

func composeFileAt(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "docker-compose.yml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

func TestReadServicesKeepsDocumentOrder(t *testing.T) {
	// Go randomises map iteration; a list that reshuffles between runs
	// is hard to trust when you are picking an item from it by number.
	path := composeFileAt(t, `
services:
  db:
    image: postgres:16
  redis:
    image: redis:7
  api:
    image: node:22
  web:
    image: nginx:alpine
`)

	for range 5 {
		services, err := ReadServices(path)
		if err != nil {
			t.Fatalf("ReadServices: %v", err)
		}
		want := []string{"db", "redis", "api", "web"}
		var got []string
		for _, s := range services {
			got = append(got, s.Name)
		}
		if !slices.Equal(got, want) {
			t.Fatalf("ReadServices() = %v, want %v", got, want)
		}
	}
}

func TestReadServicesFindsContainerPorts(t *testing.T) {
	path := composeFileAt(t, `
services:
  short:
    image: nginx
    ports: ["8080:80"]
  bare:
    image: nginx
    ports: ["3000"]
  bound:
    image: nginx
    ports: ["127.0.0.1:8080:80"]
  protocol:
    image: nginx
    ports: ["8080:80/tcp"]
  long:
    image: nginx
    ports:
      - target: 5173
        published: 8080
  exposed:
    image: nginx
    expose: ["9000"]
  none:
    image: nginx
`)

	services, err := ReadServices(path)
	if err != nil {
		t.Fatalf("ReadServices: %v", err)
	}

	want := map[string][]int{
		"short":    {80},
		"bare":     {3000},
		"bound":    {80},
		"protocol": {80},
		"long":     {5173},
		"exposed":  {9000},
		"none":     nil,
	}

	for _, s := range services {
		t.Run(s.Name, func(t *testing.T) {
			if !slices.Equal(s.Ports, want[s.Name]) {
				t.Errorf("Ports = %v, want %v", s.Ports, want[s.Name])
			}
		})
	}
}

func TestReadServicesRejectsUnusableFiles(t *testing.T) {
	tests := []struct{ name, content string }{
		{"no services block", "version: \"3\"\n"},
		{"empty services block", "services:\n"},
		{"not yaml at all", "this: [is: broken\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ReadServices(composeFileAt(t, tt.content))
			if err == nil {
				t.Fatal("want an error")
			}
			if errs.Hint(err) == "" {
				t.Error("the error carries no hint")
			}
		})
	}
}

func TestReadServicesReportsAMissingFile(t *testing.T) {
	_, err := ReadServices(filepath.Join(t.TempDir(), "nope.yml"))
	if err == nil {
		t.Fatal("want an error for a file that is not there")
	}
	if errs.Hint(err) == "" {
		t.Error("the error carries no hint")
	}
}
