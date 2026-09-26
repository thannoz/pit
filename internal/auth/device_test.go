package auth

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thannoz/pit/internal/errs"
)

// oauthServer answers like a host's OAuth endpoints: the code at
// /code, then each poll of /token with the next of polls.
type oauthServer struct {
	mu    sync.Mutex
	forms []url.Values
	polls []answer
	code  answer
}

type answer struct {
	status int
	body   string
}

func newOAuth(t *testing.T, code answer, polls ...answer) (*oauthServer, string) {
	t.Helper()
	o := &oauthServer{code: code, polls: polls}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		form, _ := url.ParseQuery(string(body))
		o.mu.Lock()
		defer o.mu.Unlock()
		if r.Header.Get("Accept") != "application/json" || r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			w.WriteHeader(http.StatusNotAcceptable)
			return
		}
		form.Set("path", r.URL.Path)
		o.forms = append(o.forms, form)
		a := o.code
		if r.URL.Path == "/token" {
			if len(o.polls) == 0 {
				t.Errorf("asked once too often")
				w.WriteHeader(http.StatusTeapot)
				return
			}
			a, o.polls = o.polls[0], o.polls[1:]
		}
		w.WriteHeader(a.status)
		_, _ = io.WriteString(w, a.body)
	}))
	t.Cleanup(srv.Close)
	return o, srv.URL
}

// clock is time that passes only when the flow sleeps.
type clock struct {
	now    time.Time
	slept  []time.Duration
	cancel func()
	after  int
}

func (c *clock) Now() time.Time { return c.now }
func (c *clock) Sleep(ctx context.Context, d time.Duration) error {
	c.slept = append(c.slept, d)
	c.now = c.now.Add(d)
	if c.cancel != nil && len(c.slept) > c.after {
		c.cancel()
	}
	return ctx.Err()
}

func flow(base string, c *clock) DeviceFlow {
	return DeviceFlow{Host: "github.com", CodeURL: base + "/code", TokenURL: base + "/token", ClientID: "Iv1.pit",
		Scopes: []string{"repo", "read:org"}, Sleep: c.Sleep, Now: c.Now}
}

// GitHub's answers: the code as its docs show it, every error with 200.
const githubCode = `{"device_code":"3584d83530557fdd1f46af8289938c8ef79f9dc5","user_code":"WDJB-MJHT","verification_uri":"https://github.com/login/device","expires_in":900,"interval":5}`

func TestTheDeviceFlowAsGitHubRunsIt(t *testing.T) {
	o, base := newOAuth(t, answer{200, githubCode},
		answer{200, `{"error":"authorization_pending","error_description":"The authorization request is still pending."}`},
		answer{200, `{"error":"slow_down","error_description":"Too many requests have been made in the same timeframe.","interval":15}`},
		answer{200, `{"error":"authorization_pending"}`},
		answer{200, `{"access_token":"gho_16C7e42F292c6912E7710c838347Ae178B4a","token_type":"bearer","scope":"repo,read:org"}`},
	)
	c := &clock{now: time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)}
	d := flow(base, c)
	code, err := d.Start(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if code.UserCode != "WDJB-MJHT" || code.VerificationURI != "https://github.com/login/device" {
		t.Errorf("code = %+v", code)
	}
	tok, err := d.Wait(t.Context(), code)
	if err != nil {
		t.Fatal(err)
	}
	if tok.Access != "gho_16C7e42F292c6912E7710c838347Ae178B4a" || tok.Refresh != "" || !tok.Expires.IsZero() {
		t.Errorf("token = %+v", tok)
	}
	// It waited as long as it was told, and longer once told to slow
	// down.
	want := []time.Duration{5 * time.Second, 5 * time.Second, 15 * time.Second, 15 * time.Second}
	if len(c.slept) != len(want) {
		t.Fatalf("slept %v", c.slept)
	}
	for i := range want {
		if c.slept[i] != want[i] {
			t.Errorf("slept %v, want %v", c.slept, want)
		}
	}
	first, poll := o.forms[0], o.forms[1]
	if first.Get("path") != "/code" || first.Get("client_id") != "Iv1.pit" || first.Get("scope") != "repo read:org" {
		t.Errorf("asked for the code with %v", first)
	}
	if poll.Get("path") != "/token" || poll.Get("device_code") != "3584d83530557fdd1f46af8289938c8ef79f9dc5" ||
		poll.Get("grant_type") != "urn:ietf:params:oauth:grant-type:device_code" || poll.Get("client_id") != "Iv1.pit" {
		t.Errorf("polled with %v", poll)
	}
}

