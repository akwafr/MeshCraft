package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"meshcraft/internal/model"
	"meshcraft/internal/store"
)

// API tokens: the refusals, one by one.
//
// ADR-0011 says it in as many words — "one more authentication surface to
// audit. The middleware must be the only place that knows both forms, and a
// test must check that an expired, revoked, or read-only token on a write
// fails — not merely that a valid token passes."
// This file is that test, and it is deliberately written as a list of ways in
// which a token must NOT work.
//
// NO DATABASE, like the rest of this repository's suite. internal/auth reaches
// the store through the narrow auth.Identities interface precisely so that both
// forms of credential can be exercised end to end — real middleware, real
// chi-less http chain, real CanSite — against the fake below. A security test
// that needs a PostgreSQL to run is a security test that does not run.
//
// What these tests CANNOT cover, said plainly so the list is not mistaken for
// end-to-end proof:
//
//   - THE SQL. That api_tokens.site_ids comes back as a []string, that
//     RevokeAPIToken's `and user_id = $2` actually scopes the update, that
//     `gen_random_uuid()` is available on the deployment — all of that is read
//     from internal/store/apitokens.go and migration 0045 and verified by
//     running the controller, not here.
//   - THE HTTP SHAPE OF THE THREE CRUD ROUTES. internal/api cannot be
//     instantiated without a *store.Store, so the list route's body is proven
//     here at the level of the TYPE it serialises (cf.
//     TestTheOwnerListCarriesNoSecret), which is where the guarantee actually
//     lives: model.APIToken has no field a secret could land in.
//   - TIMING, measured. See TestTheVerifierIsComparedInConstantTime for why a
//     stopwatch would be a flaky test and what is checked instead.

// --- The fake identity source ---

// fakeIdents is internal/store, reduced to the six questions auth asks it.
type fakeIdents struct {
	sessions map[string]model.User           // sha256 of the cookie → user
	tokens   map[string]model.APITokenRecord // selector → record
	users    map[string]model.User           // user id → user
	roles    map[string]map[string]string    // user id → site id → role
	touched  []string                        // token ids handed to TouchAPIToken
}

func newFake() *fakeIdents {
	return &fakeIdents{
		sessions: map[string]model.User{},
		tokens:   map[string]model.APITokenRecord{},
		users:    map[string]model.User{},
		roles:    map[string]map[string]string{},
	}
}

func (f *fakeIdents) GetSessionUser(ctx context.Context, hash []byte) (model.User, error) {
	var u, ok = f.sessions[string(hash)]
	if !ok {
		return model.User{}, store.ErrNotFound
	}
	return u, nil
}

func (f *fakeIdents) GetUser(ctx context.Context, id string) (model.User, error) {
	var u, ok = f.users[id]
	if !ok {
		return model.User{}, store.ErrNotFound
	}
	return u, nil
}

func (f *fakeIdents) GetSiteRole(ctx context.Context, userID, siteID string) (string, error) {
	return f.roles[userID][siteID], nil
}

func (f *fakeIdents) ListUserRoles(ctx context.Context, userID string) ([]model.Role, error) {
	var out = []model.Role{}
	var siteID, role string
	for siteID, role = range f.roles[userID] {
		out = append(out, model.Role{SiteID: siteID, Role: role})
	}
	return out, nil
}

func (f *fakeIdents) APITokenBySelector(ctx context.Context, selector string) (model.APITokenRecord, error) {
	var rec, ok = f.tokens[selector]
	if !ok {
		return model.APITokenRecord{}, store.ErrNotFound
	}
	return rec, nil
}

func (f *fakeIdents) TouchAPIToken(ctx context.Context, tokenID string) {
	f.touched = append(f.touched, tokenID)
}

// user registers a person with their roles, and returns them.
func (f *fakeIdents) user(id, email string, globalAdmin bool, roles map[string]string) model.User {
	var u = model.User{ID: id, Email: email, IsGlobalAdmin: globalAdmin}
	f.users[id] = u
	f.roles[id] = roles
	return u
}

// mint registers one token and hands back the clear value, exactly as the
// creation route does: the shape passed in is the scope, the secret is real.
func (f *fakeIdents) mint(t *testing.T, owner model.User, shape model.APIToken) string {
	t.Helper()
	var clear, selector, hash, err = NewAPIToken()
	if err != nil {
		t.Fatalf("minting a token: %v", err)
	}
	shape.ID = "tok-" + selector[:6]
	shape.Prefix = model.APITokenPrefix(selector)
	if shape.SiteIDs == nil {
		shape.SiteIDs = []string{}
	}
	f.tokens[selector] = model.APITokenRecord{
		Token: shape, User: owner, VerifierHash: hash,
	}
	return clear
}

