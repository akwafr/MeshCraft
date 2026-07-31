package api

import (
	"net/http"

	"meshcraft/internal/auth"
	"meshcraft/internal/model"
)

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !decode(w, r, &in) {
		return
	}
	var u, token, err = s.Auth.Login(r.Context(), in.Email, in.Password)
	if err != nil {
		respondErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	auth.SetCookie(w, r, token)
	var roles, _ = s.Store.ListUserRoles(r.Context(), u.ID)
	u.Roles = roles
	// Audit trail: event is not tied to a site (global view coming).
	s.emit(r, model.Event{
		Type: "user.login", Severity: model.SevInfo,
		Message: "Login from " + u.Email, Actor: u.Email,
	})
	respond(w, http.StatusOK, u)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	var c, err = r.Cookie(auth.CookieName)
	if err == nil && c.Value != "" {
		_ = s.Auth.Logout(r.Context(), c.Value)
	}
	auth.ClearCookie(w)
	respond(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	var u = auth.UserFrom(r.Context())
	var roles, err = s.Store.ListUserRoles(r.Context(), u.ID)
	if err != nil {
		fail(w, err)
		return
	}
	u.Roles = roles
	respond(w, http.StatusOK, u)
}
