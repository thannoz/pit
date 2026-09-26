package config

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestProcessesTakeThePlaceOfComposeFiles(t *testing.T) {
	root := project(t, map[string]string{
		FileName:   "processes:\n  file: Procfile\n  setup: [npm ci]\nweb: {service: web}\n",
		"Procfile": "web: node index.js\n",
	})
	c, err := Load(filepath.Join(root, FileName))
	if err != nil {
		t.Fatal(err)
	}
	// No compose file, and no port: the process is given its own.
	if len(c.Compose.Files) != 0 || !c.Processes.On() || c.Web.Port != 0 {
		t.Errorf("config = %+v", c)
	}
	// Setup runs on this machine, and says so.
	if !slices.Contains(c.HostCommands(), "npm ci") {
		t.Errorf("host commands = %q", c.HostCommands())
	}
}

func TestProcessesAreChecked(t *testing.T) {
	procfile := map[string]string{"Procfile": "web: node index.js\n", "Broken": "web node index.js\n"}
	for name, tc := range map[string]struct{ yaml, want string }{
		"no Procfile": {
			"processes: {file: Procfile.dev}\nweb: {service: web}\n",
			`processes.file: names "Procfile.dev", which does not exist`,
		},
		"a broken Procfile": {
			"processes: {file: Broken}\nweb: {service: web}\n",
			`processes.file: names "Broken", which pit cannot read: line 1 is not a process`,
		},
		"with compose files": {
			"processes: {file: Procfile}\ncompose: {files: [docker-compose.yml]}\nweb: {service: web}\n",
			"processes.file: is set together with compose.files or devcontainer.file",
		},
		"setup without a Procfile": {
			"processes: {setup: [npm ci]}\nweb: {service: web, port: 80}\n",
			"processes.setup: is set without processes.file",
		},
		"a compose command": {
			"processes: {file: Procfile}\nweb: {service: web}\ndata:\n  migrate: [\"compose exec -T web npm run migrate\"]\n",
			"data.migrate[0]: runs through compose, and the services are processes",
		},
		"a broken setup command": {
			"processes: {file: Procfile, setup: [\"npm 'ci\"]}\nweb: {service: web}\n",
			"processes.setup[0]",
		},
	} {
		err := loadBroken(t, tc.yaml, procfile)
		if !strings.Contains(err.Error(), tc.want) || !hasLineNumber.MatchString(err.Error()) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	// Without processes, a port is still needed.
	if err := loadBroken(t, "web: {service: web}\n", nil); !strings.Contains(err.Error(), "web.port") {
		t.Errorf("err = %v", err)
	}
}

func TestInitWritesProcesses(t *testing.T) {
	for _, setup := range [][]string{{"npm ci", "bundle install"}, nil} {
		data, err := Render(InitOptions{Processes: "Procfile", Setup: setup, WebService: "web", Services: []string{"web", "worker"}})
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		c, err := Parse(data)
		if err != nil || c.Processes.File != "Procfile" || !slices.Equal(c.Processes.Setup, setup) || c.Web.Service != "web" || c.Web.Port != 0 || len(c.Compose.Files) != 0 {
			t.Errorf("setup %q: %+v, %v\n%s", setup, c, err, text)
		}
		if !strings.Contains(text, "version: 1\n\n# The services are the processes of a Procfile") || strings.Contains(text, "\ncompose:") || strings.Contains(text, "\n  port:") {
			t.Errorf("setup %q:\n%s", setup, text)
		}
		if setup == nil && !strings.Contains(text, `  #   - "npm ci"`) {
			t.Errorf("no example:\n%s", text)
		}
	}
	o := InitOptions{Processes: "Procfile.dev", WebService: "web"}
	if got := o.Summary(); got != "web, from Procfile.dev" {
		t.Errorf("summary = %q", got)
	}
}