// session registers a cookie session and returns its clear token.
func (f *fakeIdents) session(t *testing.T, u model.User) string {
	t.Helper()
	var token, hash, err = NewSessionToken()
	if err != nil {
		t.Fatalf("minting a session: %v", err)
	}
	f.sessions[string(hash)] = u
	return token
}

// --- The chain under test ---

// probe is the shape of every site-scoped route in this product: the
// middleware, then one CanSite, then the work. Nothing here knows that tokens
// exist, which is the property being tested as much as the refusals are.
func probe(svc *Service, siteID, minRole string) http.Handler {
	return svc.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !svc.CanSite(r.Context(), siteID, minRole) {
			http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
}

// callWithToken runs one request bearing a token.
func callWithToken(h http.Handler, method, token string) *httptest.ResponseRecorder {
	var r = httptest.NewRequest(method, "/api/sites/site-a/devices", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	var w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func body(w *httptest.ResponseRecorder) string {
	return strings.TrimSpace(w.Body.String())
}

// --- 1. A token that no longer exists in time, or at all ---

func TestAnExpiredTokenIsRefused(t *testing.T) {
	var f = newFake()
	var u = f.user("u1", "ing@example.com", false, map[string]string{"site-a": "site_admin"})
	var svc = &Service{Lookup: f}

	var yesterday = time.Now().Add(-24 * time.Hour)
	var tok = f.mint(t, u, model.APIToken{ExpiresAt: &yesterday})

	var w = callWithToken(probe(svc, "site-a", "viewer"), http.MethodGet, tok)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("an expired token answered %d, want 401 (%s)", w.Code, body(w))
	}
	if !strings.Contains(body(w), "expired") {
		t.Errorf("the holder of an expired token is told why: got %q", body(w))
	}
	// And it never reached the bookkeeping write: a refused token has not been
	// "used".
	if len(f.touched) != 0 {
		t.Errorf("last_used_at was touched for a refused token: %v", f.touched)
	}
}

// A token that expires at this very instant is OUT, not in. The boundary is
// written as `!now.Before(expiry)` for exactly this reason, and a test is the
// only thing that keeps it from drifting back to `After`.
func TestATokenExpiringNowIsAlreadyOut(t *testing.T) {
	var f = newFake()
	var u = f.user("u1", "ing@example.com", false, map[string]string{"site-a": "viewer"})
	var now = time.Now()
	var rec = model.APITokenRecord{
		Token:        model.APIToken{ExpiresAt: &now},
		User:         u,
		VerifierHash: HashToken("v"),
	}
	var v = judgeAPIToken(rec, "v", http.MethodGet, now)
	if v.Status != http.StatusUnauthorized {
		t.Fatalf("a token expiring at `now` answered %d, want 401", v.Status)
	}
}

func TestARevokedTokenIsRefused(t *testing.T) {
	var f = newFake()
	var u = f.user("u1", "ing@example.com", false, map[string]string{"site-a": "site_admin"})
	var svc = &Service{Lookup: f}

	var when = time.Now().Add(-time.Minute)
	var tok = f.mint(t, u, model.APIToken{RevokedAt: &when})

	var w = callWithToken(probe(svc, "site-a", "viewer"), http.MethodGet, tok)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("a revoked token answered %d, want 401 (%s)", w.Code, body(w))
	}
	if !strings.Contains(body(w), "revoked") {
		t.Errorf("the holder of a revoked token is told why: got %q", body(w))
	}
}

// Revocation beats a still-valid expiry, and it is checked first. A row that is
// both revoked and expired must not answer "expired" — the two are revoked by
// different gestures and an operator reading the error needs to know which one
// closed their token.
func TestRevocationIsAnnouncedBeforeExpiry(t *testing.T) {
	var past = time.Now().Add(-time.Hour)
	var rec = model.APITokenRecord{
		Token:        model.APIToken{RevokedAt: &past, ExpiresAt: &past},
		VerifierHash: HashToken("v"),
	}
	var v = judgeAPIToken(rec, "v", http.MethodGet, time.Now())
	if !strings.Contains(v.Reason, "revoked") {
		t.Errorf("a revoked AND expired token answered %q, want the revocation", v.Reason)
	}
}

// --- 2. Read-only ---

func TestAReadOnlyTokenRefusesAWriteAndAllowsTheRead(t *testing.T) {
	var f = newFake()
	// A site_admin: all the rights in the world on this site. What stops the
	// write below is the token's scope, nothing else.
	var u = f.user("u1", "ing@example.com", false, map[string]string{"site-a": "site_admin"})
	var svc = &Service{Lookup: f}
	var tok = f.mint(t, u, model.APIToken{ReadOnly: true})

	var w = callWithToken(probe(svc, "site-a", "viewer"), http.MethodGet, tok)
	if w.Code != http.StatusOK {
		t.Fatalf("a read-only token could not READ: %d (%s)", w.Code, body(w))
	}

	var methods = []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete}
	var m string
	for _, m = range methods {
		w = callWithToken(probe(svc, "site-a", "operator"), m, tok)
		if w.Code != http.StatusForbidden {
			t.Errorf("a read-only token could %s: %d (%s)", m, w.Code, body(w))
		}
	}
}

