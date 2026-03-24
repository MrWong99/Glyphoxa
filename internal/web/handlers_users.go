package web

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
)

// handleListUsers returns paginated users for the authenticated tenant.
// Requires tenant_admin role.
func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	claims := ClaimsFromContext(r.Context())
	if claims == nil {
		writeError(w, http.StatusUnauthorized, "no_auth", "authentication required")
		return
	}

	role := r.URL.Query().Get("role")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))

	users, total, err := s.store.ListUsers(r.Context(), claims.TenantID, role, limit, offset)
	if err != nil {
		slog.Error("web: list users", "tenant_id", claims.TenantID, "err", err)
		writeError(w, http.StatusInternalServerError, "server_error", "failed to list users")
		return
	}
	if users == nil {
		users = []User{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"data":  users,
		"total": total,
	})
}

// handleGetUser returns a single user by ID. Users can always read their own
// profile; tenant_admin can read any user in their tenant.
func (s *Server) handleGetUser(w http.ResponseWriter, r *http.Request) {
	claims := ClaimsFromContext(r.Context())
	if claims == nil {
		writeError(w, http.StatusUnauthorized, "no_auth", "authentication required")
		return
	}

	id := r.PathValue("id")
	// Non-admin users can only view themselves.
	if id != claims.Sub && !hasMinRole(claims.Role, "tenant_admin") {
		writeError(w, http.StatusForbidden, "insufficient_role", "insufficient permissions")
		return
	}

	user, err := s.store.GetUser(r.Context(), id)
	if err != nil {
		slog.Error("web: get user", "user_id", id, "err", err)
		writeError(w, http.StatusInternalServerError, "server_error", "failed to get user")
		return
	}
	if user == nil || user.TenantID != claims.TenantID {
		writeError(w, http.StatusNotFound, "not_found", "user not found")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"data": user})
}

// UserUpdateRequest is the JSON body for updating a user.
type UserUpdateRequest struct {
	DisplayName *string `json:"display_name"`
	Role        *string `json:"role"`
}

// handleUpdateUser updates a user's display_name or role. Requires
// tenant_admin for role changes.
func (s *Server) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	claims := ClaimsFromContext(r.Context())
	if claims == nil {
		writeError(w, http.StatusUnauthorized, "no_auth", "authentication required")
		return
	}

	id := r.PathValue("id")
	// Non-admin users can only update themselves (display_name only).
	if id != claims.Sub && !hasMinRole(claims.Role, "tenant_admin") {
		writeError(w, http.StatusForbidden, "insufficient_role", "insufficient permissions")
		return
	}

	// When admins update another user, verify they belong to the same tenant.
	if id != claims.Sub {
		target, err := s.store.GetUser(r.Context(), id)
		if err != nil || target == nil || target.TenantID != claims.TenantID {
			writeError(w, http.StatusNotFound, "not_found", "user not found")
			return
		}
	}

	var req UserUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "invalid JSON body")
		return
	}

	// Role changes require tenant_admin.
	if req.Role != nil && !hasMinRole(claims.Role, "tenant_admin") {
		writeError(w, http.StatusForbidden, "insufficient_role", "only admins can change roles")
		return
	}

	u := &User{ID: id}
	if req.DisplayName != nil {
		if *req.DisplayName == "" {
			writeError(w, http.StatusBadRequest, "invalid_name", "display_name must not be empty")
			return
		}
		u.DisplayName = *req.DisplayName
	}
	if req.Role != nil {
		if !ValidRoles[*req.Role] {
			writeError(w, http.StatusBadRequest, "invalid_role", "invalid role")
			return
		}
		u.Role = *req.Role
	}

	if err := s.store.UpdateUser(r.Context(), u); err != nil {
		slog.Error("web: update user", "user_id", id, "err", err)
		writeError(w, http.StatusInternalServerError, "server_error", "failed to update user")
		return
	}

	updated, err := s.store.GetUser(r.Context(), id)
	if err != nil || updated == nil {
		writeError(w, http.StatusInternalServerError, "server_error", "failed to fetch updated user")
		return
	}

	slog.Info("web: user updated", "user_id", id, "by", claims.Sub)
	writeJSON(w, http.StatusOK, map[string]any{"data": updated})
}

