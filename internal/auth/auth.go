// Package auth handles sessions, cookies, and per-site access control.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"time"

	"meshcraft/internal/model"
	"meshcraft/internal/store"
)

const (
	CookieName = "meshcraft_session"
	sessionTTL = 7 * 24 * time.Hour
)

type ctxKey int

const userKey ctxKey = 0

// Identities is the narrow slice of the store that AUTHENTICATION and
// AUTHORIZATION need: resolve a credential to a person, and read that person's
// roles.
//
// It exists for one reason, and the reason is this package's job. Authentication
// is the surface where a mistake is silent — a token that outlives its
// revocation, a scope that is read in one place and forgotten in another — so
// every refusal in here has to be provable by a test, and a test that needs a
// PostgreSQL to run is a test that does not run. With this interface the whole
// of auth is exercised end to end, both forms of credential included, against a
// fake that fits in one test file (cf. apitoken_test.go).
//
// *store.Store satisfies it as it stands.
type Identities interface {
	GetSessionUser(ctx context.Context, tokenHash []byte) (model.User, error)
	GetUser(ctx context.Context, id string) (model.User, error)
	GetSiteRole(ctx context.Context, userID, siteID string) (string, error)
	ListUserRoles(ctx context.Context, userID string) ([]model.Role, error)
	APITokenBySelector(ctx context.Context, selector string) (model.APITokenRecord, error)
	TouchAPIToken(ctx context.Context, tokenID string)
}

type Service struct {
	Store *store.Store
	// Lookup replaces Store as the source of identities when it is set. Only a
	// test sets it; production wiring passes Store and leaves this nil.
	Lookup Identities
}

func (a *Service) idents() Identities {
	if a.Lookup != nil {
		return a.Lookup
	}
	return a.Store
}

// NewSessionToken generates an opaque token and its storage hash.
func NewSessionToken() (token string, hash []byte, err error) {
	var raw = make([]byte, 32)
	_, err = rand.Read(raw)
	if err != nil {
		return "", nil, err
	}
	token = hex.EncodeToString(raw)
	hash = HashToken(token)
	return token, hash, nil
}

func HashToken(token string) []byte {
	var h = sha256.Sum256([]byte(token))
	return h[:]
}

func (a *Service) Login(ctx context.Context, email, password string) (model.User, string, error) {
	var u, pwHash, err = a.Store.GetUserByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// Dummy hash to keep response time constant.
			VerifyPassword(password, "$argon2id$v=19$m=65536,t=1,p=4$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
			return model.User{}, "", errors.New("invalid credentials")
		}
		return model.User{}, "", err
	}
	if !VerifyPassword(password, pwHash) {
		return model.User{}, "", errors.New("invalid credentials")
	}
	var token, hash, tokErr = NewSessionToken()
	if tokErr != nil {
		return model.User{}, "", tokErr
	}
	if err = a.Store.CreateSession(ctx, hash, u.ID, time.Now().Add(sessionTTL)); err != nil {
		return model.User{}, "", err
	}
	return u, token, nil
}

func (a *Service) Logout(ctx context.Context, token string) error {
	return a.Store.DeleteSession(ctx, HashToken(token))
}

func SetCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   r.TLS != nil,
		MaxAge:   int(sessionTTL.Seconds()),
	})
}

func ClearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: CookieName, Value: "", Path: "/", HttpOnly: true, MaxAge: -1,
	})
}

// Middleware authenticates the request and loads the user into the context
// (or 401).
//
// TWO FORMS OF CREDENTIAL, ONE PLACE THAT KNOWS IT. A session cookie and an
// `Authorization: Bearer` API token both resolve to the SAME model.User, and
// everything downstream — every CanSite, every RequireGlobalAdmin, every
// handler — is unable to tell which one was presented. That is the decision of
// ADR-0011, and this function is where it is kept: a second place that read
// tokens could disagree with this one about expiry, revocation or scope, and
// the disagreement would be resolved in favour of the more permissive door.
//
// THE TWO FORMS DO NOT FALL BACK ON EACH OTHER. A Bearer header decides the
// request on its own: if the token is bad, the answer is 401 even when a
// perfectly valid cookie rides along. And the cookie path looks a value up in
// `sessions` only, so an API token pasted into the cookie authenticates nothing
// — which is exactly what makes a token immune to CSRF (ADR-0011 §4), and is
// locked by a test.
func (a *Service) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var bearer = bearerToken(r)
		if bearer != "" {
			var u, scope, v = a.authenticateToken(r.Context(), bearer, r.Method)
			if v.Status != 0 {
				http.Error(w, `{"error":"`+v.Reason+`"}`, v.Status)
				return
			}
			next.ServeHTTP(w, r.WithContext(
				withTokenScope(context.WithValue(r.Context(), userKey, u), scope)))
			return
		}
		var c, err = r.Cookie(CookieName)
		if err != nil || c.Value == "" {
			http.Error(w, `{"error":"not authenticated"}`, http.StatusUnauthorized)
			return
		}
		var u, sessErr = a.idents().GetSessionUser(r.Context(), HashToken(c.Value))
		if sessErr != nil {
			http.Error(w, `{"error":"invalid session"}`, http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey, u)))
	})
}