// THE EDGE GATE ON ITS OWN. Read-only is enforced twice — the method here, the
// effective role in CanSite — and the HTTP tests above cannot tell which of the
// two refused a POST. This one can: it asks the judge directly, so deleting the
// method check fails a test instead of being silently covered by the other gate.
func TestTheEdgeRefusesAnUnsafeMethodForAReadOnlyTokenByItself(t *testing.T) {
	var rec = model.APITokenRecord{
		Token:        model.APIToken{ReadOnly: true},
		VerifierHash: HashToken("v"),
	}
	var now = time.Now()

	var safe = []string{http.MethodGet, http.MethodHead, http.MethodOptions}
	var m string
	for _, m = range safe {
		var v = judgeAPIToken(rec, "v", m, now)
		if v.Status != 0 {
			t.Errorf("a read-only token was refused %s at the edge: %d %q",
				m, v.Status, v.Reason)
		}
	}
	var unsafe = []string{http.MethodPost, http.MethodPut, http.MethodPatch,
		http.MethodDelete}
	for _, m = range unsafe {
		var v = judgeAPIToken(rec, "v", m, now)
		if v.Status != http.StatusForbidden {
			t.Errorf("a read-only token was allowed %s at the edge: %d %q",
				m, v.Status, v.Reason)
		}
		if !strings.Contains(v.Reason, m) {
			t.Errorf("the refusal for %s does not name the method: %q", m, v.Reason)
		}
	}

	// A read-WRITE token passes every one of them: the gate must key on the
	// scope, not on the method alone.
	rec.Token.ReadOnly = false
	for _, m = range unsafe {
		var v = judgeAPIToken(rec, "v", m, now)
		if v.Status != 0 {
			t.Errorf("a read-write token was refused %s: %d %q", m, v.Status, v.Reason)
		}
	}
}

// The second gate: even on a SAFE method, a read-only token cannot obtain a role
// above `viewer`. This is what covers a route that writes on a GET — there
// should be none, but "should" is not a mechanism.
func TestAReadOnlyTokenCannotObtainAnOperatorRoleOnAGet(t *testing.T) {
	var f = newFake()
	var u = f.user("u1", "ing@example.com", false, map[string]string{"site-a": "site_admin"})
	var svc = &Service{Lookup: f}
	var tok = f.mint(t, u, model.APIToken{ReadOnly: true})

	var w = callWithToken(probe(svc, "site-a", "operator"), http.MethodGet, tok)
	if w.Code != http.StatusForbidden {
		t.Fatalf("a read-only token reached `operator` on a GET: %d (%s)", w.Code, body(w))
	}
}

// --- 3. Site scope ---

func TestASiteScopedTokenCannotReachAnotherSite(t *testing.T) {
	var f = newFake()
	var u = f.user("u1", "ing@example.com", false, map[string]string{
		"site-a": "site_admin", "site-b": "site_admin",
	})
	var svc = &Service{Lookup: f}
	var tok = f.mint(t, u, model.APIToken{SiteIDs: []string{"site-a"}})

	var w = callWithToken(probe(svc, "site-a", "viewer"), http.MethodGet, tok)
	if w.Code != http.StatusOK {
		t.Fatalf("the token could not reach its OWN site: %d (%s)", w.Code, body(w))
	}
	w = callWithToken(probe(svc, "site-b", "viewer"), http.MethodGet, tok)
	if w.Code != http.StatusForbidden {
		t.Fatalf("a token scoped to site-a reached site-b: %d (%s)", w.Code, body(w))
	}
}

// THE ONE ORDERING MISTAKE THAT WOULD OPEN THE WHOLE INSTANCE. CanSite answers
// true for a global admin whatever site it is asked about; if the scope were
// consulted after that shortcut instead of before it, a global admin's
// site-scoped token would reach every site of every customer.
func TestAGlobalAdminsSiteScopedTokenStaysInsideItsSites(t *testing.T) {
	var f = newFake()
	var admin = f.user("u0", "admin@example.com", true, nil)
	var svc = &Service{Lookup: f}
	var tok = f.mint(t, admin, model.APIToken{SiteIDs: []string{"site-a"}})

	var w = callWithToken(probe(svc, "site-a", "viewer"), http.MethodGet, tok)
	if w.Code != http.StatusOK {
		t.Fatalf("the admin token could not reach its own site: %d", w.Code)
	}
	w = callWithToken(probe(svc, "site-b", "viewer"), http.MethodGet, tok)
	if w.Code != http.StatusForbidden {
		t.Fatalf("a global admin's site-scoped token reached site-b: %d (%s)",
			w.Code, body(w))
	}
}