// handleDeleteUser soft-deletes a user. Requires tenant_admin.
func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	claims := ClaimsFromContext(r.Context())
	if claims == nil {
		writeError(w, http.StatusUnauthorized, "no_auth", "authentication required")
		return
	}

	id := r.PathValue("id")
	if id == claims.Sub {
		writeError(w, http.StatusBadRequest, "self_delete", "cannot delete yourself")
		return
	}

	// Verify the target user belongs to the same tenant before deleting.
	target, err := s.store.GetUser(r.Context(), id)
	if err != nil || target == nil || target.TenantID != claims.TenantID {
		writeError(w, http.StatusNotFound, "not_found", "user not found")
		return
	}

	if err := s.store.DeleteUser(r.Context(), claims.TenantID, id); err != nil {
		slog.Error("web: delete user", "user_id", id, "err", err)
		writeError(w, http.StatusNotFound, "not_found", "user not found")
		return
	}

	slog.Info("web: user deleted", "user_id", id, "by", claims.Sub)
	w.WriteHeader(http.StatusNoContent)
}

// InviteCreateRequest is the JSON body for creating an invite.
type InviteCreateRequest struct {
	Role string `json:"role"`
}

// handleCreateInvite creates a new tenant invite link. Requires tenant_admin.
func (s *Server) handleCreateInvite(w http.ResponseWriter, r *http.Request) {
	claims := ClaimsFromContext(r.Context())
	if claims == nil {
		writeError(w, http.StatusUnauthorized, "no_auth", "authentication required")
		return
	}

	var req InviteCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "invalid JSON body")
		return
	}
	if req.Role == "" {
		req.Role = "viewer"
	}
	if !ValidRoles[req.Role] {
		writeError(w, http.StatusBadRequest, "invalid_role", "invalid role")
		return
	}

	inv := &Invite{
		TenantID:  claims.TenantID,
		Role:      req.Role,
		CreatedBy: claims.Sub,
	}

	if err := s.store.CreateInvite(r.Context(), inv); err != nil {
		slog.Error("web: create invite", "tenant_id", claims.TenantID, "err", err)
		writeError(w, http.StatusInternalServerError, "server_error", "failed to create invite")
		return
	}

	slog.Info("web: invite created", "invite_id", inv.ID, "tenant_id", claims.TenantID, "role", inv.Role)
	writeJSON(w, http.StatusCreated, map[string]any{"data": inv})
}

// handleUpdateMe allows the current user to update their own display_name.
func (s *Server) handleUpdateMe(w http.ResponseWriter, r *http.Request) {
	claims := ClaimsFromContext(r.Context())
	if claims == nil {
		writeError(w, http.StatusUnauthorized, "no_auth", "authentication required")
		return
	}

	var req struct {
		DisplayName string `json:"display_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "invalid JSON body")
		return
	}
	if req.DisplayName == "" {
		writeError(w, http.StatusBadRequest, "invalid_name", "display_name is required")
		return
	}

	u := &User{ID: claims.Sub, DisplayName: req.DisplayName}
	if err := s.store.UpdateUser(r.Context(), u); err != nil {
		slog.Error("web: update me", "user_id", claims.Sub, "err", err)
		writeError(w, http.StatusInternalServerError, "server_error", "failed to update profile")
		return
	}

	updated, err := s.store.GetUser(r.Context(), claims.Sub)
	if err != nil || updated == nil {
		writeError(w, http.StatusInternalServerError, "server_error", "failed to fetch updated profile")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"data": updated})
}

// handleUpdatePreferences merges the JSON body into the current user's
// preferences column.
func (s *Server) handleUpdatePreferences(w http.ResponseWriter, r *http.Request) {
	claims := ClaimsFromContext(r.Context())
	if claims == nil {
		writeError(w, http.StatusUnauthorized, "no_auth", "authentication required")
		return
	}

	var prefs json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&prefs); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "invalid JSON body")
		return
	}

	updated, err := s.store.UpdateUserPreferences(r.Context(), claims.Sub, prefs)
	if err != nil {
		slog.Error("web: update preferences", "user_id", claims.Sub, "err", err)
		writeError(w, http.StatusInternalServerError, "server_error", "failed to update preferences")
		return
	}
	if updated == nil {
		writeError(w, http.StatusNotFound, "not_found", "user not found")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"data": updated})
}
