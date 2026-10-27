package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"meshcraft/internal/model"
	"meshcraft/internal/store"
)

// Personal API tokens: the second way to prove WHO you are.
//
// Everything this file exists to hold is decided in
// docs/en/06-Decisions/ADR-0011 — A token authenticates, it does not authorise.md.
// The short form:
//
//   - `Authorization: Bearer <token>` puts in the context EXACTLY the same
//     model.User a cookie would. Every CanSite / RequireGlobalAdmin already
//     written applies unchanged, and no business handler learns that tokens
//     exist.
//   - A token can only REDUCE. Its scope (read-only, a list of sites, an
//     expiry) is subtracted from its owner's rights, never substituted for
//     them. A viewer's token does not become an operator by asking.
//   - Never in a cookie. The token travels in the `Authorization` header and
//     nowhere else, which is precisely what makes it immune to CSRF: a browser
//     does not send a header on its own.
//
// THIS FILE AND auth.go ARE THE ONLY PLACES THAT KNOW THERE ARE TWO FORMS OF
// AUTHENTICATION. That is not tidiness, it is the security property: a second
// place that reads tokens is a second place that can disagree about expiry,
// revocation, or scope, and the more permissive one wins.
//
// # The shape of the secret
//
//	mct_<selector:32 hex><sep "_"><verifier:64 hex>
//
// `selector` (16 random bytes) is PUBLIC: stored in clear, uniquely indexed,
// and used to find the row. `verifier` (32 random bytes) is the secret: only
// sha256 of it is stored, and it is compared with crypto/subtle.
//
// Why split, when the session cookie is simply looked up by its hash? Because
// with a single hashed value there is nothing left to compare in constant time
// — locating the row IS the comparison, and it is an index probe. The split
// buys a real constant-time verification and, as a side effect, a token prefix
// that can be shown in a list without showing a secret.

const (
	// TokenPrefix marks a meshcraft API token on sight. Defined once, in
	// `model`, because the store builds the same head for the owner's list.
	TokenPrefix = model.APITokenMark

	selectorBytes = 16
	verifierBytes = 32
)

// ErrBadToken: the presented value is not a well-formed API token. Deliberately
// indistinguishable, to the caller, from "no such token" and from "wrong
// verifier" — all three answer 401 with the same words.
var ErrBadToken = errors.New("malformed api token")

// NewAPIToken mints one token and returns the three things that must go three
// different ways: the clear value to the owner (once), the selector and the
// verifier hash to the database.
//
// The clear value is never returned again, never logged, and never stored.
func NewAPIToken() (clear, selector string, verifierHash []byte, err error) {
	var raw = make([]byte, selectorBytes+verifierBytes)
	_, err = rand.Read(raw)
	if err != nil {
		return "", "", nil, err
	}
	selector = hex.EncodeToString(raw[:selectorBytes])
	var verifier = hex.EncodeToString(raw[selectorBytes:])
	clear = TokenPrefix + selector + "_" + verifier
	return clear, selector, HashToken(verifier), nil
}

// ParseAPIToken splits a presented value into its public and secret halves.
//
// Strict on purpose: a value that is not exactly the minted shape is refused
// here rather than sent to the database as a lookup key.
func ParseAPIToken(clear string) (selector, verifier string, err error) {
	var rest, ok = strings.CutPrefix(strings.TrimSpace(clear), TokenPrefix)
	if !ok {
		return "", "", ErrBadToken
	}
	var sel, ver string
	sel, ver, ok = strings.Cut(rest, "_")
	if !ok || len(sel) != selectorBytes*2 || len(ver) != verifierBytes*2 {
		return "", "", ErrBadToken
	}
	if !isHex(sel) || !isHex(ver) {
		return "", "", ErrBadToken
	}
	return sel, ver, nil
}