// GitLab says its errors with 400, and hands over a token that expires
// with one to renew it.
func TestTheDeviceFlowAsGitLabRunsIt(t *testing.T) {
	_, base := newOAuth(t,
		answer{200, `{"device_code":"dc","user_code":"0A44L90H","verification_uri":"https://gitlab.com/oauth/device","verification_uri_complete":"https://gitlab.com/oauth/device?user_code=0A44L90H","expires_in":300,"interval":5}`},
		answer{400, `{"error":"authorization_pending","error_description":"The authorization request is still pending"}`},
		// slow_down without an interval: five seconds more.
		answer{400, `{"error":"slow_down"}`},
		answer{200, `{"access_token":"glo_a","token_type":"Bearer","expires_in":7200,"refresh_token":"glr_b","scope":"api","created_at":1790000000}`},
	)
	c := &clock{now: time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)}
	d := flow(base, c)
	code, err := d.Start(t.Context())
	if err != nil || code.VerificationURIComplete != "https://gitlab.com/oauth/device?user_code=0A44L90H" {
		t.Fatalf("%+v, %v", code, err)
	}
	tok, err := d.Wait(t.Context(), code)
	if err != nil {
		t.Fatal(err)
	}
	if tok.Access != "glo_a" || tok.Refresh != "glr_b" || !tok.Expires.Equal(c.now.Add(2*time.Hour)) {
		t.Errorf("token = %+v (now %v)", tok, c.now)
	}
	if c.slept[2] != 10*time.Second {
		t.Errorf("slept %v", c.slept)
	}
}

func TestTheDeviceFlowEnds(t *testing.T) {
	short := `{"device_code":"dc","user_code":"ABCD-EFGH","verification_uri":"https://github.com/login/device","expires_in":12,"interval":5}`
	for _, tc := range []struct {
		name       string
		polls      []answer
		want, hint string
	}{
		{"declined", []answer{{200, `{"error":"access_denied","error_description":"The authorization request was denied."}`}},
			"declined on its page", "nothing was stored"},
		{"expired at the host", []answer{{200, `{"error":"expired_token"}`}}, "ABCD-EFGH expired", "pit auth login github.com"},
		// Three polls of five seconds are past twelve: pit stops asking.
		{"expired here", []answer{{200, `{"error":"authorization_pending"}`}, {200, `{"error":"authorization_pending"}`}},
			"ABCD-EFGH expired", "again"},
		{"the application is gone", []answer{{200, `{"error":"incorrect_client_credentials","error_description":"The client_id is not valid."}`}},
			"refused the login: The client_id is not valid.", "--client-id"},
		{"no device flow", []answer{{200, `{"error":"device_flow_disabled"}`}}, "refused the login", "enable it"},
		{"no token", []answer{{200, `{"token_type":"bearer"}`}}, "without a token", ""},
		{"not OAuth", []answer{{502, `<html>bad gateway</html>`}}, "answered 502", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, base := newOAuth(t, answer{200, short}, tc.polls...)
			c := &clock{now: time.Now()}
			d := flow(base, c)
			code, err := d.Start(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			_, err = d.Wait(t.Context(), code)
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(errs.Hint(err), tc.hint) {
				t.Errorf("err = %v, hint %q", err, errs.Hint(err))
			}
		})
	}
}

func TestTheDeviceFlowStopsWhenInterrupted(t *testing.T) {
	_, base := newOAuth(t, answer{200, githubCode}, answer{200, `{"error":"authorization_pending"}`})
	ctx, cancel := context.WithCancel(t.Context())
	c := &clock{now: time.Now(), cancel: cancel, after: 1}
	d := flow(base, c)
	code, err := d.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Wait(ctx, code); err == nil || !strings.Contains(err.Error(), "stopped waiting") {
		t.Errorf("err = %v", err)
	}
}

func TestTheCodeIsRefused(t *testing.T) {
	for _, tc := range []struct {
		code       answer
		want, hint string
	}{
		{answer{200, `{"error":"unauthorized_client","error_description":"Client authentication failed"}`}, "Client authentication failed", "does not know the application"},
		// What github.com answers to an application it does not know.
		{answer{404, `{"error":"Not Found"}`}, "refused the login: Not Found", "does not know the application"},
		{answer{400, `{"error":"invalid_scope"}`}, "invalid_scope", "repo, read:org"},
		{answer{404, `Not Found`}, "answered 404", "server of your own"},
		{answer{200, `{"verification_uri":"https://github.com/login/device"}`}, "did not hand out a code", "/code"},
		{answer{200, `{"user_code":"ABCD-EFGH","verification_uri":"https://github.com/login/device"}`}, "did not hand out a code", "/code"},
		{answer{200, `{"device_code":"dc","user_code":"ABCD-EFGH"}`}, "did not hand out a code", "/code"},
	} {
		_, base := newOAuth(t, tc.code)
		c := &clock{now: time.Now()}
		_, err := flow(base, c).Start(t.Context())
		if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(errs.Hint(err), tc.hint) {
			t.Errorf("err = %v, hint %q", err, errs.Hint(err))
		}
	}
	d := DeviceFlow{Host: "github.invalid", CodeURL: "http://127.0.0.1:1/code"}
	if _, err := d.Start(t.Context()); err == nil || !strings.Contains(err.Error(), "cannot reach github.invalid") {
		t.Errorf("err = %v", err)
	}
}

// Without an interval or an expiry, RFC 8628's defaults hold.
func TestTheCodeHasDefaults(t *testing.T) {
	_, base := newOAuth(t, answer{200, `{"device_code":"dc","user_code":"U","verification_uri":"https://h/device"}`})
	c := &clock{now: time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)}
	code, err := flow(base, c).Start(t.Context())
	if err != nil || code.interval != 5*time.Second || !code.expires.Equal(c.now.Add(15*time.Minute)) {
		t.Errorf("%+v, %v", code, err)
	}
}
