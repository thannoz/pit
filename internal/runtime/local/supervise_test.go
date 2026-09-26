//go:build unix

package local

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
)

// Processes end whenever they end: their states are saved at the same
// time as each other's, and every save must land whole.
func TestSavesAtTheSameTimeLandWhole(t *testing.T) {
	s := &supervisor{dir: t.TempDir(), state: state{Processes: map[string]procState{}}}
	var wg sync.WaitGroup
	errs := make(chan error, 50)
	for i := range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.set("p"+strconv.Itoa(i), procState{Running: true})
			errs <- s.save()
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(filepath.Join(s.dir, statusFile))
	if err != nil {
		t.Fatal(err)
	}
	var got state
	if err := json.Unmarshal(data, &got); err != nil || len(got.Processes) != 50 {
		t.Errorf("%d processes, %v", len(got.Processes), err)
	}
}
