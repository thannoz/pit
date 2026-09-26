package auth

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/thannoz/pit/internal/errs"
)

// DeviceFlow logs in the way RFC 8628 describes, which needs no
// browser on this machine and no server of pit's: pit shows a code,
// the reviewer enters it on the host's page, wherever they are logged
// in, and pit asks until the host hands over a token.
type DeviceFlow struct {
	// Host names the service in what is said: "github.com".
	Host string
	// CodeURL hands out the code; TokenURL the token.
	CodeURL, TokenURL string
	// ClientID is pit's application at the host. The device flow needs
	// no secret, which pit could not keep anyway.
	ClientID string
	// Scopes are what the token may do.
	Scopes []string
	// Client makes the requests; nil makes them with a timeout.
	Client *http.Client
	// Sleep waits between two questions; nil waits for real.
	Sleep func(context.Context, time.Duration) error
	// Now is the clock; nil is the system's.
	Now func() time.Time
}

// Code is what the reviewer is shown.
type Code struct {
	// UserCode is what they type, on the page at VerificationURI, or
	// have filled in for them at VerificationURIComplete.
	UserCode                string
	VerificationURI         string
	VerificationURIComplete string

	deviceCode string
	interval   time.Duration
	expires    time.Time
}

// Token is what the host hands over.
type Token struct {
	Access  string
	Refresh string
	// Expires is when Access stops working; zero for never.
	Expires time.Time
}

// Start asks the host for a code.
func (d DeviceFlow) Start(ctx context.Context) (Code, error) {
	form := url.Values{"client_id": {d.ClientID}}
	if len(d.Scopes) > 0 {
		form.Set("scope", strings.Join(d.Scopes, " "))
	}
	var raw struct {
		DeviceCode              string `json:"device_code"`
		UserCode                string `json:"user_code"`
		VerificationURI         string `json:"verification_uri"`
		VerificationURIComplete string `json:"verification_uri_complete"`
		ExpiresIn               int    `json:"expires_in"`
		Interval                int    `json:"interval"`
	}
	answer, err := d.post(ctx, d.CodeURL, form, &raw)
	if err != nil {
		return Code{}, err
	}
	if answer.Error != "" {
		return Code{}, d.refused(answer)
	}
	if raw.DeviceCode == "" || raw.UserCode == "" || raw.VerificationURI == "" {
		return Code{}, errs.New("%s did not hand out a code", d.Host).
			WithHint("the address pit asked, %s, may not be one for logging in", d.CodeURL)
	}
	interval := time.Duration(raw.Interval) * time.Second
	if interval <= 0 {
		// RFC 8628's default.
		interval = 5 * time.Second
	}
	expires := raw.ExpiresIn
	if expires <= 0 {
		expires = 900
	}
	return Code{
		UserCode:                raw.UserCode,
		VerificationURI:         raw.VerificationURI,
		VerificationURIComplete: raw.VerificationURIComplete,
		deviceCode:              raw.DeviceCode,
		interval:                interval,
		expires:                 d.now().Add(time.Duration(expires) * time.Second),
	}, nil
}

// Wait asks the host, as often as it allows, until the code has been
// entered, refused or has expired.
func (d DeviceFlow) Wait(ctx context.Context, c Code) (Token, error) {
	interval := c.interval
	form := url.Values{
		"client_id":   {d.ClientID},
		"device_code": {c.deviceCode},
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
	}
	for {
		if err := d.sleep(ctx, interval); err != nil {
			return Token{}, errs.Wrap(err, "stopped waiting for the code to be entered")
		}
		if d.now().After(c.expires) {
			return Token{}, errs.New("the code %s expired before it was entered", c.UserCode).
				WithHint("run `pit auth login %s` again for a new one", d.Host)
		}
		var raw tokenAnswer
		answer, err := d.post(ctx, d.TokenURL, form, &raw)
		if err != nil {
			return Token{}, err
		}
		switch answer.Error {
		case "":
			return raw.token(d.Host, d.now())
		case "authorization_pending":
			continue
		case "slow_down":
			// The host says how long to wait now, or RFC 8628 does.
			if answer.Interval > 0 {
				interval = time.Duration(answer.Interval) * time.Second
			} else {
				interval += 5 * time.Second
			}
			continue
		case "expired_token", "token_expired":
			return Token{}, errs.New("the code %s expired before it was entered", c.UserCode).
				WithHint("run `pit auth login %s` again for a new one", d.Host)
		case "access_denied":
			return Token{}, errs.New("the login to %s was declined on its page", d.Host).
				WithHint("nothing was stored; run `pit auth login %s` again to allow it", d.Host)
		}
		return Token{}, d.refused(answer)
	}
}

