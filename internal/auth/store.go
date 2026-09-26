// Package auth keeps pit's own logins to hosting services: in the
// system's keychain, one per host, and got through the service's OAuth
// device flow or pasted in by hand.
//
// A login is the fallback, not the first choice: a token in the
// environment wins over it, and for GitHub, without either, pit goes on
// asking gh.
package auth

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/thannoz/pit/internal/errs"
)

// Credential is what pit keeps for a host.
type Credential struct {
	// Token is what the host's API is asked with.
	Token string `json:"token"`
	// Refresh gets a new Token when this one expires, from TokenURL, for
	// the application ClientID names. GitHub's tokens do not expire;
	// GitLab's last two hours.
	Refresh  string    `json:"refresh,omitempty"`
	Expires  time.Time `json:"expires,omitzero"`
	TokenURL string    `json:"token_url,omitempty"`
	ClientID string    `json:"client_id,omitempty"`
	// User is whose it is, as the host answered when it was stored.
	User string `json:"user,omitempty"`
}

// Expired says the token needs refreshing before it is used: a minute
// early, so that it does not expire on its way.
func (c Credential) Expired(now time.Time) bool {
	return !c.Expires.IsZero() && now.After(c.Expires.Add(-time.Minute))
}

// Store keeps credentials by host.
type Store interface {
	// Get returns the host's credential, or ErrNone.
	Get(host string) (Credential, error)
	Set(host string, c Credential) error
	// Delete forgets the host's credential; ErrNone when there is none.
	Delete(host string) error
	// Hosts are the hosts it has credentials for.
	Hosts() ([]string, error)
}

// ErrNone says a host has no login.
var ErrNone = errors.New("no login")

// Keychain keeps credentials in the system's keychain: the macOS
// keychain, the Secret Service of a Linux desktop, the Windows
// Credential Manager. Nothing is written to a file: a machine without a
// keychain has no logins, and is told to use the environment instead.
type Keychain struct {
	// Service is what the entries are filed under: "pit".
	Service string
}

// hostsKey is the entry that lists the hosts. A host name cannot hold
// parentheses, so it cannot be one.
const hostsKey = "(hosts)"

var _ Store = Keychain{}

// Get reads a host's credential.
func (k Keychain) Get(host string) (Credential, error) {
	raw, err := keyring.Get(k.Service, host)
	if err != nil {
		return Credential{}, k.fail(err, "cannot read the login for %s from the keychain", host)
	}
	var c Credential
	if err := json.Unmarshal([]byte(raw), &c); err != nil || c.Token == "" {
		return Credential{}, errs.New("the keychain's login for %s is not one pit wrote", host).
			WithHint("run `pit auth logout %s` and log in again", host)
	}
	return c, nil
}

// Set stores a host's credential and adds the host to the list.
func (k Keychain) Set(host string, c Credential) error {
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	if err := keyring.Set(k.Service, host, string(data)); err != nil {
		return k.fail(err, "cannot store the login for %s in the keychain", host)
	}
	hosts, err := k.Hosts()
	if err != nil || slices.Contains(hosts, host) {
		return err
	}
	return k.setHosts(append(hosts, host))
}

// Delete forgets a host's credential.
func (k Keychain) Delete(host string) error {
	err := keyring.Delete(k.Service, host)
	if err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return k.fail(err, "cannot remove the login for %s from the keychain", host)
	}
	hosts, herr := k.Hosts()
	if herr != nil {
		return herr
	}
	if i := slices.Index(hosts, host); i >= 0 {
		if herr := k.setHosts(slices.Delete(hosts, i, i+1)); herr != nil {
			return herr
		}
	}
	if err != nil {
		return ErrNone
	}
	return nil
}

// Hosts lists the hosts with a login.
func (k Keychain) Hosts() ([]string, error) {
	raw, err := keyring.Get(k.Service, hostsKey)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, k.fail(err, "cannot read pit's logins from the keychain")
	}
	var hosts []string
	if err := json.Unmarshal([]byte(raw), &hosts); err != nil {
		// A list pit cannot read is started again: what it lists is
		// only what `pit auth status` shows.
		return nil, nil
	}
	return hosts, nil
}

func (k Keychain) setHosts(hosts []string) error {
	if len(hosts) == 0 {
		if err := keyring.Delete(k.Service, hostsKey); err != nil && !errors.Is(err, keyring.ErrNotFound) {
			return k.fail(err, "cannot update pit's logins in the keychain")
		}
		return nil
	}
	slices.Sort(hosts)
	data, err := json.Marshal(hosts)
	if err != nil {
		return err
	}
	if err := keyring.Set(k.Service, hostsKey, string(data)); err != nil {
		return k.fail(err, "cannot update pit's logins in the keychain")
	}
	return nil
}

// fail says what went wrong with the keychain, and what to do about a
// machine that has none.
func (k Keychain) fail(err error, format string, args ...any) error {
	if errors.Is(err, keyring.ErrNotFound) {
		return ErrNone
	}
	wrapped := errs.Wrap(err, format, args...)
	if unavailable(err) {
		return wrapped.WithHint("this machine has no keychain pit can use; set the service's token in the environment instead (GH_TOKEN, GITLAB_TOKEN, ...)")
	}
	return wrapped
}

// unavailable recognises a machine without a keychain: a Linux server
// with no Secret Service, or a system go-keyring does not support.
func unavailable(err error) bool {
	if errors.Is(err, keyring.ErrUnsupportedPlatform) {
		return true
	}
	text := err.Error()
	return strings.Contains(text, "org.freedesktop.secrets") || strings.Contains(text, "dbus") ||
		strings.Contains(text, "DBUS_SESSION_BUS_ADDRESS")
}

// Memory keeps credentials for as long as it lives: for tests, and for
// anything that must not reach the keychain.
type Memory struct {
	mu    sync.Mutex
	creds map[string]Credential
}

var _ Store = (*Memory)(nil)

// Get returns a host's credential.
func (m *Memory) Get(host string) (Credential, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.creds[host]
	if !ok {
		return Credential{}, ErrNone
	}
	return c, nil
}

// Set stores a host's credential.
func (m *Memory) Set(host string, c Credential) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.creds == nil {
		m.creds = map[string]Credential{}
	}
	m.creds[host] = c
	return nil
}

// Delete forgets a host's credential.
func (m *Memory) Delete(host string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.creds[host]; !ok {
		return ErrNone
	}
	delete(m.creds, host)
	return nil
}

// Hosts lists the hosts, sorted.
func (m *Memory) Hosts() ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	hosts := make([]string, 0, len(m.creds))
	for h := range m.creds {
		hosts = append(hosts, h)
	}
	slices.Sort(hosts)
	return hosts, nil
}
