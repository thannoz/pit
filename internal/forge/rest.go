package forge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/thannoz/pit/internal/errs"
)

// rest asks a hosting service's REST API: GitLab's, Gitea's, Bitbucket's.
type rest struct {
	// Service names it in what goes wrong: "GitLab answered 404".
	Service string
	// Host is the machine asked, for the same purpose.
	Host string
	// Client makes the requests; nil makes them with a timeout.
	Client *http.Client
}

// status is an answer that was not a success.
type status struct {
	service string
	code    int
	message string
}

func (s status) Error() string {
	if s.message == "" {
		return fmt.Sprintf("%s answered %d", s.service, s.code)
	}
	return fmt.Sprintf("%s answered %d: %s", s.service, s.code, s.message)
}

// call sends a request and reads the answer into out.
func (r rest) call(ctx context.Context, method, url string, headers map[string]string, payload []byte, out any) error {
	var body io.Reader
	if payload != nil {
		body = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	client := r.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		// GitLab says "message" or "error", Gitea "message", Bitbucket
		// {"error": {"message": ...}}.
		var m struct {
			Message any `json:"message"`
			Error   any `json:"error"`
		}
		_ = json.Unmarshal(data, &m)
		msg := firstOf(m.Message, m.Error)
		if nested, ok := msg.(map[string]any); ok {
			msg = firstOf(nested["message"])
		}
		return status{service: r.Service, code: resp.StatusCode, message: fmt.Sprint(msg)}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return errs.Wrap(err, "cannot read what %s answered", r.Host)
	}
	return nil
}

func firstOf(values ...any) any {
	for _, v := range values {
		if v != nil {
			return v
		}
	}
	return ""
}