// Site scoping also narrows the LISTS, including the `all` shortcut a global
// admin gets. Without this, the first screen that enumerates sites would hand
// back everything the scope had just excluded.
func TestSiteIDsWithRoleIsIntersectedWithTheScope(t *testing.T) {
	var f = newFake()
	var admin = f.user("u0", "admin@example.com", true, nil)
	var ing = f.user("u1", "ing@example.com", false, map[string]string{
		"site-a": "site_admin", "site-b": "operator",
	})
	var svc = &Service{Lookup: f}

	// A cookie session: unchanged behaviour, `all` for the admin.
	var ctx = context.WithValue(context.Background(), userKey, admin)
	var ids, all, err = svc.SiteIDsWithRole(ctx, "viewer")
	if err != nil || !all || ids != nil {
		t.Fatalf("a cookie admin lost its `all` shortcut: ids=%v all=%v err=%v", ids, all, err)
	}

	// The same admin through a site-scoped token: a list, and NOT `all`.
	ctx = withTokenScope(ctx, &TokenScope{SiteIDs: []string{"site-a"}})
	ids, all, err = svc.SiteIDsWithRole(ctx, "viewer")
	if err != nil || all || len(ids) != 1 || ids[0] != "site-a" {
		t.Fatalf("a scoped admin got ids=%v all=%v err=%v, want [site-a] false", ids, all, err)
	}

	// A plain user: the intersection of their roles and the scope.
	ctx = withTokenScope(context.WithValue(context.Background(), userKey, ing),
		&TokenScope{SiteIDs: []string{"site-b", "site-z"}})
	ids, all, err = svc.SiteIDsWithRole(ctx, "operator")
	if err != nil || all || len(ids) != 1 || ids[0] != "site-b" {
		t.Fatalf("got ids=%v all=%v err=%v, want [site-b] false", ids, all, err)
	}

	// Read-only, asked for a writing role: nothing, not everything.
	ctx = withTokenScope(context.WithValue(context.Background(), userKey, admin),
		&TokenScope{ReadOnly: true})
	ids, all, err = svc.SiteIDsWithRole(ctx, "operator")
	if err != nil || all || len(ids) != 0 {
		t.Fatalf("a read-only admin token got ids=%v all=%v, want [] false", ids, all)
	}
}

func TestRequireGlobalAdminRefusesASiteScopedToken(t *testing.T) {
	var admin = model.User{ID: "u0", Email: "admin@example.com", IsGlobalAdmin: true}
	var reached bool
	var h = RequireGlobalAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))

	// A cookie admin: through, as before.
	var r = httptest.NewRequest(http.MethodGet, "/api/users", nil)
	r = r.WithContext(context.WithValue(r.Context(), userKey, admin))
	var w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK || !reached {
		t.Fatalf("a cookie global admin was refused: %d", w.Code)
	}

	// The same admin, through a token restricted to a site: refused, because
	// there is no site to intersect an instance-wide sub-tree against.
	reached = false
	r = httptest.NewRequest(http.MethodGet, "/api/users", nil)
	r = r.WithContext(withTokenScope(
		context.WithValue(r.Context(), userKey, admin),
		&TokenScope{SiteIDs: []string{"site-a"}}))
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden || reached {
		t.Fatalf("a site-scoped token reached instance-wide administration: %d", w.Code)
	}

	// An UNRESTRICTED token of a global admin is still a global admin: the
	// reduction must not turn into a blanket refusal of all tokens.
	r = httptest.NewRequest(http.MethodGet, "/api/users", nil)
	r = r.WithContext(withTokenScope(
		context.WithValue(r.Context(), userKey, admin), &TokenScope{}))
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("an unrestricted admin token was refused: %d", w.Code)
	}
}

// --- 4. A token cannot grant what its owner does not have ---

