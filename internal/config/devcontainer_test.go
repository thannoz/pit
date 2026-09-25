package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestADevcontainerTakesThePlaceOfComposeFiles(t *testing.T) {
	root := project(t, map[string]string{
		FileName:                          "devcontainer:\n  file: .devcontainer/devcontainer.json\n  start: npm start\nweb: {service: dev, port: 3000}\n",
		".devcontainer/devcontainer.json": `{"image": "node"}`,
	})
	c, err := Load(filepath.Join(root, FileName))
	if err != nil {
		t.Fatal(err)
	}
	// Not the compose file beside it: the devcontainer.json says what runs.
	if len(c.Compose.Files) != 0 || c.Devcontainer.File != ".devcontainer/devcontainer.json" || c.Devcontainer.Start != "npm start" {
		t.Errorf("config = %+v", c)
	}
}

func TestADevcontainerIsChecked(t *testing.T) {
	for name, tc := range map[string]struct{ yaml, want string }{
		"a file that is not there": {
			"devcontainer:\n  file: .devcontainer/missing.json\nweb: {service: dev, port: 3000}\n",
			`devcontainer.file: names ".devcontainer/missing.json", which does not exist`,
		},
		"a directory": {
			"devcontainer:\n  file: .devcontainer\nweb: {service: dev, port: 3000}\n",
			`devcontainer.file: names ".devcontainer", which is a directory`,
		},
		"both": {
			"devcontainer:\n  file: .devcontainer/devcontainer.json\ncompose:\n  files: [docker-compose.yml]\nweb: {service: dev, port: 3000}\n",
			"devcontainer.file: is set together with compose.files",
		},
		"a start without a file": {
			"devcontainer:\n  start: npm start\nweb: {service: web, port: 3000}\n",
			"devcontainer.start: is set without devcontainer.file",
		},
	} {
		err := loadBroken(t, tc.yaml, map[string]string{".devcontainer/devcontainer.json": `{"image": "node"}`})
		if !strings.Contains(err.Error(), tc.want) || !hasLineNumber.MatchString(err.Error()) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestInitWritesADevcontainer(t *testing.T) {
	for _, start := range []string{"npm start", ""} {
		data, err := Render(InitOptions{Devcontainer: ".devcontainer/devcontainer.json", Start: start, WebService: "dev", WebPort: 3000, Services: []string{"dev"}})
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		if strings.Contains(text, "files:") || !strings.Contains(text, "version: 1\n\n# The services come from the project's devcontainer.json") {
			t.Errorf("start %q:\n%s", start, text)
		}
		c, err := Parse(data)
		if err != nil || c.Devcontainer.File != ".devcontainer/devcontainer.json" || c.Devcontainer.Start != start || len(c.Compose.Files) != 0 {
			t.Errorf("start %q: %+v, %v\n%s", start, c, err, text)
		}
		if start == "" && !strings.Contains(text, "  # start: npm run dev") {
			t.Errorf("no example of a start command:\n%s", text)
		}
	}
	o := InitOptions{Devcontainer: ".devcontainer.json", WebService: "dev", WebPort: 3000}
	if got := o.Summary(); got != "dev on port 3000, from .devcontainer.json" {
		t.Errorf("summary = %q", got)
	}
	// A compose project reads as it did.
	data, err := Render(InitOptions{ComposeFiles: []string{"compose.yaml"}, WebService: "web", WebPort: 80, Services: []string{"web"}})
	if err != nil || !strings.Contains(string(data), "version: 1\n\ncompose:\n  files:\n    - compose.yaml\n") || strings.Contains(string(data), "devcontainer") {
		t.Errorf("%v\n%s", err, data)
	}
}
