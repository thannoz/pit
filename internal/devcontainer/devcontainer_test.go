package devcontainer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestStandardTakesOutWhatJSONHasNot(t *testing.T) {
	in := `{
	// a comment, "with quotes"
	"url": "https://example.com/a//b", /* a block
	over lines */ "glob": "src/**/*.ts",
	"quote": "say \"//\" here",
	"list": [1, 2,],
	"nested": {"a": 1,},
}`
	out := standard([]byte(in))
	if len(out) != len(in) || strings.Count(string(out), "\n") != strings.Count(in, "\n") {
		t.Errorf("the lines moved:\n%s", out)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	want := map[string]any{
		"url": "https://example.com/a//b", "glob": "src/**/*.ts", "quote": `say "//" here`,
		"list": []any{1.0, 2.0}, "nested": map[string]any{"a": 1.0},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v", got)
	}
	// A comma that something follows stays.
	if got := string(standard([]byte(`[1, /* x */ 2]`))); got != `[1,         2]` {
		t.Errorf("got %q", got)
	}
}

// tryNode is the devcontainer.json of microsoft/vscode-remote-try-node,
// which T-1002 ran against.
const tryNode = `// For format details, see https://aka.ms/devcontainer.json.
{
	"name": "Node.js",
	"image": "mcr.microsoft.com/devcontainers/javascript-node:1-18-bullseye",
	// "features": {},
	"customizations": {"vscode": {"settings": {}, "extensions": ["streetsidesoftware.code-spell-checker"]}},
	// "forwardPorts": [3000],
	"portsAttributes": {
		"3000": {"label": "Hello Remote World", "onAutoForward": "notify"}
	},
	"postCreateCommand": "npm install"
	// "remoteUser": "root"
}`

var vars = Vars{Workspace: "/wt/pr-7", Basename: "shop", ID: "pit-shop-7", Env: func(k string) string {
	return map[string]string{"USER": "lisa"}[k]
}}

func TestParseAnImageBasedFile(t *testing.T) {
	f, err := Parse([]byte(tryNode), vars)
	if err != nil {
		t.Fatal(err)
	}
	if f.Image != "mcr.microsoft.com/devcontainers/javascript-node:1-18-bullseye" || f.Compose() {
		t.Errorf("image = %q", f.Image)
	}
	if f.WorkspaceFolder != "/workspaces/shop" {
		t.Errorf("workspaceFolder = %q", f.WorkspaceFolder)
	}
	if !reflect.DeepEqual(f.Ports(), []int{3000}) {
		t.Errorf("ports = %v", f.Ports())
	}
	if len(f.PostCreateCommand) != 1 || f.PostCreateCommand[0].Shell != "npm install" {
		t.Errorf("postCreateCommand = %+v", f.PostCreateCommand)
	}
}

func TestParseFillsInTheVariables(t *testing.T) {
	f, err := Parse([]byte(`{
		"image": "x",
		"workspaceFolder": "/src/${localWorkspaceFolderBasename}",
		"containerEnv": {
			"HOME_USER": "${localEnv:USER}",
			"MISSING": "${localEnv:NOPE:fallback}",
			"EMPTY": "${localEnv:NOPE}",
			"WS": "${containerWorkspaceFolder}/bin:${containerWorkspaceFolderBasename}",
			"ID": "${devcontainerId}",
			"LOCAL": "${localWorkspaceFolder}"
		},
		"remoteEnv": {"PATH": "${containerEnv:PATH}:/extra", "ODD": "${unknownThing}"}
	}`), vars)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"HOME_USER": "lisa", "MISSING": "fallback", "EMPTY": "",
		"WS": "/src/shop/bin:shop", "ID": "pit-shop-7", "LOCAL": "/wt/pr-7",
	}
	if !reflect.DeepEqual(f.ContainerEnv, want) {
		t.Errorf("containerEnv = %v", f.ContainerEnv)
	}
	if f.RemoteEnv["PATH"] != "${PATH}:/extra" || f.RemoteEnv["ODD"] != "${unknownThing}" {
		t.Errorf("remoteEnv = %v", f.RemoteEnv)
	}
}

