// Package secrets fetches what a sandbox's services need and a
// repository must not hold: from 1Password, through its CLI, or from
// Vault, through its own. The values go to the services when pit starts
// them and nowhere else; pit writes none of them down.
package secrets

import (
	"context"
	"errors"
	"os/exec"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/thannoz/pit/internal/errs"
	"github.com/thannoz/pit/internal/proc"
)

// The stores a reference can name.
const (
	OnePassword = "1Password"
	Vault       = "Vault"
)

// Ref is where one secret is kept.
type Ref struct {
	// Store is OnePassword or Vault.
	Store string
	// Raw is the reference as written: op://vault/item/field, or
	// vault://path#field.
	Raw string
	// Path and Field are Vault's: the secret's path, with its mount,
	// and the field of it.
	Path, Field string
}

// name is what a variable may be called, for the services and for
// Compose, which reads the value from its own environment.
var name = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ValidName reports whether a secret can be given to the services
// under this name.
func ValidName(n string) bool { return name.MatchString(n) }

// Parse reads a reference.
func Parse(raw string) (Ref, error) {
	switch {
	case strings.HasPrefix(raw, "op://"):
		// 1Password's own secret reference: op://vault/item/field,
		// with an optional section before the field.
		parts := strings.Split(strings.TrimPrefix(raw, "op://"), "/")
		if len(parts) < 3 || len(parts) > 4 || slices.Contains(parts, "") {
			return Ref{}, errs.New("%q is not a 1Password reference; it reads op://vault/item/field", raw)
		}
		return Ref{Store: OnePassword, Raw: raw}, nil
	case strings.HasPrefix(raw, "vault://"):
		path, field, _ := strings.Cut(strings.TrimPrefix(raw, "vault://"), "#")
		path = strings.Trim(path, "/")
		if path == "" || field == "" {
			return Ref{}, errs.New("%q is not a Vault reference; it reads vault://mount/path#field", raw)
		}
		return Ref{Store: Vault, Raw: raw, Path: path, Field: field}, nil
	}
	return Ref{}, errs.New("%q names no store pit knows; a secret is op://… for 1Password or vault://…#field for Vault", raw)
}

// Command is what asks the store for a secret. It prints the value
// alone, without a line ending of its own.
func (r Ref) Command() proc.Command {
	if r.Store == OnePassword {
		return proc.Command{Name: "op", Args: []string{"read", "--no-newline", r.Raw}}
	}
	return proc.Command{Name: "vault", Args: []string{"kv", "get", "-field=" + r.Field, r.Path}}
}

// Runner runs the stores' CLIs. internal/proc.Exec is one.
type Runner interface {
	Output(ctx context.Context, c proc.Command) ([]byte, error)
}

// Resolve fetches every secret, by the name it is given to the
// services under. What went wrong names the secret and the store,
// never a value.
func Resolve(ctx context.Context, r Runner, refs map[string]string) (map[string]string, error) {
	names := make([]string, 0, len(refs))
	for n := range refs {
		names = append(names, n)
	}
	sort.Strings(names)
	values := make(map[string]string, len(refs))
	for _, n := range names {
		ref, err := Parse(refs[n])
		if err != nil {
			return nil, errs.Wrap(err, "env.secrets.%s", n)
		}
		out, err := r.Output(ctx, ref.Command())
		if err != nil {
			return nil, errs.Wrap(err, "cannot fetch %s from %s", n, ref.Store).WithHint("%s", hint(ref, err))
		}
		values[n] = strings.TrimSuffix(string(out), "\n")
	}
	return values, nil
}

func hint(r Ref, err error) string {
	missing := errors.Is(err, exec.ErrNotFound)
	if r.Store == OnePassword {
		if missing {
			return "install the 1Password CLI (https://developer.1password.com/docs/cli/) and sign in with `op signin`"
		}
		return "sign in with `op signin`, or set OP_SERVICE_ACCOUNT_TOKEN; `op read " + r.Raw + "` reproduces it"
	}
	if missing {
		return "install the Vault CLI (https://developer.hashicorp.com/vault/install), and set VAULT_ADDR and VAULT_TOKEN"
	}
	return "check VAULT_ADDR and VAULT_TOKEN; `vault kv get -field=" + r.Field + " " + r.Path + "` reproduces it"
}

// Stores are the stores refs name, for saying where secrets came from.
func Stores(refs map[string]string) []string {
	var stores []string
	for _, raw := range refs {
		if ref, err := Parse(raw); err == nil && !slices.Contains(stores, ref.Store) {
			stores = append(stores, ref.Store)
		}
	}
	sort.Strings(stores)
	return stores
}

// EnvPrefix is before the name of the variable Compose reads a secret
// from while it starts the services, so that it does not stand in for
// a variable of the project's own compose file.
const EnvPrefix = "PIT_SECRET_"
