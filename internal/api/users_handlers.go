package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"meshcraft/internal/auth"
	"meshcraft/internal/model"
)

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	var users, err = s.Store.ListUsers(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	var i int
	for i = range users {
		var roles, rErr = s.Store.ListUserRoles(r.Context(), users[i].ID)
		if rErr != nil {
			fail(w, rErr)
			return
		}
		users[i].Roles = roles
	}
	respond(w, http.StatusOK, users)
}

type userInput struct {
	Email         string `json:"email"`
	DisplayName   string `json:"display_name"`
	Password      string `json:"password"`
	IsGlobalAdmin bool   `json:"is_global_admin"`
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var in userInput
	if !decode(w, r, &in) {
		return
	}
	if in.Email == "" || len(in.Password) < 8 {
		respondErr(w, http.StatusBadRequest, "email required and password of at least 8 characters")
		return
	}
	var hash, hErr = auth.HashPassword(in.Password)
	if hErr != nil {
		fail(w, hErr)
		return
	}
	var u, err = s.Store.CreateUser(r.Context(), in.Email, hash, in.DisplayName, in.IsGlobalAdmin)
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, http.StatusCreated, u)
}

func (s *Server) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	var id = chi.URLParam(r, "id")
	var in userInput
	if !decode(w, r, &in) {
		return
	}
	var me = auth.UserFrom(r.Context())
	if me.ID == id && !in.IsGlobalAdmin {
		respondErr(w, http.StatusBadRequest, "cannot remove your own global administrator status")
		return
	}
	var hash string
	if in.Password != "" {
		if len(in.Password) < 8 {
			respondErr(w, http.StatusBadRequest, "password of at least 8 characters")
			return
		}
		var hErr error
		hash, hErr = auth.HashPassword(in.Password)
		if hErr != nil {
			fail(w, hErr)
			return
		}
	}
	var u, err = s.Store.UpdateUser(r.Context(), id, in.DisplayName, in.IsGlobalAdmin, hash)
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, http.StatusOK, u)
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	var id = chi.URLParam(r, "id")
	if auth.UserFrom(r.Context()).ID == id {
		respondErr(w, http.StatusBadRequest, "cannot delete your own account")
		return
	}
	var err = s.Store.DeleteUser(r.Context(), id)
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleSetUserRoles(w http.ResponseWriter, r *http.Request) {
	var id = chi.URLParam(r, "id")
	var in []model.Role
	if !decode(w, r, &in) {
		return
	}
	var role model.Role
	for _, role = range in {
		if model.RoleLevel[role.Role] == 0 {
			respondErr(w, http.StatusBadRequest, "unknown role: "+role.Role)
			return
		}
	}
	var err = s.Store.SetUserRoles(r.Context(), id, in)
	if err != nil {
		fail(w, err)
		return
	}
	var roles, listErr = s.Store.ListUserRoles(r.Context(), id)
	if listErr != nil {
		fail(w, listErr)
		return
	}
	respond(w, http.StatusOK, roles)
}
