package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Client is an authenticated caller of the meshcraft REST API.
//
// It carries ONE user's API token and nothing else. Per ADR-0011 the token is a
// second way to prove WHO the caller is, never a grant of extra power: every
// `CanSite` and `RequireGlobalAdmin` on the controller applies to these calls
// exactly as it would to that person's browser session. This type therefore has
// no notion of permission, and must never grow one — a second permission model
// living here is precisely the defect ADR-0011 exists to prevent.
type Client struct {
	Base  string
	Token string
	HTTP  *http.Client
}

// NewClient builds a client for `base`, authenticating with `token`.
//
// skipVerify disables certificate verification. It exists for lab controllers
// behind a self-signed certificate and is OFF unless asked for: a token travels
// on every one of these requests, so the default must be the configuration that
// cannot leak it.
func NewClient(base, token string, timeout time.Duration, skipVerify bool) *Client {
	var transport = http.DefaultTransport
	if skipVerify {
		//nolint:gosec // deliberate, opt-in, documented above.
		transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	}
	return &Client{
		Base:  strings.TrimSuffix(base, "/"),
		Token: token,
		HTTP:  &http.Client{Timeout: timeout, Transport: transport},
	}
}

// APIError is a refusal by the controller, carrying the prose it chose.
//
// `Because` is the controller's own sentence, relayed WITHOUT rewording. This
// API writes refusals for a human to read — "this credential holds a DISTINCT
// secret per device", "generation 4 was requested but the active generation is
// 5" — and each one names the repair. Paraphrasing it into "request failed"
// would throw away the only part an agent can act on, which is why the project
// rule shows `because` verbatim.
type APIError struct {
	Status  int
	Method  string
	Path    string
	Because string
}

func (e *APIError) Error() string {
	var b strings.Builder
	b.WriteString(e.Method)
	b.WriteString(" ")
	b.WriteString(e.Path)
	b.WriteString(" — HTTP ")
	b.WriteString(strconv.Itoa(e.Status))
	if e.Because != "" {
		b.WriteString(": ")
		b.WriteString(e.Because)
	}
	// The statuses this API gives a specific meaning. Spelling the meaning out
	// turns a number an agent would have to guess about into the next move.
	var hint = statusHint(e.Status)
	if hint != "" {
		b.WriteString(" (")
		b.WriteString(hint)
		b.WriteString(")")
	}
	return b.String()
}

func statusHint(status int) string {
	switch status {
	case http.StatusUnauthorized:
		return "the token is absent, expired or revoked — it is not a permission problem"
	case http.StatusForbidden:
		return "authenticated, but this identity lacks the role this route requires; " +
			"a read-only token also answers here on a write"
	case http.StatusNotFound:
		return "no such object, or it is outside what this identity may see"
	case http.StatusConflict:
		return "an object with that identity already exists"
	case http.StatusUnprocessableEntity:
		return "the request was understood and REFUSED; the prose above names the repair"
	case http.StatusRequestEntityTooLarge:
		return "send fewer items per call"
	case http.StatusServiceUnavailable:
		return "a dependency is down (database, proxy, or tunnel) — retrying later may work"
	}
	return ""
}

// becauseFrom digs the human sentence out of a failed response body.
//
// Three shapes exist in this product and all three are tried, most specific
// first: the MDP/agent contract's nested envelope ({"error":{"because":…}}),
// a payload-level `because` (the shape conformity verdicts use), and the
// controller's own {"error":"…"} from respondErr. If none parses, the raw body
// is relayed — a body we cannot model is still evidence, and discarding it to
// print "unknown error" would hide the one thing that was actually said.
func becauseFrom(body []byte) string {
	var probe map[string]json.RawMessage
	if json.Unmarshal(body, &probe) == nil {
		var nested, hasErr = probe["error"]
		if hasErr {
			var inner map[string]any
			if json.Unmarshal(nested, &inner) == nil {
				var key string
				for _, key = range []string{"because", "message", "code"} {
					var s, ok = inner[key].(string)
					if ok && s != "" {
						return s
					}
				}
			}
			var plain string
			if json.Unmarshal(nested, &plain) == nil && plain != "" {
				return plain
			}
		}
		var top, hasBecause = probe["because"]
		if hasBecause {
			var s string
			if json.Unmarshal(top, &s) == nil && s != "" {
				return s
			}
		}
	}
	var raw = strings.TrimSpace(string(body))
	if len(raw) > 600 {
		raw = raw[:600] + "…"
	}
	return raw
}

// maxResponse caps what one API answer may bring back.
//
// An agent pays for every byte of this in context, and some of these routes
// (a site's clients, an event log) are unbounded ledgers. Truncating loudly is
// better than silently filling a context window, so the cut is reported.
const maxResponse = 1 << 20

// Do performs one API call and returns the raw body, or an *APIError.
func (c *Client) Do(ctx context.Context, method, path string, body any) ([]byte, error) {
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	var reader io.Reader
	if body != nil {
		var encoded, err = json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(encoded)
	}
	var req, err = http.NewRequestWithContext(ctx, method, c.Base+path, reader)
	if err != nil {
		return nil, err
	}
	// The token travels ONLY in this header, never in a cookie. That is what
	// makes it immune to CSRF (a browser does not send a header on its own) and
	// it is point 4 of ADR-0011.
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	var resp, doErr = c.HTTP.Do(req)
	if doErr != nil {
		return nil, doErr
	}
	defer func() { _ = resp.Body.Close() }()

	var read, readErr = io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if readErr != nil {
		return nil, readErr
	}
	var truncated = len(read) > maxResponse
	if truncated {
		read = read[:maxResponse]
	}
	if resp.StatusCode >= 400 {
		return nil, &APIError{
			Status:  resp.StatusCode,
			Method:  method,
			Path:    path,
			Because: becauseFrom(read),
		}
	}
	if truncated {
		return read, errTruncated
	}
	return read, nil
}

// errTruncated rides alongside a body that was cut at maxResponse. It is not a
// failure: the caller gets the bytes AND the warning, and decides.
var errTruncated = errors.New("response truncated at 1 MiB: narrow the query (a site id, a shorter window, fewer items)")

// GetJSON fetches `path` and returns the decoded body as generic JSON.
func (c *Client) GetJSON(ctx context.Context, path string) (any, error) {
	var body, err = c.Do(ctx, http.MethodGet, path, nil)
	if err != nil && !errors.Is(err, errTruncated) {
		return nil, err
	}
	var out any
	var decodeErr = json.Unmarshal(body, &out)
	if decodeErr != nil {
		// A body that will not parse is the honest answer here: it usually means
		// the path fell through to the SPA, which serves HTML on 200.
		return nil, errors.New("the controller answered " + path +
			" with something that is not JSON — check the path exists " +
			"(an unknown /api path falls through to the web application)")
	}
	return out, err
}
