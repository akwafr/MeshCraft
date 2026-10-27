// Package notify sends one alert to one channel, and nothing else.
//
// ADR-0024, rule 6: delivery is an EFFECT, not a condition. Nothing in this
// package decides whether an alert exists — that is settled in
// internal/api/alerting.go, and the delivery slip is written to the database
// before any function here is called. A channel that does not answer therefore
// cannot block detection; it can only fail to be told.
//
// # Why this does not go through the tunnel
//
// Every other outbound path in the controller reaches a DEVICE, and devices are
// behind proxies: internal/unifi, internal/driver/webfetch and
// internal/discovery/probe all install a custom `DialContext` that hands the
// connection to the tunnel hub. Telegram and Slack are the opposite case —
// third parties on the public internet — so the dialer is left at the OS
// default, which is the same direct egress a site extension in `direct`
// transport already uses (migration 0046).
//
// TLS is VERIFIED here, with no option to skip. The rest of the codebase sets
// InsecureSkipVerify because it talks to switches with self-signed certificates
// and no name that resolves; `api.telegram.org` and `hooks.slack.com` are
// public endpoints with real certificates, and a notification channel is
// exactly where a silent man-in-the-middle would be worth somebody's trouble.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

// Message is what a channel is asked to deliver, in channel-agnostic form.
//
// Deliberately not a formatted string: Telegram and Slack differ in how they
// mark emphasis and how they tolerate length, so the rendering belongs to each
// sender and the caller states FACTS. Keeping it structured is also what lets a
// third channel arrive without touching the queue.
type Message struct {
	// Title is one line: what happened, to what.
	Title string
	// Body carries the detail — the journal message, the transition count, the
	// attribution. May be empty.
	Body string
	// Severity is the journal severity (model.Sev*), used to pick a marker.
	Severity int
	// Site and Device name the subject in words an operator recognises, since a
	// channel has no screen to click through to.
	Site   string
	Device string
	Port   string
}

// Sender delivers a message, or says why it could not.
//
// One method, context-bounded. An implementation must return a non-nil error
// for anything short of acceptance by the far end — a 200 with a body saying
// "false" is a FAILURE (that is literally Telegram's shape), and reporting it
// as success is how a channel comes to look healthy while delivering nothing.
type Sender interface {
	Send(ctx context.Context, m Message) error
	// Kind names the channel type, for log lines and errors.
	Kind() string
}

// httpClient is the shared client for both channels.
//
// DisableKeepAlives because these are third parties contacted a handful of
// times an hour: a pooled connection to Slack held open for days is a
// connection that will be found dead at the one moment it matters. The timeout
// is a backstop only — the caller passes a context with its own deadline.
func httpClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Transport: &http.Transport{DisableKeepAlives: true},
		Timeout:   timeout,
	}
}

// marker prefixes a message with something readable at a glance in a phone
// notification, where there is no colour and no column.
//
// Text, not emoji alone: a channel read through a screen reader, a terminal
// bridge or an email gateway must still carry the severity. The emoji is a
// bonus for the eye, the word is the content.
func marker(sev int) string {
	switch {
	case sev >= 5:
		return "⛔ CRITICAL"
	case sev == 4:
		return "❗ ERROR"
	case sev == 3:
		return "⚠️ WARNING"
	case sev == 2:
		return "✅ OK"
	default:
		return "ℹ️ INFO"
	}
}

// plain renders a message as text both channels can carry.
//
// No markup at all, and that is on purpose: a device name is operator-supplied
// and routinely contains the characters Telegram's MarkdownV2 and Slack's
// mrkdwn treat as syntax. Escaping them per dialect is a bug farm whose failure
// mode is a message silently rejected by the API, i.e. an alert lost to
// formatting. Plain text cannot be malformed.
func plain(m Message) string {
	var b strings.Builder
	b.WriteString(marker(m.Severity))
	if m.Title != "" {
		b.WriteString(" — ")
		b.WriteString(m.Title)
	}
	b.WriteString("\n")
	var where []string
	if m.Site != "" {
		where = append(where, m.Site)
	}
	if m.Device != "" {
		where = append(where, m.Device)
	}
	if m.Port != "" {
		where = append(where, m.Port)
	}
	if len(where) > 0 {
		b.WriteString(strings.Join(where, " · "))
		b.WriteString("\n")
	}
	if m.Body != "" {
		b.WriteString(m.Body)
	}
	return strings.TrimRight(b.String(), "\n")
}

// --- Telegram ---

// Telegram posts through the Bot API.
type Telegram struct {
	Token  string
	ChatID string
	// BaseURL overrides api.telegram.org, for tests only.
	BaseURL string
	Timeout time.Duration
}

func (t Telegram) Kind() string { return "telegram" }

// telegramLimit is the Bot API's hard ceiling on `text` (4096 UTF-16 code
// units). We cut well under it: a truncated alert is still an alert, a rejected
// one is nothing.
const telegramLimit = 3500

