package cli

import (
	"reflect"
	"testing"
)

// TestRealManagerWiresEveryDependency guards the one thing this file
// does. Proc was left nil here for three stages: no test noticed
// because every test builds its own manager, and the first repository
// with a hook configured would have crashed on a nil interface instead
// of running it. Checking the whole struct catches the next omission
// without anyone having to remember this one.
func TestRealManagerWiresEveryDependency(t *testing.T) {
	t.Setenv("PIT_STATE_DIR", t.TempDir())

	m, err := realManager()
	if err != nil {
		t.Fatalf("realManager: %v", err)
	}

	v := reflect.ValueOf(*m)
	for i := range v.NumField() {
		if v.Field(i).IsZero() {
			t.Errorf("%s is not wired", v.Type().Field(i).Name)
		}
	}
}