// THE POINT OF THE WHOLE ADR. A viewer asks for the widest token the API can
// mint — read-write, no site restriction, no expiry — and gets exactly the
// rights of a viewer. There is no field in the request that could have done
// otherwise, and this test is what keeps one from being added.
func TestAViewersTokenDoesNotBecomeAnOperatorByAskingForMore(t *testing.T) {
	var f = newFake()
	var viewer = f.user("u2", "viewer@example.com", false,
		map[string]string{"site-a": "viewer"})
	var svc = &Service{Lookup: f}
	var tok = f.mint(t, viewer, model.APIToken{
		ReadOnly:  false,      // the widest the API will mint
		SiteIDs:   []string{}, // no site restriction
		ExpiresAt: nil,        // no expiry
	})

	var w = callWithToken(probe(svc, "site-a", "viewer"), http.MethodGet, tok)
	if w.Code != http.StatusOK {
		t.Fatalf("the viewer's token could not read: %d (%s)", w.Code, body(w))
	}
	var minRole string
	for _, minRole = range []string{"operator", "site_admin"} {
		w = callWithToken(probe(svc, "site-a", minRole), http.MethodPost, tok)
		if w.Code != http.StatusForbidden {
			t.Errorf("a viewer's token reached %q: %d (%s)", minRole, w.Code, body(w))
		}
	}
	// And on a site they hold nothing on, the unrestricted scope grants nothing.
	w = callWithToken(probe(svc, "site-b", "viewer"), http.MethodGet, tok)
	if w.Code != http.StatusForbidden {
		t.Fatalf("a viewer's token reached a site with no role: %d (%s)", w.Code, body(w))
	}
}

// --- 5. Never in a cookie ---

// ADR-0011 §4: the token travels in the `Authorization` header and nowhere
// else. This is what makes it immune to CSRF — a browser does not send a header
// on its own — and it only holds as long as the cookie path refuses to look a
// token up. The two paths read two different tables, and they do not fall back
// on each other.
func TestATokenPresentedInACookieAuthenticatesNothing(t *testing.T) {
	var f = newFake()
	var u = f.user("u1", "ing@example.com", false, map[string]string{"site-a": "site_admin"})
	var svc = &Service{Lookup: f}
	var tok = f.mint(t, u, model.APIToken{})

	var r = httptest.NewRequest(http.MethodGet, "/api/sites/site-a/devices", nil)
	r.AddCookie(&http.Cookie{Name: CookieName, Value: tok})
	var w = httptest.NewRecorder()
	probe(svc, "site-a", "viewer").ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("an API token worked as a session cookie: %d (%s)", w.Code, body(w))
	}
	if len(f.touched) != 0 {
		t.Errorf("a token presented in a cookie was recorded as used: %v", f.touched)
	}
}

// And the reverse direction: a session token presented as a Bearer is not a
// token either. Nothing falls back on anything.
func TestASessionTokenPresentedAsABearerAuthenticatesNothing(t *testing.T) {
	var f = newFake()
	var u = f.user("u1", "ing@example.com", false, map[string]string{"site-a": "site_admin"})
	var svc = &Service{Lookup: f}
	var sess = f.session(t, u)

	var w = callWithToken(probe(svc, "site-a", "viewer"), http.MethodGet, sess)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("a session token worked as a Bearer: %d (%s)", w.Code, body(w))
	}
}

// A BAD BEARER IS FATAL EVEN WITH A GOOD COOKIE. The alternative — falling back
// to the cookie — would mean a browser-based client silently keeping access
// after its token was revoked, and nobody noticing for months.
func TestABadBearerIsNotRescuedByAValidCookie(t *testing.T) {
	var f = newFake()
	var u = f.user("u1", "ing@example.com", false, map[string]string{"site-a": "site_admin"})
	var svc = &Service{Lookup: f}
	var sess = f.session(t, u)

	var r = httptest.NewRequest(http.MethodGet, "/api/sites/site-a/devices", nil)
	r.AddCookie(&http.Cookie{Name: CookieName, Value: sess})
	r.Header.Set("Authorization", "Bearer "+TokenPrefix+strings.Repeat("0", 32)+"_"+
		strings.Repeat("0", 64))
	var w = httptest.NewRecorder()
	probe(svc, "site-a", "viewer").ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("a bad Bearer fell back on the cookie: %d (%s)", w.Code, body(w))
	}
}

// An `Authorization` header for something else, on the other hand, must not
// break a perfectly good session: it means "no token was presented here".
func TestAnotherAuthorizationSchemeLeavesTheCookieAlone(t *testing.T) {
	var f = newFake()
	var u = f.user("u1", "ing@example.com", false, map[string]string{"site-a": "site_admin"})
	var svc = &Service{Lookup: f}
	var sess = f.session(t, u)

	var r = httptest.NewRequest(http.MethodGet, "/api/sites/site-a/devices", nil)
	r.AddCookie(&http.Cookie{Name: CookieName, Value: sess})
	r.Header.Set("Authorization", "Basic dXNlcjpwYXNz")
	var w = httptest.NewRecorder()
	probe(svc, "site-a", "viewer").ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("a Basic header broke a valid session: %d (%s)", w.Code, body(w))
	}
}

// --- 6. The secret itself ---