// Refresh exchanges a refresh token for a new token, at now.
func Refresh(ctx context.Context, client *http.Client, host string, c Credential, now time.Time) (Token, error) {
	d := DeviceFlow{Host: host, TokenURL: c.TokenURL, ClientID: c.ClientID, Client: client, Now: func() time.Time { return now }}
	form := url.Values{
		"client_id":     {c.ClientID},
		"grant_type":    {"refresh_token"},
		"refresh_token": {c.Refresh},
	}
	var raw tokenAnswer
	answer, err := d.post(ctx, c.TokenURL, form, &raw)
	if err != nil {
		return Token{}, err
	}
	if answer.Error != "" {
		return Token{}, errs.New("%s would not renew pit's login: %s", host, answer.describe()).
			WithHint("run `pit auth login %s` again", host)
	}
	return raw.token(host, d.now())
}

// tokenAnswer is a token, as OAuth hands one over.
type tokenAnswer struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
}

func (t tokenAnswer) token(host string, now time.Time) (Token, error) {
	if t.AccessToken == "" {
		return Token{}, errs.New("%s answered without a token", host)
	}
	tok := Token{Access: t.AccessToken, Refresh: t.RefreshToken}
	if t.ExpiresIn > 0 {
		tok.Expires = now.Add(time.Duration(t.ExpiresIn) * time.Second)
	}
	return tok, nil
}

// oauthError is what OAuth says instead of an answer. GitHub says it
// with 200, GitLab with 400; both in the body.
type oauthError struct {
	Error       string `json:"error"`
	Description string `json:"error_description"`
	Interval    int    `json:"interval"`
}

func (e oauthError) describe() string {
	if e.Description != "" {
		return e.Description
	}
	return e.Error
}

// refused explains an error the flow cannot go on from.
func (d DeviceFlow) refused(e oauthError) error {
	err := errs.New("%s refused the login: %s", d.Host, e.describe())
	switch e.Error {
	// GitHub says an application it does not know is "Not Found".
	case "invalid_client", "incorrect_client_credentials", "unauthorized_client", "Not Found":
		return err.WithHint("%s does not know the application %q; for a server of your own, register one there and pass its ID with --client-id", d.Host, d.ClientID)
	case "device_flow_disabled":
		return err.WithHint("the application %q does not allow the device flow; enable it in its settings on %s", d.ClientID, d.Host)
	case "invalid_scope":
		return err.WithHint("the application may not ask for %s", strings.Join(d.Scopes, ", "))
	}
	return err
}

// post sends a form and reads the answer into out, or the error OAuth
// says instead.
func (d DeviceFlow) post(ctx context.Context, to string, form url.Values, out any) (oauthError, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, to, strings.NewReader(form.Encode()))
	if err != nil {
		return oauthError{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// GitHub answers in a form of its own unless asked for JSON.
	req.Header.Set("Accept", "application/json")
	client := d.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return oauthError{}, errs.Wrap(err, "cannot reach %s", d.Host).
			WithHint("check the network; pit logs in at %s", to)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return oauthError{}, err
	}
	var e oauthError
	_ = json.Unmarshal(data, &e)
	if e.Error != "" {
		return e, nil
	}
	if resp.StatusCode/100 != 2 {
		return oauthError{}, errs.New("%s answered %d at %s", d.Host, resp.StatusCode, to).
			WithHint("the address may not be one for logging in; for a server of your own, check that it is %s", d.Host)
	}
	if err := json.Unmarshal(data, out); err != nil {
		return oauthError{}, errs.Wrap(err, "cannot read what %s answered", d.Host)
	}
	return oauthError{}, nil
}

func (d DeviceFlow) sleep(ctx context.Context, dur time.Duration) error {
	if d.Sleep != nil {
		return d.Sleep(ctx, dur)
	}
	t := time.NewTimer(dur)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (d DeviceFlow) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}