// UserFrom returns the authenticated user from the context.
func UserFrom(ctx context.Context) model.User {
	var u, _ = ctx.Value(userKey).(model.User)
	return u
}

// RequireGlobalAdmin protects a sub-tree.
//
// A SITE-SCOPED TOKEN IS REFUSED HERE even when its owner is a global admin.
// The sub-trees behind this gate are instance-wide by nature — users, proxies,
// driver repositories — so there is no site to intersect a scope against, and
// "this token only reaches site A" would silently stop meaning anything. That
// is still only ever a reduction (ADR-0011 §2): the gate never lets through
// somebody a cookie session would have refused.
func RequireGlobalAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !UserFrom(r.Context()).IsGlobalAdmin {
			http.Error(w, `{"error":"reserved for global administrators"}`, http.StatusForbidden)
			return
		}
		var sc = TokenScopeFrom(r.Context())
		if sc != nil && len(sc.SiteIDs) > 0 {
			http.Error(w,
				`{"error":"this token is restricted to a list of sites: instance-wide administration is out of its scope"}`,
				http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// SiteIDsWithRole returns sites where the user reaches at least minRole.
// For a global admin the list is meaningless: they see everything, hence the
// `all` boolean — the caller must not filter in that case.
//
// A TOKEN'S SCOPE NARROWS THE ANSWER, including the `all` shortcut: a global
// admin holding a site-scoped token gets that list and all=false, because
// answering "everything" would hand the caller every site in the instance
// through the first screen that lists them.
func (a *Service) SiteIDsWithRole(ctx context.Context, minRole string) (ids []string, all bool, err error) {
	var u = UserFrom(ctx)
	var sc = TokenScopeFrom(ctx)
	// Read-only caps the effective role at `viewer`, so a question about
	// `operator` or above has an empty answer — the same rule permits() applies,
	// asked a different way.
	if !sc.allowsRole(minRole) {
		return []string{}, false, nil
	}
	if u.IsGlobalAdmin {
		var scoped = sc.scopeSites()
		if len(scoped) == 0 {
			return nil, true, nil
		}
		return append([]string{}, scoped...), false, nil
	}
	var roles, rolesErr = a.idents().ListUserRoles(ctx, u.ID)
	if rolesErr != nil {
		return nil, false, rolesErr
	}
	ids = []string{}
	var r model.Role
	for _, r = range roles {
		if model.RoleLevel[r.Role] >= model.RoleLevel[minRole] {
			ids = append(ids, r.SiteID)
		}
	}
	return sc.keep(ids), false, nil
}

// CanSite checks that the user has at least minRole on the site.
// CanSiteFor answers the same question as [Service.CanSite] for a NAMED user,
// rather than the one carried in the context.
//
// Needed by callers that authenticate outside the HTTP middleware and therefore
// have no user in their context — the SOCKS route is the first (cf.
// api/socks_handlers.go), where the operator is identified by a token presented
// on a raw TCP connection.
//
// Deliberately re-reads the global-admin flag rather than trusting a caller to
// pass it: a permission check whose inputs come from the caller is a permission
// check the caller can pass itself.
func (a *Service) CanSiteFor(ctx context.Context, userID, siteID, minRole string) bool {
	if userID == "" || siteID == "" {
		return false
	}
	if !TokenScopeFrom(ctx).permits(siteID, minRole) {
		return false
	}
	var u, err = a.idents().GetUser(ctx, userID)
	if err != nil {
		return false
	}
	if u.IsGlobalAdmin {
		return true
	}
	var role string
	role, err = a.idents().GetSiteRole(ctx, userID, siteID)
	if err != nil || role == "" {
		return false
	}
	return model.RoleLevel[role] >= model.RoleLevel[minRole]
}

// THE TOKEN SCOPE IS CONSULTED BEFORE THE GLOBAL-ADMIN SHORTCUT, and that order
// is the whole of ADR-0011 §2 in one line. Asked after it, a site-scoped token
// belonging to a global admin would reach every site in the instance, because
// the shortcut answers true for any site including one the scope excluded.
//
// The scope only ever subtracts: a user who could not reach the site cannot
// reach it with a token, whatever the token asks for.
func (a *Service) CanSite(ctx context.Context, siteID, minRole string) bool {
	if !TokenScopeFrom(ctx).permits(siteID, minRole) {
		return false
	}
	var u = UserFrom(ctx)
	if u.IsGlobalAdmin {
		return true
	}
	var role, err = a.idents().GetSiteRole(ctx, u.ID, siteID)
	if err != nil || role == "" {
		return false
	}
	return model.RoleLevel[role] >= model.RoleLevel[minRole]
}