func TestMintedTokensRoundTripAndHashToWhatIsStored(t *testing.T) {
	var clear, selector, hash, err = NewAPIToken()
	if err != nil {
		t.Fatalf("minting: %v", err)
	}
	var sel, ver string
	sel, ver, err = ParseAPIToken(clear)
	if err != nil {
		t.Fatalf("a minted token did not parse: %v", err)
	}
	if sel != selector {
		t.Errorf("selector round-trip: got %q want %q", sel, selector)
	}
	if string(HashToken(ver)) != string(hash) {
		t.Error("the verifier does not hash to the stored value")
	}
	if !strings.HasPrefix(clear, TokenPrefix) {
		t.Errorf("a token must be recognisable on sight: %q", clear)
	}
	// Two tokens are never the same, and the prefix shown in a list is not
	// enough to rebuild one.
	var clear2, _, _, _ = NewAPIToken()
	if clear == clear2 {
		t.Fatal("two minted tokens were identical")
	}
	if strings.Contains(clear, model.APITokenPrefix(selector)) !=
		strings.HasPrefix(clear, model.APITokenPrefix(selector)) {
		t.Error("the displayed prefix is not the head of the token")
	}
}

func TestMalformedTokensAreRefusedBeforeAnyLookup(t *testing.T) {
	var cases = []struct{ name, token string }{
		{"empty", ""},
		{"no mark", strings.Repeat("a", 97)},
		{"mark only", TokenPrefix},
		{"no separator", TokenPrefix + strings.Repeat("a", 96)},
		{"short selector", TokenPrefix + strings.Repeat("a", 31) + "_" + strings.Repeat("a", 64)},
		{"short verifier", TokenPrefix + strings.Repeat("a", 32) + "_" + strings.Repeat("a", 63)},
		{"not hex", TokenPrefix + strings.Repeat("z", 32) + "_" + strings.Repeat("a", 64)},
		{"uppercase hex", TokenPrefix + strings.Repeat("A", 32) + "_" + strings.Repeat("a", 64)},
		{"a session token", "0123456789abcdef"},
	}
	var c struct{ name, token string }
	for _, c = range cases {
		var _, _, err = ParseAPIToken(c.token)
		if err == nil {
			t.Errorf("%s: %q parsed as a token", c.name, c.token)
		}
	}
}

// A GUESSED SELECTOR LEARNS NOTHING. An unknown selector, a wrong verifier and
// a malformed value answer with the same code and the same words: whoever is
// probing cannot tell "this token exists but you have the wrong secret" from
// "no such token", and therefore cannot enumerate tokens one guess at a time.
func TestAnUnknownSelectorAnswersExactlyLikeAWrongSecret(t *testing.T) {
	var f = newFake()
	var u = f.user("u1", "ing@example.com", false, map[string]string{"site-a": "site_admin"})
	var svc = &Service{Lookup: f}
	var good = f.mint(t, u, model.APIToken{})

	// Same selector, last character of the verifier flipped.
	var wrongSecret = flipLast(good)
	// A selector that was never minted.
	var unknown = TokenPrefix + strings.Repeat("f", 32) + "_" + strings.Repeat("f", 64)
	// Not a token at all.
	var garbage = "not-a-token"

	var h = probe(svc, "site-a", "viewer")
	var first = callWithToken(h, http.MethodGet, wrongSecret)
	var v string
	for _, v = range []string{unknown, garbage} {
		var w = callWithToken(h, http.MethodGet, v)
		if w.Code != first.Code || body(w) != body(first) {
			t.Errorf("refusals differ: %d %q vs %d %q",
				w.Code, body(w), first.Code, body(first))
		}
	}
	if first.Code != http.StatusUnauthorized {
		t.Fatalf("a wrong secret answered %d, want 401", first.Code)
	}
	if len(f.touched) != 0 {
		t.Errorf("a refused token was recorded as used: %v", f.touched)
	}
}

