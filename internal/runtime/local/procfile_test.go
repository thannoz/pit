package local

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseProcfile(t *testing.T) {
	got, err := ParseProcfile([]byte("# Heroku's\nweb: bundle exec puma -p $PORT\n\n  worker:   bin/jobs  \r\nrelease: rails db:migrate\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := []Process{{"web", "bundle exec puma -p $PORT"}, {"worker", "bin/jobs"}, {"release", "rails db:migrate"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v", got)
	}
	for in, msg := range map[string]string{
		"web npm start\n":    `line 1 is not a process: "web npm start"`,
		"web: a\nweb: b\n":   "line 2 names web a second time",
		"web:\n":             "line 1 names web, but no command",
		"# only a comment\n": "names no processes",
		"web: a\nweb.2: b\n": "line 2 is not a process",
	} {
		if _, err := ParseProcfile([]byte(in)); err == nil || !strings.Contains(err.Error(), msg) {
			t.Errorf("%q: err = %v, want %q", in, err, msg)
		}
	}
}

func TestReadProcfileNamesTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Procfile")
	if _, err := ReadProcfile(path); err == nil || !strings.Contains(err.Error(), "cannot read") {
		t.Errorf("err = %v", err)
	}
	if err := os.WriteFile(path, []byte("oops\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadProcfile(path); err == nil || !strings.Contains(err.Error(), path+": line 1") {
		t.Errorf("err = %v", err)
	}
}

func TestPlanGivesPortsAndEnvironment(t *testing.T) {
	procs := []Process{{"worker", "a"}, {"web", "b"}, {"css", "c"}}
	p := Plan{Project: "pit-shop-7", Web: "web", Port: 40007, Env: map[string]string{"B": "2", "A": "1", "E": "5", "C": "3", "D": "4", "F": "6"}}
	for name, want := range map[string]int{"web": 40007, "worker": 40107, "css": 40207, "mail": 0} {
		if got := p.PortOf(procs, name); got != want {
			t.Errorf("%s: port %d, want %d", name, got, want)
		}
	}
	want := []string{"A=1", "B=2", "C=3", "D=4", "E=5", "F=6", "PORT=40107", "PIT_PORT=40007", "PIT_PROJECT=pit-shop-7"}
	if got := p.Environment(procs, "worker"); !reflect.DeepEqual(got, want) {
		t.Errorf("worker: %q", got)
	}
	// A command run for the sandbox sees the web process's port.
	if got := p.Environment(procs, ""); got[6] != "PORT=40007" {
		t.Errorf("a command: %q", got)
	}

	path := filepath.Join(t.TempDir(), "state", "pr-7.processes.json")
	if err := WritePlan(path, p); err != nil {
		t.Fatal(err)
	}
	back, err := ReadPlan(path)
	if err != nil || !reflect.DeepEqual(back, p) {
		t.Errorf("read back %+v, %v", back, err)
	}
	if _, err := ReadPlan(filepath.Join(t.TempDir(), "none.json")); err == nil {
		t.Error("read a plan that is not there")
	}
}
