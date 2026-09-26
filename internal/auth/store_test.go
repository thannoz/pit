package auth

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/thannoz/pit/internal/errs"
)

// No test reaches the keychain of the machine it runs on.
func init() { keyring.MockInit() }

func TestKeychainKeepsCredentialsByHost(t *testing.T) {
	keyring.MockInit()
	k := Keychain{Service: "pit-test"}
	if _, err := k.Get("github.com"); !errors.Is(err, ErrNone) {
		t.Fatalf("empty keychain: %v", err)
	}
	if hosts, err := k.Hosts(); err != nil || len(hosts) != 0 {
		t.Fatalf("hosts %q, %v", hosts, err)
	}

	gh := Credential{Token: "gho_a", User: "octocat", ClientID: "Iv1.x"}
	gl := Credential{Token: "gl_a", Refresh: "r", Expires: time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC), TokenURL: "https://gitlab.com/oauth/token", ClientID: "c"}
	for host, c := range map[string]Credential{"gitlab.com": gl, "github.com": gh} {
		if err := k.Set(host, c); err != nil {
			t.Fatal(err)
		}
	}
	// Stored again, a host is listed once.
	if err := k.Set("github.com", gh); err != nil {
		t.Fatal(err)
	}
	if got, err := k.Get("gitlab.com"); err != nil || !got.Expires.Equal(gl.Expires) || got.Refresh != "r" || got.TokenURL != gl.TokenURL {
		t.Errorf("gitlab.com = %+v, %v", got, err)
	}
	if hosts, _ := k.Hosts(); !slices.Equal(hosts, []string{"github.com", "gitlab.com"}) {
		t.Errorf("hosts = %q", hosts)
	}

	if err := k.Delete("github.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := k.Get("github.com"); !errors.Is(err, ErrNone) {
		t.Errorf("after delete: %v", err)
	}
	if err := k.Delete("github.com"); !errors.Is(err, ErrNone) {
		t.Errorf("deleted twice: %v", err)
	}
	if hosts, _ := k.Hosts(); !slices.Equal(hosts, []string{"gitlab.com"}) {
		t.Errorf("hosts = %q", hosts)
	}
	// The last one takes the list with it.
	if err := k.Delete("gitlab.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := keyring.Get("pit-test", hostsKey); !errors.Is(err, keyring.ErrNotFound) {
		t.Errorf("the list is left: %v", err)
	}

	// An entry pit did not write is not taken for a login.
	if err := keyring.Set("pit-test", "codeberg.org", "not json"); err != nil {
		t.Fatal(err)
	}
	if _, err := k.Get("codeberg.org"); err == nil || errors.Is(err, ErrNone) {
		t.Errorf("foreign entry: %v", err)
	}
	// Nor is one that is JSON, but holds no token.
	if err := keyring.Set("pit-test", "gitea.com", `{"user": "me"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := k.Get("gitea.com"); err == nil || !strings.Contains(err.Error(), "not one pit wrote") {
		t.Errorf("entry without a token: %v", err)
	}
}

func TestAKeychainThatIsNotThereSaysWhatToDo(t *testing.T) {
	keyring.MockInitWithError(errors.New("The name org.freedesktop.secrets was not provided by any .service files"))
	t.Cleanup(keyring.MockInit)
	k := Keychain{Service: "pit-test"}
	err := k.Set("github.com", Credential{Token: "t"})
	if err == nil || !strings.Contains(errs.Hint(err), "GH_TOKEN") {
		t.Errorf("err = %v, hint %q", err, errs.Hint(err))
	}
	if _, err := k.Get("github.com"); err == nil || errors.Is(err, ErrNone) {
		t.Errorf("get: %v", err)
	}
}

func TestMemory(t *testing.T) {
	m := &Memory{}
	if _, err := m.Get("a"); !errors.Is(err, ErrNone) {
		t.Error(err)
	}
	_ = m.Set("b.example", Credential{Token: "1"})
	_ = m.Set("a.example", Credential{Token: "2"})
	if hosts, _ := m.Hosts(); !slices.Equal(hosts, []string{"a.example", "b.example"}) {
		t.Errorf("hosts %q", hosts)
	}
	if c, err := m.Get("a.example"); err != nil || c.Token != "2" {
		t.Errorf("%+v %v", c, err)
	}
	if err := m.Delete("a.example"); err != nil {
		t.Error(err)
	}
	if err := m.Delete("a.example"); !errors.Is(err, ErrNone) {
		t.Error(err)
	}
}

func TestExpired(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		expires time.Time
		want    bool
	}{
		{time.Time{}, false},
		{now.Add(time.Hour), false},
		// A minute early, so it does not expire on its way.
		{now.Add(30 * time.Second), true},
		{now.Add(-time.Hour), true},
	} {
		if got := (Credential{Expires: tc.expires}).Expired(now); got != tc.want {
			t.Errorf("%v: %v", tc.expires, got)
		}
	}
}