// THE CONSTANT-TIME COMPARISON, checked two ways, neither of them a stopwatch.
//
// A measured timing test on a shared CI runner is a flaky test: the difference
// being looked for is tens of nanoseconds and the noise is milliseconds. It
// would be deleted the first week for failing at random, and then nothing would
// be checking anything. So instead:
//
//  1. BEHAVIOUR: a verifier that differs only in its LAST character and one
//     that differs only in its FIRST are refused identically. A comparison that
//     short-circuits still answers the same thing, so this alone is not proof —
//     it is the half that survives a rewrite of the primitive.
//  2. THE PRIMITIVE, read off the source: the comparison goes through
//     crypto/subtle, and the two shortcuts that would quietly undo it —
//     bytes.Equal and == on the hash — are absent. That is the half that
//     actually holds, and it fails loudly the day somebody "simplifies" it.
func TestTheVerifierIsComparedInConstantTime(t *testing.T) {
	var f = newFake()
	var u = f.user("u1", "ing@example.com", false, map[string]string{"site-a": "site_admin"})
	var svc = &Service{Lookup: f}
	var good = f.mint(t, u, model.APIToken{})
	var h = probe(svc, "site-a", "viewer")

	var last = callWithToken(h, http.MethodGet, flipLast(good))
	var first = callWithToken(h, http.MethodGet, flipFirstVerifierChar(good))
	if last.Code != http.StatusUnauthorized || first.Code != http.StatusUnauthorized {
		t.Fatalf("a wrong secret was accepted: %d / %d", last.Code, first.Code)
	}
	if body(last) != body(first) {
		t.Errorf("the refusal depends on WHERE the secret is wrong: %q vs %q",
			body(last), body(first))
	}

	var src, err = os.ReadFile("apitoken.go")
	if err != nil {
		t.Fatalf("reading apitoken.go: %v", err)
	}
	var text = string(src)
	if !strings.Contains(text, "subtle.ConstantTimeCompare(HashToken(verifier), rec.VerifierHash)") {
		t.Error("judgeAPIToken no longer compares the verifier with " +
			"crypto/subtle.ConstantTimeCompare")
	}
	var forbidden string
	for _, forbidden = range []string{"bytes.Equal", "string(rec.VerifierHash) =="} {
		if strings.Contains(text, forbidden) {
			t.Errorf("apitoken.go compares a secret with %q: use crypto/subtle", forbidden)
		}
	}
}

// --- 7. The clear value never comes back ---

// THE LIST CANNOT LEAK WHAT IT DOES NOT HAVE. model.APIToken is the only shape
// the list and creation routes serialise, and it has no field a secret could
// occupy: the hash lives in model.APITokenRecord, which carries no json tags at
// all. This test fills a record with real secret material, serialises the view
// the owner receives, and checks that neither the token nor its hash is in it.
func TestTheOwnerListCarriesNoSecret(t *testing.T) {
	var clear, selector, hash, err = NewAPIToken()
	if err != nil {
		t.Fatalf("minting: %v", err)
	}
	var rec = model.APITokenRecord{
		Token: model.APIToken{
			ID: "tok-1", Name: "laptop CLI",
			Prefix: model.APITokenPrefix(selector), ReadOnly: true,
			SiteIDs: []string{}, CreatedAt: time.Now(),
		},
		User:         model.User{ID: "u1", Email: "ing@example.com"},
		VerifierHash: hash,
	}

	var out []byte
	out, err = json.Marshal([]model.APIToken{rec.Token})
	if err != nil {
		t.Fatalf("marshalling the list: %v", err)
	}
	var text = string(out)

	var _, verifier, _ = ParseAPIToken(clear)
	var leaks = map[string]string{
		"the clear token": clear,
		"its secret half": verifier,
		"the stored hash": hexOf(hash),
	}
	var what, needle string
	for what, needle = range leaks {
		if strings.Contains(text, needle) {
			t.Errorf("the owner's list contains %s: %s", what, text)
		}
	}
	// What it DOES carry: enough to tell two tokens apart and to revoke one.
	var expected string
	for _, expected = range []string{`"id":"tok-1"`, `"name":"laptop CLI"`,
		`"prefix":"` + model.APITokenPrefix(selector) + `"`, `"read_only":true`} {
		if !strings.Contains(text, expected) {
			t.Errorf("the owner's list is missing %s: %s", expected, text)
		}
	}

	// And the whole record — the type that DOES hold the hash — has no json
	// surface to be serialised through by accident.
	out, err = json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshalling the record: %v", err)
	}
	if strings.Contains(string(out), hexOf(hash)) {
		t.Error("model.APITokenRecord serialises its verifier hash; it must not " +
			"be reachable from a response at all")
	}
}

// --- 8. last_used_at ---

// A TOKEN NOBODY KNOWS IS IN USE IS A TOKEN NOBODY DARES REVOKE (ADR-0011,
// Consequences). Every accepted call writes the stamp; no refused call does.
func TestLastUsedIsRecordedOnEveryAcceptedCall(t *testing.T) {
	var f = newFake()
	var u = f.user("u1", "ing@example.com", false, map[string]string{"site-a": "viewer"})
	var svc = &Service{Lookup: f}
	var tok = f.mint(t, u, model.APIToken{ReadOnly: true})
	var h = probe(svc, "site-a", "viewer")

	var i int
	for i = 0; i < 3; i++ {
		var w = callWithToken(h, http.MethodGet, tok)
		if w.Code != http.StatusOK {
			t.Fatalf("call %d was refused: %d (%s)", i, w.Code, body(w))
		}
	}
	if len(f.touched) != 3 {
		t.Fatalf("last_used_at written %d times for 3 calls: %v", len(f.touched), f.touched)
	}

	// A refusal that happens AFTER the identity is established — read-only on a
	// write — must not count as a use either: the stamp answers "is this token
	// doing work", and a stream of refusals is not work.
	var before = len(f.touched)
	var w = callWithToken(probe(svc, "site-a", "operator"), http.MethodPost, tok)
	if w.Code != http.StatusForbidden {
		t.Fatalf("the write was not refused: %d", w.Code)
	}
	if len(f.touched) != before {
		t.Errorf("a refused write was recorded as a use: %v", f.touched)
	}
}

