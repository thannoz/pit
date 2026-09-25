package cli

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/thannoz/pit/internal/config"
	"github.com/thannoz/pit/internal/data"
	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/snapshot"
	"github.com/thannoz/pit/internal/state"
	"github.com/thannoz/pit/internal/ui"
	"github.com/thannoz/pit/internal/view"
)

type broughtUp struct {
	base     bool
	scenario string
}

// withCompare answers compare's two bring-ups: the pull request's with
// the data it had, the base's with what it was asked for -- or fails
// the base with baseErr.
func withCompare(t *testing.T, ownScenario string, ownEdited bool, snap *snapshot.Snapshot, baseErr error) *[]broughtUp {
	t.Helper()
	var calls []broughtUp
	prevPlan, prevBring := planUp, bringUp
	planUp = func(c *cobra.Command, _ *upOptions, _ string) (*upPlan, error) {
		out := ui.New(c.OutOrStdout(), c.ErrOrStderr())
		return &upPlan{c: c, out: out, rep: newStepReporter(out, c.ErrOrStderr()), snap: snap}, nil
	}
	bringUp = func(_ *upPlan, base bool, scenario string) (state.Sandbox, error) {
		calls = append(calls, broughtUp{base, scenario})
		if base {
			if baseErr != nil {
				return state.Sandbox{}, baseErr
			}
			box := state.Sandbox{PR: 482, Base: true, URL: "http://localhost:40999/", SHA: "8c21f0d1e2b3", Scenario: scenario}
			if snap != nil {
				box.Scenario, box.Snapshot = snap.Scenario, snap.ID
			}
			return box, nil
		}
		box := state.Sandbox{PR: 482, URL: "http://localhost:40482/", SHA: "a3f91c2e4b7d", Scenario: ownScenario, Edited: ownEdited}
		if scenario != "" {
			box.Scenario = scenario
		}
		return box, nil
	}
	t.Cleanup(func() { planUp, bringUp = prevPlan, prevBring })
	return &calls
}

// TestCompare is the acceptance criterion for T-902, as far as it is
// pit's: two sandboxes, the pull request's and the base's, the second
// on the data the first has, and both URLs given.
func TestCompare(t *testing.T) {
	calls := withCompare(t, "refunded", false, nil, nil)
	out, stderr, err := run(t, "compare", "482")
	if err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 2 || (*calls)[0] != (broughtUp{false, ""}) || (*calls)[1] != (broughtUp{true, "refunded"}) {
		t.Errorf("brought up %+v", *calls)
	}
	if !strings.Contains(out, "#482       http://localhost:40482/  a3f91c2\n#482 base  http://localhost:40999/  8c21f0d\n") {
		t.Errorf("stdout:\n%s", out)
	}
	if !strings.Contains(stderr, "both with scenario refunded") {
		t.Errorf("stderr:\n%s", stderr)
	}
}

func TestCompareWithANamedScenario(t *testing.T) {
	calls := withCompare(t, "standard", false, nil, nil)
	if _, _, err := run(t, "compare", "482", "--scenario=bulk"); err != nil {
		t.Fatal(err)
	}
	if (*calls)[0] != (broughtUp{false, "bulk"}) || (*calls)[1] != (broughtUp{true, "bulk"}) {
		t.Errorf("brought up %+v", *calls)
	}
}

// A snapshot goes into both as it is; no scenario is named for the base.
func TestCompareWithASnapshot(t *testing.T) {
	calls := withCompare(t, "standard", false, &snapshot.Snapshot{ID: "sn_7f3a1b", Scenario: "standard"}, nil)
	_, stderr, err := run(t, "compare", "482")
	if err != nil {
		t.Fatal(err)
	}
	if (*calls)[1] != (broughtUp{true, ""}) || !strings.Contains(stderr, "both with snapshot sn_7f3a1b") {
		t.Errorf("brought up %+v:\n%s", *calls, stderr)
	}
}

func TestCompareOnEditedData(t *testing.T) {
	withCompare(t, "standard", true, nil, nil)
	_, stderr, err := run(t, "compare", "482")
	if err != nil || !strings.Contains(stderr, "#482's data was changed by hand since it was loaded; the base has it as loaded") {
		t.Errorf("%v:\n%s", err, stderr)
	}
}

