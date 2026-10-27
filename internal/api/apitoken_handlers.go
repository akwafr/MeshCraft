package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"meshcraft/internal/auth"
	"meshcraft/internal/model"
)

// PERSONAL API TOKENS — THE CONTRACT OF THE THREE ROUTES, IN FULL.
//
// This header is the reference two other screens are built against (the profile
// panel and the documentation page). It is written out here rather than left to
// be read out of the handlers, because an API shape guessed from an
// implementation is an API shape that changes when the implementation does.
//
// Decided by docs/en/06-Decisions/ADR-0011 — A token authenticates, it does
// not authorise.md. One sentence of it governs everything below: A TOKEN IS A
// SECOND WAY TO PROVE WHO YOU ARE, AND IT NEVER CHANGES WHAT YOU MAY DO.
//
// # How a token is used (what the other screens must document)
//
//	Authorization: Bearer mct_<32 hex>_<64 hex>
//
// On EVERY route of this API — there is no separate "API for agents". If a page
// can do something, a token can, because it is the same handler. The token must
// NEVER be put in a cookie: it is refused there, deliberately, and the header is
// what makes it immune to CSRF.
//
// # GET /api/auth/tokens
//
// The caller's OWN active tokens, newest first. 200 with a (possibly empty)
// array of:
//
//	{
//	  "id":           "uuid",
//	  "name":         "laptop CLI",
//	  "prefix":       "mct_1a2b3c4d",   // visible head, NOT a credential
//	  "read_only":    true,
//	  "site_ids":     ["uuid", …],      // [] means "no site restriction"
//	  "created_at":   "RFC3339",
//	  "expires_at":   "RFC3339" | null, // null means "never expires"
//	  "last_used_at": "RFC3339" | null  // null means "never presented"
//	}
//
// THE CLEAR VALUE IS NOT IN THIS RESPONSE, AND THERE IS NO ROUTE THAT RETURNS
// IT. It was shown once, at creation, and the server kept only a hash. Nobody —
// not the owner, not a global admin, not a database dump — can read it back.
//
// NOBODY SEES ANOTHER PERSON'S TOKENS. Not even a global admin: a token is a
// secret in one person's hands, not an administrable resource. An administrator
// who must cut somebody off removes their roles or their account, which closes
// every one of their tokens in the same second — rights are read at call time.
//
// # POST /api/auth/tokens
//
// Request (every field optional except `name`):
//
//	{
//	  "name":            "laptop CLI",   // required, 1..80 chars after trim
//	  "read_only":       true,           // DEFAULT true
//	  "site_ids":        ["uuid", …],    // DEFAULT [] = no site restriction
//	  "expires_in_days": 90              // DEFAULT 90; 0 = never; max 3650
//	}
//
// 201 with the list shape above PLUS one field that never appears again:
//
//	{ …, "token": "mct_…" }
//
// The defaults are the LEAST powerful option, not the most convenient one: a
// token is as dangerous as its holder's password minus whatever scope it gave
// itself, so read-only and an expiry are what you get by not choosing.
//
// The scope can only REDUCE. Asking for `read_only: false` does not make a
// viewer an operator; listing sites the caller has no role on grants nothing.
//
// Errors: 400 `name is required` / `name is too long` / `expires_in_days` out of
// range / `site_ids` not uuid-shaped; 409 when the caller already holds
// maxTokensPerUser active tokens; 403 when the caller is authenticated BY A
// TOKEN (see below).
//
// # DELETE /api/auth/tokens/{tokenID}
//
// Revokes one of the caller's own tokens. 204 on success, 404 when the id is
// unknown OR belongs to somebody else — indistinguishable on purpose, so the
// route cannot be used to probe who holds which token. Revocation is immediate:
// the next call with that token answers 401 `token revoked`.
//
// # The error codes a client must handle
//
//	401 {"error":"invalid token"}    malformed, unknown, or wrong secret —
//	                                 the three are deliberately identical
//	401 {"error":"token revoked"}
//	401 {"error":"token expired"}
//	403 {"error":"read-only token: this token may not POST"}
//	403 {"error":"reserved for global administrators"}      (unchanged)
//	403 {"error":"this token is restricted to a list of sites: …"}
//	403 anything a cookie session would also have been refused
//
// A 401 means "get a new credential"; a 403 means "this will never work with
// this credential". A client that retries a 403 by re-authenticating loops
// forever, which is why read-only refusals are not 401.
//
// # Why these three routes refuse a token and demand a session
//
// CREATE AND REVOKE ARE GUARDED BY auth.CookieOnly. A token that can mint
// another token outlives its own expiry and its own revocation, which quietly
// undoes two of the five rules of the ADR. Minting therefore requires the
// password-backed session. Reading the list is left open to tokens: it carries
// no secret, and an agent listing its own credentials is harmless.

// maxTokensPerUser caps active tokens per person.
//
// Not a security boundary — a cap of 20 stops nothing a cap of 1 would not —
// but a hygiene one: the list is the revocation tool, and a list nobody reads
// is a list nobody acts on. Picked to be far above any honest use.
const maxTokensPerUser = 20

// maxTokenExpiryDays ceilings `expires_in_days` at ten years. A value beyond it
// is a typo, and `never` is already available by asking for 0 explicitly.
const maxTokenExpiryDays = 3650

// defaultTokenExpiryDays is what a caller who says nothing gets.
const defaultTokenExpiryDays = 90

const maxTokenNameLen = 80