func isHex(s string) bool {
	var i int
	for i = 0; i < len(s); i++ {
		var c = s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return len(s) > 0
}

// --- The scope, and where it is applied ---

// TokenScope is the optional REDUCTION an API token carries. Zero value means
// "an unrestricted token": the holder gets their owner's rights, no more.
//
// It never grants. There is no field here that could: read-only subtracts
// writes, SiteIDs subtracts sites, and expiry subtracts time.
type TokenScope struct {
	// ReadOnly refuses every unsafe HTTP method, and caps the effective role at
	// `viewer` for anything that asks CanSite. Two gates on purpose — see
	// permits().
	ReadOnly bool
	// SiteIDs, when non-empty, is the only set of sites this token reaches.
	// Empty means "no site restriction", NOT "no site".
	SiteIDs []string
	// AND NOTHING ELSE. In particular, not the token's id: a permission that
	// depended on WHICH token was presented would be the second permission
	// model this whole design exists to avoid. What the scope carries is what
	// it may subtract, and no more.
}

type scopeCtxKey int

const scopeKey scopeCtxKey = 0

// TokenScopeFrom returns the scope of the API token that authenticated the
// request, or NIL when a session cookie did.
//
// NIL MEANS "NO TOKEN", NOT "NO RESTRICTION". The distinction is load-bearing
// twice: a nil scope reduces nothing (a cookie session is the full rights of
// its user), and non-nil is the only reliable signal that the caller is holding
// a standing credential — which is what CookieOnly keys on.
func TokenScopeFrom(ctx context.Context) *TokenScope {
	var sc, _ = ctx.Value(scopeKey).(*TokenScope)
	return sc
}

func withTokenScope(ctx context.Context, sc *TokenScope) context.Context {
	return context.WithValue(ctx, scopeKey, sc)
}

// permits is the whole authorization contribution of a token, and it is PURE so
// that it can be proven exhaustively without a database.
//
// Read as "does the scope still allow asking for minRole on siteID?". A nil
// scope always does. The answer is ANDed with the user's real rights by the
// caller — it never replaces them.
func (sc *TokenScope) permits(siteID, minRole string) bool {
	if sc == nil {
		return true
	}
	if !sc.allowsRole(minRole) {
		return false
	}
	if len(sc.SiteIDs) == 0 {
		return true
	}
	var id string
	for _, id = range sc.SiteIDs {
		// Case-insensitive: the stored ids are canonical lowercase uuids, the
		// ones in a URL are whatever the caller typed.
		if strings.EqualFold(id, siteID) {
			return true
		}
	}
	return false
}

// allowsRole reports whether the scope still permits ASKING for minRole, with
// no site involved.
//
// A read-only token caps the effective role at `viewer`. This is the SECOND
// gate for read-only — the first refuses unsafe HTTP methods at the edge — and
// it is not redundant: a route that writes on a GET (there should be none, but
// "should" is not a mechanism) is covered only here, and a route that reads on
// a POST is covered only there.
func (sc *TokenScope) allowsRole(minRole string) bool {
	if sc == nil || !sc.ReadOnly {
		return true
	}
	return model.RoleLevel[minRole] <= model.RoleLevel["viewer"]
}

// scopeSites is the scope's site list, nil-safe.
func (sc *TokenScope) scopeSites() []string {
	if sc == nil {
		return nil
	}
	return sc.SiteIDs
}

// keep intersects a list of site ids with the scope, preserving the caller's
// order. Pure, for the same reason as permits.
func (sc *TokenScope) keep(ids []string) []string {
	if sc == nil || len(sc.SiteIDs) == 0 {
		return ids
	}
	var out = []string{}
	var id string
	for _, id = range ids {
		if sc.permits(id, "viewer") {
			out = append(out, id)
		}
	}
	return out
}

// --- Judging a presented token ---

// tokenVerdict is what the middleware does with a presented token. Status 0
// means accepted.
type tokenVerdict struct {
	Status int
	Reason string
}

// badToken: the one answer every "this token is not usable as an identity"
// gives. Shared so that an unknown selector, a malformed value, and a wrong
// verifier cannot be told apart by the words or the code.
var badToken = tokenVerdict{http.StatusUnauthorized, "invalid token"}

// zeroHash: something to compare against when there is no row, so that an
// unknown selector does not answer measurably faster than a known one with a
// wrong verifier. Same reasoning as socksAuth.Authenticate.
var zeroHash = make([]byte, 32)

// judgeAPIToken decides, from the stored record alone, whether a presented
// token authenticates this request. PURE: no clock of its own, no database, no
// http.Request — which is what makes the four refusals testable one by one.
//
// THE ORDER OF THE CHECKS IS PART OF THE DESIGN. The verifier is compared
// FIRST, before any state of the row is consulted, so that somebody who guessed
// a selector but not the secret learns nothing about whether that token is
// expired, revoked, or read-only. After that the holder is, by definition, the
// legitimate holder, and telling them exactly why their token stopped working
// is a kindness rather than a leak.
func judgeAPIToken(rec model.APITokenRecord, verifier, method string, now time.Time) tokenVerdict {
	if subtle.ConstantTimeCompare(HashToken(verifier), rec.VerifierHash) != 1 {
		return badToken
	}
	if rec.Token.RevokedAt != nil {
		return tokenVerdict{http.StatusUnauthorized, "token revoked"}
	}
	if rec.Token.ExpiresAt != nil && !now.Before(*rec.Token.ExpiresAt) {
		return tokenVerdict{http.StatusUnauthorized, "token expired"}
	}
	// 403 and not 401: the token IS a valid identity, it is the gesture that is
	// out of its scope. Answering 401 would send a client into a re-login loop
	// for a request that will never be allowed.
	if rec.Token.ReadOnly && !isSafeMethod(method) {
		return tokenVerdict{http.StatusForbidden,
			"read-only token: this token may not " + method}
	}
	return tokenVerdict{}
}

// isSafeMethod: the methods that do not change state. OPTIONS is included
// because a preflight must not be answered 403 by a read-only token.
func isSafeMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	return false
}

