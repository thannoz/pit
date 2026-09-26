package secrets

import (
	"context"
	"errors"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/proc"
)

func TestParse(t *testing.T) {
	for raw, want := range map[string]Ref{
		"op://Dev/Stripe/key":               {Store: OnePassword, Raw: "op://Dev/Stripe/key"},
		"op://Dev/Stripe/test keys/key":     {Store: OnePassword, Raw: "op://Dev/Stripe/test keys/key"},
		"vault://secret/shop/db#password":   {Store: Vault, Raw: "vault://secret/shop/db#password", Path: "secret/shop/db", Field: "password"},
		"vault:///secret/shop/db/#password": {Store: Vault, Raw: "vault:///secret/shop/db/#password", Path: "secret/shop/db", Field: "password"},
	} {
		if got, err := Parse(raw); err != nil || got != want {
			t.Errorf("Parse(%q) = %+v, %v", raw, got, err)
		}
	}
	for raw, want := range map[string]string{
		"op://Dev/Stripe":             "it reads op://vault/item/field",
		"op://Dev//key":               "it reads op://vault/item/field",
		"op://a/b/c/d/e":              "it reads op://vault/item/field",
		"vault://secret/shop/db":      "it reads vault://mount/path#field",
		"vault://secret/shop/db#":     "it reads vault://mount/path#field",
		"vault://#password":           "it reads vault://mount/path#field",
		"sk_test_4242":                "names no store pit knows",
		"https://vault.example/x#key": "names no store pit knows",
	} {
		if _, err := Parse(raw); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Parse(%q) = %v", raw, err)
		}
	}
}

func TestCommands(t *testing.T) {
	op, _ := Parse("op://Dev/Stripe/key")
	if c := op.Command(); c.Name != "op" || !slices.Equal(c.Args, []string{"read", "--no-newline", "op://Dev/Stripe/key"}) {
		t.Errorf("op: %+v", c)
	}
	v, _ := Parse("vault://secret/shop/db#password")
	if c := v.Command(); c.Name != "vault" || !slices.Equal(c.Args, []string{"kv", "get", "-field=password", "secret/shop/db"}) {
		t.Errorf("vault: %+v", c)
	}
}

// stores answers as op and vault would, from a table of commands.
type stores struct {
	values map[string]string
	fail   error
	asked  []string
}

func (s *stores) Output(_ context.Context, c proc.Command) ([]byte, error) {
	s.asked = append(s.asked, c.String())
	if s.fail != nil {
		return nil, s.fail
	}
	v, ok := s.values[c.String()]
	if !ok {
		return nil, errors.New("no such secret")
	}
	return []byte(v), nil
}

func TestResolve(t *testing.T) {
	s := &stores{values: map[string]string{
		"op read --no-newline op://Dev/Stripe/key":    "sk_test_4242",
		"vault kv get -field=password secret/shop/db": "hunter2\n",
	}}
	got, err := Resolve(t.Context(), s, map[string]string{"STRIPE_KEY": "op://Dev/Stripe/key", "DB_PASSWORD": "vault://secret/shop/db#password"})
	if err != nil {
		t.Fatal(err)
	}
	// Vault's line ending is its own, not the secret's.
	if got["STRIPE_KEY"] != "sk_test_4242" || got["DB_PASSWORD"] != "hunter2" || len(got) != 2 {
		t.Errorf("got %q", got)
	}
	// In the order of their names, every time.
	if len(s.asked) != 2 || !strings.HasPrefix(s.asked[0], "vault") {
		t.Errorf("asked %q", s.asked)
	}
	// The same, whatever order the map is walked in.
	for range 50 {
		if got := Stores(map[string]string{"A": "op://a/b/c", "B": "vault://s/p#f", "C": "op://d/e/f", "D": "nonsense"}); !slices.Equal(got, []string{OnePassword, Vault}) {
			t.Fatalf("stores = %q", got)
		}
	}
}

// What went wrong names the secret and where it was asked for, and
// how to put it right; never a value.
func TestResolveSaysWhatWentWrong(t *testing.T) {
	for _, tc := range []struct {
		ref  string
		fail error
		want []string
	}{
		{"op://Dev/Stripe/key", errs.New("op exited with code 1\n    [ERROR] You are not currently signed in"), []string{"cannot fetch STRIPE_KEY from 1Password", "not currently signed in", "op signin", "op read op://Dev/Stripe/key"}},
		{"op://Dev/Stripe/key", errs.Wrap(exec.ErrNotFound, "op is not installed"), []string{"install the 1Password CLI"}},
		{"vault://secret/shop#key", errs.New("vault exited with code 2\n    permission denied"), []string{"cannot fetch STRIPE_KEY from Vault", "permission denied", "VAULT_TOKEN", "vault kv get -field=key secret/shop"}},
		{"vault://secret/shop#key", errs.Wrap(exec.ErrNotFound, "vault is not installed"), []string{"install the Vault CLI"}},
		{"sk_live_nope", nil, []string{"env.secrets.STRIPE_KEY", "names no store"}},
	} {
		s := &stores{fail: tc.fail}
		_, err := Resolve(t.Context(), s, map[string]string{"STRIPE_KEY": tc.ref})
		text := ""
		if err != nil {
			text = err.Error() + "\n" + errs.Hint(err)
		}
		for _, w := range tc.want {
			if err == nil || !strings.Contains(text, w) {
				t.Errorf("%s: %q lacks %q", tc.ref, text, w)
			}
		}
	}
}

func TestValidName(t *testing.T) {
	for n, want := range map[string]bool{"STRIPE_KEY": true, "_x1": true, "1X": false, "A-B": false, "": false, "A B": false} {
		if ValidName(n) != want {
			t.Errorf("ValidName(%q) != %v", n, want)
		}
	}
}