// --- 9. A token cannot mint a token ---

// CookieOnly guards the routes that mint or revoke a standing credential. A
// token able to mint another one outlives its own expiry and its own
// revocation, and the two rules it sidesteps are the ones that make a leaked
// token survivable.
func TestCookieOnlyRefusesATokenAndPassesASession(t *testing.T) {
	var reached bool
	var h = CookieOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))
	var u = model.User{ID: "u1", Email: "ing@example.com"}

	// Cookie session: through.
	var r = httptest.NewRequest(http.MethodPost, "/api/auth/tokens", nil)
	r = r.WithContext(context.WithValue(r.Context(), userKey, u))
	var w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK || !reached {
		t.Fatalf("a cookie session could not mint a token: %d", w.Code)
	}

	// An API token — even an unrestricted one — refused.
	reached = false
	r = httptest.NewRequest(http.MethodPost, "/api/auth/tokens", nil)
	r = r.WithContext(withTokenScope(
		context.WithValue(r.Context(), userKey, u), &TokenScope{}))
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden || reached {
		t.Fatalf("an API token minted a token: %d (%s)", w.Code, body(w))
	}
}

// --- 10. The scope type itself, exhaustively ---

// permits() is the entire authorization contribution of a token, so it is worth
// a table rather than being inferred from the HTTP tests above.
func TestScopePermitsOnlyEverSubtracts(t *testing.T) {
	var cases = []struct {
		name    string
		scope   *TokenScope
		siteID  string
		minRole string
		want    bool
	}{
		{"no token at all", nil, "site-a", "site_admin", true},
		{"unrestricted token", &TokenScope{}, "site-a", "site_admin", true},
		{"read-only, viewer", &TokenScope{ReadOnly: true}, "site-a", "viewer", true},
		{"read-only, operator", &TokenScope{ReadOnly: true}, "site-a", "operator", false},
		{"read-only, site_admin", &TokenScope{ReadOnly: true}, "site-a", "site_admin", false},
		{"scoped, inside", &TokenScope{SiteIDs: []string{"site-a"}}, "site-a", "site_admin", true},
		{"scoped, outside", &TokenScope{SiteIDs: []string{"site-a"}}, "site-b", "viewer", false},
		{"scoped, case differs", &TokenScope{SiteIDs: []string{"abc-def"}}, "ABC-DEF", "viewer", true},
		{"scoped and read-only", &TokenScope{ReadOnly: true, SiteIDs: []string{"site-a"}},
			"site-a", "operator", false},
		// An unknown role name has level 0, below `viewer`: it must not become a
		// hole in the read-only cap. It is refused by the site list all the same.
		{"unknown role, outside the sites", &TokenScope{SiteIDs: []string{"site-a"}},
			"site-b", "nonsense", false},
	}
	var c struct {
		name    string
		scope   *TokenScope
		siteID  string
		minRole string
		want    bool
	}
	for _, c = range cases {
		var got = c.scope.permits(c.siteID, c.minRole)
		if got != c.want {
			t.Errorf("%s: permits(%q, %q) = %v, want %v",
				c.name, c.siteID, c.minRole, got, c.want)
		}
	}
}

// --- small helpers ---

// flipLast changes the last character of a token's verifier.
func flipLast(token string) string {
	var head = token[:len(token)-1]
	if token[len(token)-1] == 'a' {
		return head + "b"
	}
	return head + "a"
}

// flipFirstVerifierChar changes the FIRST character of the verifier, leaving the
// selector intact so the same row is still found.
func flipFirstVerifierChar(token string) string {
	var cut = strings.LastIndex(token, "_")
	var c byte = 'a'
	if token[cut+1] == 'a' {
		c = 'b'
	}
	return token[:cut+1] + string(c) + token[cut+2:]
}

func hexOf(b []byte) string {
	const digits = "0123456789abcdef"
	var out = make([]byte, 0, len(b)*2)
	var x byte
	for _, x = range b {
		out = append(out, digits[x>>4], digits[x&0x0f])
	}
	return string(out)
}