// bearerToken extracts the value of `Authorization: Bearer <token>`.
//
// ANOTHER SCHEME RETURNS "" rather than an error: a `Basic` header aimed at
// something else must not turn a perfectly good cookie session into a 401. An
// empty result means "no token was presented here", and the cookie path runs.
func bearerToken(r *http.Request) string {
	var h = r.Header.Get("Authorization")
	if h == "" {
		return ""
	}
	var scheme, rest, found = strings.Cut(h, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return strings.TrimSpace(rest)
}

// authenticateToken resolves a presented token to its owner and scope.
//
// The last_used_at write happens HERE, on every accepted call, and its failure
// is swallowed: bookkeeping must not break an API call. It is also the reason
// this function is not pure — the judgement it delegates to judgeAPIToken is.
func (a *Service) authenticateToken(ctx context.Context, clear, method string) (
	model.User, *TokenScope, tokenVerdict) {
	var selector, verifier, err = ParseAPIToken(clear)
	if err != nil {
		return model.User{}, nil, badToken
	}
	var rec model.APITokenRecord
	rec, err = a.idents().APITokenBySelector(ctx, selector)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			slog.Error("api token lookup failed", "err", err.Error())
			return model.User{}, nil, tokenVerdict{
				http.StatusInternalServerError, "internal error"}
		}
		// Spend the comparison anyway, against a value that cannot match, so an
		// unknown selector does not answer faster than a known one.
		_ = subtle.ConstantTimeCompare(HashToken(verifier), zeroHash)
		return model.User{}, nil, badToken
	}
	var v = judgeAPIToken(rec, verifier, method, time.Now())
	if v.Status != 0 {
		return model.User{}, nil, v
	}
	a.idents().TouchAPIToken(ctx, rec.Token.ID)
	return rec.User, &TokenScope{
		ReadOnly: rec.Token.ReadOnly,
		SiteIDs:  rec.Token.SiteIDs,
	}, tokenVerdict{}
}

// CookieOnly refuses a request authenticated by an API token.
//
// It guards exactly one family of routes: the ones that MINT OR REVOKE A
// STANDING CREDENTIAL (API tokens, the SOCKS route token). A token that can
// mint another token extends its own life past its own expiry and past its own
// revocation, which quietly undoes points 2 and 3 of ADR-0011 — so that gesture
// requires the password-backed session, and the reading of those same routes
// stays open to tokens because a list carries no secret.
//
// Mounted INSIDE the session group: by the time this runs, Middleware has
// already said who the caller is. 403 and not 401, for the reason given in
// judgeAPIToken: re-authenticating would not help.
func CookieOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if TokenScopeFrom(r.Context()) != nil {
			http.Error(w,
				`{"error":"this gesture requires a signed-in session, not an API token"}`,
				http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