func (t Telegram) Send(ctx context.Context, m Message) error {
	if strings.TrimSpace(t.Token) == "" {
		return errors.New("telegram: no bot token configured")
	}
	if strings.TrimSpace(t.ChatID) == "" {
		return errors.New("telegram: no chat id configured")
	}
	var base = t.BaseURL
	if base == "" {
		base = "https://api.telegram.org"
	}
	var payload, err = json.Marshal(map[string]any{
		"chat_id": t.ChatID,
		"text":    truncate(plain(m), telegramLimit),
		// No parse_mode: see `plain`. Unparsed text cannot be rejected for
		// containing an unescaped bracket from a device name.
		"disable_web_page_preview": true,
	})
	if err != nil {
		return err
	}
	// The token is in the PATH of the Bot API, which is why this URL must never
	// be logged. Callers log `Kind()` and the error, never the request.
	var url = strings.TrimRight(base, "/") + "/bot" + t.Token + "/sendMessage"
	var req *http.Request
	req, err = http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	var timeout = t.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	var resp *http.Response
	resp, err = httpClient(timeout).Do(req)
	if err != nil {
		// Scrub the token: a transport error from net/http embeds the URL, and
		// the URL IS the credential. Logging it verbatim would publish the bot
		// token to the application log and to `last_error`, which the API
		// returns to any operator who can read the channel list.
		return errors.New("telegram: " + scrub(err.Error(), t.Token))
	}
	defer resp.Body.Close()
	var body, _ = io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	// Telegram answers 200 with {"ok":false,...} for a wrong chat id, a bot
	// kicked from the group, or a blocked user. Trusting the status code is how
	// a channel reports health while delivering nothing.
	var out struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
		ErrorCode   int    `json:"error_code"`
	}
	_ = json.Unmarshal(body, &out)
	if resp.StatusCode/100 != 2 || !out.OK {
		var why = out.Description
		if why == "" {
			why = strings.TrimSpace(string(body))
		}
		if why == "" {
			why = resp.Status
		}
		return errors.New("telegram: " + scrub(why, t.Token))
	}
	return nil
}

// --- Slack ---

// Slack posts to an incoming webhook.
//
// The URL is the whole credential — anyone holding it can post to the channel —
// which is why it is sealed entire and why `notify_channels.target` stays empty
// for Slack (migration 0048).
type Slack struct {
	WebhookURL string
	Timeout    time.Duration
}

func (s Slack) Kind() string { return "slack" }

// slackLimit: the documented ceiling for a `text` payload is 40000 characters,
// far above anything we send; the cut is here so a pathological journal message
// cannot build a megabyte request.
const slackLimit = 8000

func (s Slack) Send(ctx context.Context, m Message) error {
	var url = strings.TrimSpace(s.WebhookURL)
	if url == "" {
		return errors.New("slack: no webhook URL configured")
	}
	if !strings.HasPrefix(url, "https://") {
		// An http:// webhook would send the alert, and the URL that
		// authenticates it, in clear. Refuse rather than downgrade.
		return errors.New("slack: webhook URL must be https")
	}
	var payload, err = json.Marshal(map[string]any{
		"text": truncate(plain(m), slackLimit),
	})
	if err != nil {
		return err
	}
	var req *http.Request
	req, err = http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	var timeout = s.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	var resp *http.Response
	resp, err = httpClient(timeout).Do(req)
	if err != nil {
		return errors.New("slack: " + scrub(err.Error(), url))
	}
	defer resp.Body.Close()
	var body, _ = io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	var checkErr = checkSlackResponse(resp.StatusCode, resp.Status, string(body))
	if checkErr != nil {
		return errors.New("slack: " + scrub(checkErr.Error(), url))
	}
	return nil
}

// checkSlackResponse judges a webhook answer.
//
// Split out as a pure function so it can be tested: the https guard above
// (correctly) refuses a plaintext URL, which means an httptest server cannot be
// pointed at without either a TLS backdoor in production code or an
// InsecureSkipVerify this package deliberately does not offer. Extracting the
// judgement is the honest way to cover it.
//
// A webhook answers the literal string "ok" on success. Non-2xx carries a
// diagnostic worth surfacing verbatim — "no_service" for a revoked hook,
// "invalid_payload", "channel_not_found" — because each tells the operator a
// different thing to fix.
func checkSlackResponse(code int, status, body string) error {
	if code/100 == 2 {
		return nil
	}
	var why = strings.TrimSpace(body)
	if why == "" {
		why = status
	}
	return errors.New(why)
}

// --- shared helpers ---

// truncate cuts on a rune boundary and says it did.
//
// Cutting mid-rune would produce invalid UTF-8, which both APIs reject — losing
// the alert to a truncation meant to save it.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	var suffix = "\n… (truncated)"
	var keep = max - len(suffix)
	if keep < 0 {
		keep = 0
	}
	var r = []rune(s)
	// len() counted bytes; walk back to a rune boundary under the byte budget.
	var out = make([]rune, 0, len(r))
	var n int
	var c rune
	for _, c = range r {
		if n+len(string(c)) > keep {
			break
		}
		out = append(out, c)
		n += len(string(c))
	}
	return string(out) + suffix
}

// scrub removes a secret from text destined for a log or for `last_error`.
//
// `last_error` is returned by the channel-list route, which is secret-free by
// contract (`has_token`, never the token). A transport error that embeds the
// request URL would drive a coach and horses through that contract, since for
// both channels the URL contains the credential.
func scrub(text, secret string) string {
	var s = strings.TrimSpace(secret)
	if s == "" || len(s) < 8 {
		return text
	}
	return strings.ReplaceAll(text, s, "[redacted]")
}