func TestParseTheOtherForms(t *testing.T) {
	f, err := Parse([]byte(`{
		"dockerComposeFile": ["../compose.yml", "compose.dev.yml"],
		"service": "app",
		"runServices": ["app", "db"],
		"forwardPorts": [8000, "db:5432", "app:9229"],
		"appPort": "4000",
		"mounts": [
			"source=cache-${devcontainerId},target=/root/.cache,type=volume",
			{"type": "bind", "source": "/tmp", "target": "/host-tmp"},
			"source=/etc/hosts,target=/etc/hosts2,type=bind,readonly",
			"source=gems,target=/gems"
		],
		"onCreateCommand": ["make", "deps"],
		"postStartCommand": {"server": "rails s", "assets": ["yarn", "watch"], "empty": "", "jobs": "sidekiq", "css": "sass --watch", "b": "b", "z": "z"}
	}`), vars)
	if err != nil {
		t.Fatal(err)
	}
	if !f.Compose() || !reflect.DeepEqual([]string(f.ComposeFiles), []string{"../compose.yml", "compose.dev.yml"}) {
		t.Errorf("compose files = %v", f.ComposeFiles)
	}
	if f.WorkspaceFolder != "/" {
		t.Errorf("a compose file's workspace is / by default, got %q", f.WorkspaceFolder)
	}
	f.PortsAttributes = map[string]json.RawMessage{"9000": nil, "5173": nil, "8000": nil, "debug": nil}
	if !reflect.DeepEqual(f.Ports(), []int{8000, 9229, 4000, 5173, 9000}) {
		t.Errorf("ports = %v; another service's port is not the app's", f.Ports())
	}
	wantMounts := []Mount{
		{Type: "volume", Source: "cache-pit-shop-7", Target: "/root/.cache"},
		{Type: "bind", Source: "/tmp", Target: "/host-tmp"},
		{Type: "bind", Source: "/etc/hosts", Target: "/etc/hosts2", ReadOnly: true},
		{Type: "volume", Source: "gems", Target: "/gems"},
	}
	if !reflect.DeepEqual(f.Mounts, wantMounts) {
		t.Errorf("mounts = %+v", f.Mounts)
	}
	if len(f.OnCreateCommand) != 1 || !reflect.DeepEqual(f.OnCreateCommand[0].Args, []string{"make", "deps"}) {
		t.Errorf("onCreateCommand = %+v", f.OnCreateCommand)
	}
	// Side by side in the file, one after the other by name in pit;
	// an empty one is nothing to run.
	got := f.PostStartCommand
	var names []string
	for _, c := range got {
		names = append(names, c.Name)
	}
	if !reflect.DeepEqual(names, []string{"assets", "b", "css", "jobs", "server", "z"}) || got[0].String() != "yarn watch" || got[4].Shell != "rails s" {
		t.Errorf("postStartCommand = %+v", got)
	}
}

func TestParseTheOlderBuildAndASingleForwardedPort(t *testing.T) {
	f, err := Parse([]byte(`{"dockerFile": "Dockerfile", "forwardPorts": 3000}`), vars)
	if err != nil {
		t.Fatal(err)
	}
	if f.Build == nil || f.Build.Dockerfile != "Dockerfile" || f.Build.Context != "." {
		t.Errorf("build = %+v", f.Build)
	}
	if !reflect.DeepEqual(f.Ports(), []int{3000}) {
		t.Errorf("ports = %v", f.Ports())
	}
}

func TestParseSaysWhatIsWrong(t *testing.T) {
	for name, tc := range map[string]struct{ in, want string }{
		"syntax":         {"{\n\"image\": \"x\"\n\"name\": 1}", "line 3"},
		"not an object":  {`[1]`, "is not an object"},
		"no service":     {`{"dockerComposeFile": "c.yml"}`, "no service"},
		"nothing to run": {`{"name": "x"}`, "neither which image"},
		"a bad port":     {`{"image": "x", "forwardPorts": ["web:http"]}`, "forwardPorts"},
		"a bad mount":    {`{"image": "x", "mounts": ["source=a"]}`, "mounts[0]"},
		"a bad command":  {`{"image": "x", "postCreateCommand": 3}`, "postCreateCommand"},
	} {
		_, err := Parse([]byte(tc.in), vars)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", name, err, tc.want)
		}
	}
}

func TestFindLooksWhereTheSpecificationDoes(t *testing.T) {
	dir := t.TempDir()
	if _, ok := Find(dir); ok {
		t.Error("found one in an empty directory")
	}
	write := func(p string) {
		t.Helper()
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(".devcontainer/python/devcontainer.json")
	write(".devcontainer/node/devcontainer.json")
	if got, _ := Find(dir); got != ".devcontainer/node/devcontainer.json" {
		t.Errorf("got %q", got)
	}
	write(".devcontainer.json")
	if got, _ := Find(dir); got != ".devcontainer.json" {
		t.Errorf("got %q", got)
	}
	write(".devcontainer/devcontainer.json")
	if got, _ := Find(dir); got != ".devcontainer/devcontainer.json" {
		t.Errorf("got %q", got)
	}
}

func TestLoadNamesTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "devcontainer.json")
	if _, err := Load(path, vars); err == nil || !strings.Contains(err.Error(), "cannot read") {
		t.Errorf("err = %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"name": "x"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path, vars); err == nil || !strings.Contains(err.Error(), "cannot use "+path) {
		t.Errorf("err = %v", err)
	}
}