func TestCompareWhenTheBaseFails(t *testing.T) {
	t.Run("a scenario of the pull request's", func(t *testing.T) {
		withCompare(t, "refunded", false, nil, wrapUnknown(t))
		_, _, err := run(t, "compare", "482")
		if err == nil || !strings.Contains(err.Error(), "its base cannot have the same data") ||
			!strings.Contains(hintOf(err), `scenario "refunded" comes with the pull request`) {
			t.Errorf("err = %v, hint %q", err, hintOf(err))
		}
	})
	t.Run("anything else", func(t *testing.T) {
		withCompare(t, "standard", false, nil, errors.New("port in use"))
		_, _, err := run(t, "compare", "482")
		if err == nil || !strings.Contains(err.Error(), "its base could not be brought up: port in use") ||
			!strings.Contains(hintOf(err), "`pit down 482 --base`") {
			t.Errorf("err = %v, hint %q", err, hintOf(err))
		}
	})
}

// wrapUnknown is the error data gives for a scenario it does not have.
func wrapUnknown(t *testing.T) error {
	t.Helper()
	cfg := mustConfig(t, withScenarios)
	_, err := data.Select(cfg, "refunded")
	if !data.IsUnknownScenario(err) {
		t.Fatalf("not an unknown scenario: %v", err)
	}
	return err
}

func TestCompareJSON(t *testing.T) {
	withCompare(t, "standard", false, nil, nil)
	out, _, err := run(t, "compare", "482", "--json")
	var r compareJSON
	if err != nil || json.Unmarshal([]byte(out), &r) != nil || r.URL != "http://localhost:40482/" || r.BaseURL != "http://localhost:40999/" || r.Scenario != "standard" {
		t.Errorf("%v:\n%s", err, out)
	}
}

func TestCompareOpensBoth(t *testing.T) {
	withCompare(t, "standard", false, nil, nil)
	opened := withOpened(t)
	if _, _, err := run(t, "compare", "482", "--open"); err != nil {
		t.Fatal(err)
	}
	if strings.Join(*opened, " ") != "http://localhost:40482/ http://localhost:40999/" {
		t.Errorf("opened %v", *opened)
	}
}

func mustConfig(t *testing.T, yaml string) *config.Config {
	t.Helper()
	cfg, err := config.Parse([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func hintOf(err error) string { return errs.Hint(err) }

func TestCompareHasNoBaseFlag(t *testing.T) {
	calls := withCompare(t, "standard", false, nil, nil)
	if _, _, err := run(t, "compare", "482", "--base"); err == nil || len(*calls) != 0 {
		t.Errorf("err = %v, brought up %+v", err, *calls)
	}
}

func TestCompareSideBySide(t *testing.T) {
	withCompare(t, "standard", false, nil, nil)
	opened := withOpened(t)
	var sides []view.Side
	previous := serveView
	serveView = func(_ context.Context, left, right view.Side, ready func(string)) error {
		sides = []view.Side{left, right}
		ready("http://127.0.0.1:51234/")
		return nil
	}
	t.Cleanup(func() { serveView = previous })

	out, stderr, err := run(t, "compare", "482", "--view")
	if err != nil {
		t.Fatal(err)
	}
	if len(sides) != 2 || sides[0].Target != "http://localhost:40482/" || sides[1].Target != "http://localhost:40999/" ||
		sides[0].Label != "#482" || sides[1].Label != "#482 base" || sides[1].Detail != "default branch at 8c21f0d" {
		t.Errorf("sides %+v", sides)
	}
	if !strings.Contains(out, "Side by side: http://127.0.0.1:51234/") || !strings.Contains(stderr, "Ctrl+C stops showing them") {
		t.Errorf("stdout:\n%s\nstderr:\n%s", out, stderr)
	}
	if strings.Join(*opened, " ") != "http://127.0.0.1:51234/" {
		t.Errorf("opened %v", *opened)
	}
	if _, _, err := run(t, "compare", "482", "--view", "--json"); err == nil {
		t.Error("--view with --json")
	}
}
