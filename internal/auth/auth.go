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

type Service struct {
	Store *store.Store
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

// Middleware loads the session user into the context (or 401).
func (a *Service) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var c, err = r.Cookie(CookieName)
		if err != nil || c.Value == "" {
			http.Error(w, `{"error":"not authenticated"}`, http.StatusUnauthorized)
			return
		}
		var u, sessErr = a.Store.GetSessionUser(r.Context(), HashToken(c.Value))
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
func RequireGlobalAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !UserFrom(r.Context()).IsGlobalAdmin {
			http.Error(w, `{"error":"reserved for global administrators"}`, http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// SiteIDsWithRole returns sites where the user reaches at least minRole.
// For a global admin the list is meaningless: they see everything, hence the
// `all` boolean — the caller must not filter in that case.
func (a *Service) SiteIDsWithRole(ctx context.Context, minRole string) (ids []string, all bool, err error) {
	var u = UserFrom(ctx)
	if u.IsGlobalAdmin {
		return nil, true, nil
	}
	var roles, rolesErr = a.Store.ListUserRoles(ctx, u.ID)
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
	return ids, false, nil
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
	var u, err = a.Store.GetUser(ctx, userID)
	if err != nil {
		return false
	}
	if u.IsGlobalAdmin {
		return true
	}
	var role string
	role, err = a.Store.GetSiteRole(ctx, userID, siteID)
	if err != nil || role == "" {
		return false
	}
	return model.RoleLevel[role] >= model.RoleLevel[minRole]
}

func (a *Service) CanSite(ctx context.Context, siteID, minRole string) bool {
	var u = UserFrom(ctx)
	if u.IsGlobalAdmin {
		return true
	}
	var role, err = a.Store.GetSiteRole(ctx, u.ID, siteID)
	if err != nil || role == "" {
		return false
	}
	return model.RoleLevel[role] >= model.RoleLevel[minRole]
}
