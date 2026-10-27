package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// THE TRAP OF THE BOT API: it answers HTTP 200 with {"ok":false} for a wrong
// chat id, a bot removed from the group, or a user who blocked it. Trusting the
// status code is how a channel reports perfect health while delivering nothing
// — which is precisely the silent failure an alerting must not have.
func TestTelegram200ButOkFalseIsAFailure(t *testing.T) {
	var srv = httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":false,"error_code":400,` +
				`"description":"Bad Request: chat not found"}`))
		}))
	defer srv.Close()

	var tg = Telegram{Token: "123456:AAbbCCddEEffGG", ChatID: "-1001", BaseURL: srv.URL}
	var err = tg.Send(context.Background(), Message{Title: "x", Severity: 3})
	if err == nil {
		t.Fatal("a 200 with ok:false must be an error")
	}
	if !strings.Contains(err.Error(), "chat not found") {
		t.Fatalf("the far end's diagnostic must come through, not be swallowed: %v", err)
	}
}

func TestTelegramSuccessAndPayload(t *testing.T) {
	var vu map[string]any
	var srv = httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			var body, _ = io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &vu)
			if !strings.HasSuffix(r.URL.Path, "/sendMessage") {
				t.Errorf("unexpected path %s", r.URL.Path)
			}
			_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
		}))
	defer srv.Close()

	var tg = Telegram{Token: "tok", ChatID: "42", BaseURL: srv.URL}
	var err = tg.Send(context.Background(), Message{
		Title: "port.down", Body: "GE1 down", Severity: 3,
		Site: "ARENA", Device: "sw01", Port: "GE1",
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if vu["chat_id"] != "42" {
		t.Fatalf("chat_id = %v", vu["chat_id"])
	}
	var text, _ = vu["text"].(string)
	// No parse_mode is sent: unparsed text cannot be rejected for containing an
	// unescaped bracket out of a device name.
	var _, present = vu["parse_mode"]
	if present {
		t.Fatal("no parse_mode must be sent")
	}
	// The severity must be readable as a WORD, not only as an emoji: a channel
	// read through a screen reader or an email gateway still has to carry it.
	if !strings.Contains(text, "WARNING") {
		t.Fatalf("the severity must be readable in words: %q", text)
	}
	if !strings.Contains(text, "ARENA") || !strings.Contains(text, "sw01") {
		t.Fatalf("the message must name its subject: %q", text)
	}
}

// A device name routinely contains characters Telegram's MarkdownV2 and Slack's
// mrkdwn treat as syntax. Plain text cannot be malformed, and this locks that
// nothing escapes or mangles them.
func TestHostileNamesPassIntact(t *testing.T) {
	var m = Message{Title: "port.down", Severity: 3, Device: "sw-[labo]_#1 (*core*)"}
	var out = plain(m)
	if !strings.Contains(out, "sw-[labo]_#1 (*core*)") {
		t.Fatalf("the name must pass verbatim: %q", out)
	}
}

// `last_error` is returned by the channel-list route, which is secret-free by
// contract. A transport error embeds the request URL, and for both channels the
// URL IS the credential.
func TestTheSecretDoesNotLeakIntoTheError(t *testing.T) {
	var token = "123456:AAbbCCddEEffGGhhIIjj"
	var scrubbed = scrub(`Post "https://api.telegram.org/bot`+token+
		`/sendMessage": dial tcp: lookup failed`, token)
	if strings.Contains(scrubbed, token) {
		t.Fatalf("the token leaked: %q", scrubbed)
	}
	if !strings.Contains(scrubbed, "[redacted]") {
		t.Fatalf("the redaction must be visible: %q", scrubbed)
	}
	// A short "secret" is not scrubbed: it would redact ordinary words out of
	// every diagnostic and make errors unreadable.
	if scrub("dial tcp failed", "abc") != "dial tcp failed" {
		t.Fatal("a secret that is too short must not be processed")
	}
}

func TestSlackRefusesPlainHTTP(t *testing.T) {
	var s = Slack{WebhookURL: "http://hooks.slack.com/services/x/y/z"}
	var err = s.Send(context.Background(), Message{Title: "x"})
	if err == nil || !strings.Contains(err.Error(), "https") {
		t.Fatalf("a plaintext webhook must be refused, not degraded: %v", err)
	}
}

// A revoked hook answers 404 "no_service", a wrong payload 400
// "invalid_payload". Each tells the operator a different thing to fix, so the
// body must reach them verbatim rather than being flattened to "failed".
func TestSlackSurfacesTheDiagnostic(t *testing.T) {
	var err = checkSlackResponse(404, "404 Not Found", "no_service")
	if err == nil || err.Error() != "no_service" {
		t.Fatalf("the diagnostic must come through verbatim: %v", err)
	}
	err = checkSlackResponse(400, "400 Bad Request", "invalid_payload")
	if err == nil || err.Error() != "invalid_payload" {
		t.Fatalf("diagnostic: %v", err)
	}
	// An empty body falls back to the status line rather than to an empty
	// error, which would read as success in a log.
	err = checkSlackResponse(500, "500 Internal Server Error", "")
	if err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("an empty body must fall back to the status: %v", err)
	}
	if checkSlackResponse(200, "200 OK", "ok") != nil {
		t.Fatal("a 200 must pass")
	}
}

// Truncation must cut on a rune boundary: invalid UTF-8 is rejected by both
// APIs, so a bad cut loses the alert the cut was meant to save.
func TestTruncationRespectsRunes(t *testing.T) {
	var long = strings.Repeat("日", 4000)
	var out = truncate(long, 100)
	if len(out) > 120 {
		t.Fatalf("truncation too long: %d bytes", len(out))
	}
	if !strings.HasSuffix(out, "(truncated)") {
		t.Fatalf("the truncation must say so: %q", out)
	}
	// Valid UTF-8 end to end.
	var r rune
	for _, r = range out {
		if r == '�' {
			t.Fatal("cut in the middle of a rune: invalid UTF-8")
		}
	}
	// Short text is untouched.
	if truncate("abc", 100) != "abc" {
		t.Fatal("a short text must not be modified")
	}
}

func TestMissingFieldsRefused(t *testing.T) {
	var err = Telegram{ChatID: "1"}.Send(context.Background(), Message{})
	if err == nil || !strings.Contains(err.Error(), "token") {
		t.Fatalf("a missing token must be refused: %v", err)
	}
	err = Telegram{Token: "t"}.Send(context.Background(), Message{})
	if err == nil || !strings.Contains(err.Error(), "chat id") {
		t.Fatalf("a missing chat id must be refused: %v", err)
	}
	err = Slack{}.Send(context.Background(), Message{})
	if err == nil {
		t.Fatal("a missing webhook must be refused")
	}
}