func (s *Server) handleListAPITokens(w http.ResponseWriter, r *http.Request) {
	var me = auth.UserFrom(r.Context())
	var toks, err = s.Store.ListAPITokens(r.Context(), me.ID)
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, http.StatusOK, toks)
}

// handleCreateAPIToken mints a token and returns the clear value ONCE.
//
// For the CALLER'S OWN account only, and there is no parameter that could say
// otherwise. An administrator minting a token for somebody else would break the
// one property that makes the audit line worth keeping: that a call names the
// person who made it.
func (s *Server) handleCreateAPIToken(w http.ResponseWriter, r *http.Request) {
	var me = auth.UserFrom(r.Context())
	var in struct {
		Name string `json:"name"`
		// Pointers, both of them, because ABSENT and FALSE must not be the same
		// thing: a client that omits read_only gets the safe default, and one
		// that sends `false` has said so explicitly. With a bare bool the safe
		// default would be unreachable.
		ReadOnly      *bool    `json:"read_only"`
		SiteIDs       []string `json:"site_ids"`
		ExpiresInDays *int     `json:"expires_in_days"`
	}
	if !decodeOptional(w, r, &in) {
		return
	}

	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		respondErr(w, http.StatusBadRequest,
			"name is required: a token you cannot name is a token you dare not revoke")
		return
	}
	if len(in.Name) > maxTokenNameLen {
		respondErr(w, http.StatusBadRequest, "name is too long")
		return
	}

	var readOnly = true
	if in.ReadOnly != nil {
		readOnly = *in.ReadOnly
	}

	var sites, sitesOK = normalizeSiteIDs(in.SiteIDs)
	if !sitesOK {
		respondErr(w, http.StatusBadRequest,
			"site_ids must be site identifiers (uuid)")
		return
	}

	var days = defaultTokenExpiryDays
	if in.ExpiresInDays != nil {
		days = *in.ExpiresInDays
	}
	if days < 0 || days > maxTokenExpiryDays {
		respondErr(w, http.StatusBadRequest,
			"expires_in_days must be between 0 (never) and 3650")
		return
	}
	var expiresAt *time.Time
	if days > 0 {
		var at = time.Now().Add(time.Duration(days) * 24 * time.Hour)
		expiresAt = &at
	}

	var n, countErr = s.Store.CountAPITokens(r.Context(), me.ID)
	if countErr != nil {
		fail(w, countErr)
		return
	}
	if n >= maxTokensPerUser {
		respondErr(w, http.StatusConflict,
			"you already hold the maximum number of API tokens: revoke one first")
		return
	}

	var clear, selector, hash, mintErr = auth.NewAPIToken()
	if mintErr != nil {
		fail(w, mintErr)
		return
	}
	var tok model.APIToken
	tok, mintErr = s.Store.CreateAPIToken(r.Context(), me.ID, in.Name, selector,
		hash, readOnly, sites, expiresAt)
	if mintErr != nil {
		fail(w, mintErr)
		return
	}

	// The audit line names the token by its PREFIX, never by its value. An
	// event body is read by everyone who can read the fleet log, and a secret
	// that reaches a log is a secret that has been published.
	var scope = "read-only"
	if !readOnly {
		scope = "read-write"
	}
	if len(sites) > 0 {
		scope += ", restricted to " + strconv.Itoa(len(sites)) + " site(s)"
	}
	s.emitInstance(r, "apitoken.created", model.SevWarning,
		"API token "+tok.Prefix+" ("+scope+") created by "+me.Email)

	respond(w, http.StatusCreated, struct {
		model.APIToken
		// Token: the ONLY moment this value exists outside the caller's hand.
		Token string `json:"token"`
	}{tok, clear})
}

// handleRevokeAPIToken closes one of the caller's own tokens.
func (s *Server) handleRevokeAPIToken(w http.ResponseWriter, r *http.Request) {
	var me = auth.UserFrom(r.Context())
	var id = chi.URLParam(r, "tokenID")
	var err = s.Store.RevokeAPIToken(r.Context(), me.ID, id)
	if err != nil {
		// store.ErrNotFound covers "no such token" AND "not yours": fail()
		// turns both into 404, which is the answer that reveals nothing.
		fail(w, err)
		return
	}
	s.emitInstance(r, "apitoken.revoked", model.SevNotice,
		"API token revoked by "+me.Email)
	respond(w, http.StatusNoContent, nil)
}

// normalizeSiteIDs lowercases and shape-checks the scope's site list.
//
// Validated HERE rather than left to PostgreSQL so a typo answers 400 with a
// sentence instead of 500 with a driver error — and lowercased because the
// comparison in auth is against the canonical form a uuid column prints.
//
// The check is shape only, not existence: the list can only ever SUBTRACT, so
// an id naming no site simply grants nothing. Verifying existence would add a
// query per creation to protect against a mistake that is already harmless.
func normalizeSiteIDs(in []string) ([]string, bool) {
	var out = []string{}
	var raw string
	for _, raw = range in {
		var id = strings.ToLower(strings.TrimSpace(raw))
		if id == "" {
			continue
		}
		if !looksLikeUUID(id) {
			return nil, false
		}
		out = append(out, id)
	}
	return out, true
}

// looksLikeUUID: 8-4-4-4-12 hex. Deliberately not a uuid parse — the only thing
// that matters is that the value cannot be a surprise to the database.
func looksLikeUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	var groups = []int{8, 13, 18, 23}
	var i int
	for _, i = range groups {
		if s[i] != '-' {
			return false
		}
	}
	for i = 0; i < len(s); i++ {
		if s[i] == '-' {
			continue
		}
		var c = s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
