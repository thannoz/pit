package report

import (
	"strings"
	"testing"
	"time"

	"github.com/thannoz/pit/internal/notes"
)

func utc(s string) time.Time {
	t, err := time.Parse(time.RFC3339, "2026-09-25T"+s+"Z")
	if err != nil {
		panic(err)
	}
	return t
}

// TestLogsInTheComment is the acceptance criterion for T-807: the
// comment carries what the services wrote around the note, in the order
// they wrote it, and nothing more.
func TestLogsInTheComment(t *testing.T) {
	n := voucher
	n.Logs = []notes.Log{
		{Service: "web", Skipped: 12, Lines: []notes.LogLine{
			{At: utc("10:31:02"), Text: "\x1b[97;42m 200 \x1b[0m GET /orders/1002"},
			{At: utc("10:31:04"), Text: "GET /api/basket 404\tin 1ms"},
			{At: utc("10:31:05"), Text: "retrying with Authorization: Bearer abc.def"},
		}},
		{Service: "db", Lines: []notes.LogLine{
			{At: utc("10:31:03"), Text: `ERROR:  column "refunded_cents" does not exist at character 8`},
		}},
	}
	got := Comment(Input{PR: 7, Notes: []notes.Note{n}})
	want := "\n<details>\n<summary>What web and db logged around this (times in UTC)</summary>\n\n" +
		"```text\n" +
		"(12 earlier lines of web left out)\n" +
		"10:31:02 web   200  GET /orders/1002\n" +
		"10:31:03 db   ERROR:  column \"refunded_cents\" does not exist at character 8\n" +
		"10:31:04 web  GET /api/basket 404 in 1ms\n" +
		"10:31:05 web  retrying with Authorization: [redacted]\n" +
		"```\n\n</details>\n"
	if !strings.Contains(got, want) {
		t.Errorf("the comment lacks\n%s\nin\n%s", want, got)
	}
	// After what went wrong on the page, before the picture.
	if i, j := strings.Index(got, "**No answer:**"), strings.Index(got, "<details>"); i < 0 || j < i {
		t.Errorf("the logs are not after the page's problems:\n%s", got)
	}
}

func TestOneServiceHasNoColumn(t *testing.T) {
	n := voucher
	n.Logs = []notes.Log{{Service: "web", Lines: []notes.LogLine{{At: utc("10:31:02"), Text: "GET /orders/1002 200"}}}}
	got := Comment(Input{PR: 7, Notes: []notes.Note{n}})
	if !strings.Contains(got, "<summary>What web logged around this (times in UTC)</summary>") ||
		!strings.Contains(got, "```text\n10:31:02  GET /orders/1002 200\n```") {
		t.Errorf("the comment is\n%s", got)
	}
}

func TestNoLogsNoDetails(t *testing.T) {
	if got := Comment(Input{PR: 7, Notes: []notes.Note{voucher}}); strings.Contains(got, "<details>") {
		t.Errorf("the comment is\n%s", got)
	}
}

// A log with backticks of its own does not end the block early.
func TestLogFence(t *testing.T) {
	n := voucher
	n.Logs = []notes.Log{{Service: "web", Lines: []notes.LogLine{{At: utc("10:31:02"), Text: "rendered ```js block``` and ````more````"}}}}
	got := Comment(Input{PR: 7, Notes: []notes.Note{n}})
	if !strings.Contains(got, "`````text\n10:31:02  rendered ```js block``` and ````more````\n`````\n") {
		t.Errorf("the comment is\n%s", got)
	}
}

func TestClipLog(t *testing.T) {
	if got := clipLog("\x1b[1;31mfailed\x1b[0m\x07 to\tconnect\r   "); got != "failed to connect" {
		t.Errorf("clipLog = %q", got)
	}
	if got := clipLog(strings.Repeat("é", 1000)); len([]rune(got)) != maxLogText+1 || !strings.HasSuffix(got, "…") {
		t.Errorf("clipLog kept %d", len([]rune(got)))
	}
}

func TestRedact(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.e30.abc", "Authorization: [redacted]"},
		{"calling upstream with bearer abc.def-ghi", "calling upstream with Bearer [redacted]"},
		{"cookie: session=abc123; theme=dark", "cookie: [redacted]"},
		{"login attempt password=hunter2 user=lisa", "login attempt password=[redacted] user=lisa"},
		{`config {"api_key": "sk_live_123", "region": "eu"}`, `config {"api_key": "[redacted]", "region": "eu"}`},
		{"GET /callback?code=1&access_token=abc&state=x", "GET /callback?code=1&access_token=[redacted]&state=x"},
		{"env DB_PASSWORD=s3cret STRIPE_SECRET_KEY=sk_test_1", "env DB_PASSWORD=[redacted] STRIPE_SECRET_KEY=[redacted]"},
		{"client_secret: abc", "client_secret: [redacted]"},
		{"GITHUB_TOKEN_READONLY=ghp_1 SESSION_SECRET_V2: xyz", "GITHUB_TOKEN_READONLY=[redacted] SESSION_SECRET_V2: [redacted]"},
		{"connecting to postgres://app:s3cret@db:5432/shop", "connecting to postgres://app:[redacted]@db:5432/shop"},
		{"redis://:pa55@cache:6379", "redis://:[redacted]@cache:6379"},
		// What only mentions a secret keeps its words.
		{"GET /orders/1001 200 in 3ms", "GET /orders/1001 200 in 3ms"},
		{"token expired for user 42", "token expired for user 42"},
		{"passwordless login enabled", "passwordless login enabled"},
		{"http://localhost:8080/orders", "http://localhost:8080/orders"},
	} {
		if got := redact(tc.in); got != tc.want {
			t.Errorf("redact(%q)\n = %q\nwant %q", tc.in, got, tc.want)
		}
	}
}
