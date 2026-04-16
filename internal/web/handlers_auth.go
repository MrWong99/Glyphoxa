package web

import (
	"crypto/subtle"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
)

// handleDiscordLogin initiates the Discord OAuth2 flow by redirecting the user
// to Discord's authorization page.
func (s *Server) handleDiscordLogin(w http.ResponseWriter, r *http.Request) {
	state, err := GenerateState()
	if err != nil {
		slog.Error("web: generate oauth state", "err", err)
		writeError(w, http.StatusInternalServerError, "server_error", "failed to generate state")
		return
	}

	// Store state in a short-lived cookie for CSRF validation on callback.
	http.SetCookie(w, &http.Cookie{
		Name:     "glyphoxa_oauth_state",
		Value:    state,
		Path:     "/",
		MaxAge:   300, // 5 minutes
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})

	if inviteToken := r.URL.Query().Get("invite"); inviteToken != "" {
		http.SetCookie(w, &http.Cookie{
			Name:     "glyphoxa_invite",
			Value:    inviteToken,
			Path:     "/",
			MaxAge:   300,
			HttpOnly: true,
			Secure:   true,
			SameSite: http.SameSiteLaxMode,
		})
	}

	params := url.Values{
		"client_id":     {s.cfg.DiscordClientID},
		"redirect_uri":  {s.cfg.DiscordRedirectURI},
		"response_type": {"code"},
		"scope":         {"identify email"},
		"state":         {state},
	}

	target := "https://discord.com/oauth2/authorize?" + params.Encode()
	http.Redirect(w, r, target, http.StatusFound)
}

// handleDiscordCallback handles the Discord OAuth2 callback, exchanges the
// authorization code for tokens, fetches the user profile, upserts the user,
// and issues a JWT.
func (s *Server) handleDiscordCallback(w http.ResponseWriter, r *http.Request) {
	// Validate state for CSRF protection.
	stateCookie, err := r.Cookie("glyphoxa_oauth_state")
	if err != nil || stateCookie.Value == "" {
		writeError(w, http.StatusBadRequest, "invalid_state", "missing OAuth state cookie")
		return
	}
	if r.URL.Query().Get("state") != stateCookie.Value {
		writeError(w, http.StatusBadRequest, "invalid_state", "state mismatch")
		return
	}

	clearAuthCookie(w, "glyphoxa_oauth_state")

	var inviteToken string
	if ic, err := r.Cookie("glyphoxa_invite"); err == nil {
		inviteToken = ic.Value
	}
	clearAuthCookie(w, "glyphoxa_invite")

	code := r.URL.Query().Get("code")
	if code == "" {
		writeError(w, http.StatusBadRequest, "missing_code", "authorization code required")
		return
	}

	// Check for error from Discord.
	if errParam := r.URL.Query().Get("error"); errParam != "" {
		desc := r.URL.Query().Get("error_description")
		slog.Warn("web: discord oauth error", "error", errParam, "description", desc)
		writeError(w, http.StatusBadRequest, "discord_error", fmt.Sprintf("discord: %s", desc))
		return
	}

	oauthCfg := DiscordOAuthConfig{
		ClientID:     s.cfg.DiscordClientID,
		ClientSecret: s.cfg.DiscordClientSecret,
		RedirectURI:  s.cfg.DiscordRedirectURI,
	}

	// Exchange code for access token.
	accessToken, err := ExchangeDiscordCode(r.Context(), oauthCfg, code)
	if err != nil {
		slog.Error("web: discord token exchange", "err", err)
		writeError(w, http.StatusBadGateway, "discord_error", "failed to exchange authorization code")
		return
	}

	// Fetch Discord user profile.
	discordUser, err := FetchDiscordUser(r.Context(), accessToken)
	if err != nil {
		slog.Error("web: fetch discord user", "err", err)
		writeError(w, http.StatusBadGateway, "discord_error", "failed to fetch user profile")
		return
	}

	var invite *Invite
	if inviteToken != "" {
		invite, err = s.store.GetInviteByToken(r.Context(), inviteToken)
		if err != nil {
			slog.Warn("web: get invite by token", "err", err)
		}
		if invite == nil {
			slog.Warn("web: invite token invalid or expired", "token_prefix", inviteToken[:min(8, len(inviteToken))])
		}
	}

	tenantID := ""
	if invite != nil {
		tenantID = invite.TenantID
	}

	user, err := s.store.UpsertDiscordUser(
		r.Context(),
		discordUser.ID,
		discordUser.Email,
		discordUser.DisplayName(),
		discordUser.AvatarURL(),
		tenantID,
	)
	if err != nil {
		slog.Error("web: upsert discord user", "discord_id", discordUser.ID, "err", err)
		writeError(w, http.StatusInternalServerError, "server_error", "failed to create user")
		return
	}

	if invite != nil {
		switch {
		case user.TenantID != "" && user.TenantID != invite.TenantID:
			slog.Warn("web: invite ignored, user already in tenant", "user_id", user.ID, "user_tenant", user.TenantID, "invite_tenant", invite.TenantID)
		case user.TenantID == "":
			if err := s.store.UpdateUserTenant(r.Context(), user.ID, invite.TenantID, invite.Role); err != nil {
				slog.Error("web: assign user to invite tenant", "err", err)
				writeError(w, http.StatusInternalServerError, "server_error", "failed to join tenant")
				return
			}
			user.TenantID = invite.TenantID
			user.Role = invite.Role
			fallthrough
		default:
			if user.Role != invite.Role {
				if err := s.store.UpdateUser(r.Context(), &User{ID: user.ID, TenantID: user.TenantID, Role: invite.Role}); err != nil {
					slog.Warn("web: update invite role", "err", err)
				} else {
					user.Role = invite.Role
				}
			}
			if err := s.store.UseInvite(r.Context(), invite.ID, user.ID); err != nil {
				slog.Warn("web: mark invite used", "invite_id", invite.ID, "err", err)
			}
		}
	}

	// Issue JWT.
	token, err := SignJWT(s.cfg.JWTSecret, Claims{
		Sub:      user.ID,
		TenantID: user.TenantID,
		Role:     user.Role,
	})
	if err != nil {
		slog.Error("web: sign jwt", "user_id", user.ID, "err", err)
		writeError(w, http.StatusInternalServerError, "server_error", "failed to issue token")
		return
	}

	slog.Info("web: user authenticated via discord",
		"user_id", user.ID,
		"discord_id", discordUser.ID,
		"display_name", user.DisplayName,
		"tenant_id", user.TenantID,
	)

	// Redirect to the frontend callback page with the JWT as a query param.
	// The frontend will store the token and redirect to the dashboard.
	redirect := "/auth/callback?token=" + url.QueryEscape(token)
	if user.TenantID == "" {
		redirect += "&onboarding=true"
	}
	http.Redirect(w, r, redirect, http.StatusFound)
}

// handleRefresh issues a new JWT from a valid existing token.
func (s *Server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	claims := requireClaims(w, r)
	if claims == nil {
		return
	}

	// Re-fetch user to get current role/tenant.
	user, err := s.store.GetUser(r.Context(), claims.TenantID, claims.Sub)
	if err != nil || user == nil {
		writeError(w, http.StatusUnauthorized, "user_not_found", "user no longer exists")
		return
	}

	token, err := SignJWT(s.cfg.JWTSecret, Claims{
		Sub:      user.ID,
		TenantID: user.TenantID,
		Role:     user.Role,
	})
	if err != nil {
		slog.Error("web: refresh sign jwt", "user_id", user.ID, "err", err)
		writeError(w, http.StatusInternalServerError, "server_error", "failed to issue token")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"data": map[string]any{
			"access_token": token,
			"token_type":   "Bearer",
			"expires_in":   86400,
			"user":         user,
		},
	})
}

// handleAPIKeyLogin authenticates a user via a shared admin API key and
// returns a JWT for the super_admin user. This is a fallback for environments
// where Discord OAuth2 is not configured.
func (s *Server) handleAPIKeyLogin(w http.ResponseWriter, r *http.Request) {
	if s.cfg.AdminAPIKey == "" {
		writeError(w, http.StatusNotFound, "not_configured", "API key login is not enabled")
		return
	}

	var body struct {
		APIKey string `json:"api_key"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.APIKey == "" {
		writeError(w, http.StatusBadRequest, "missing_key", "api_key is required")
		return
	}

	// Constant-time comparison to prevent timing attacks.
	if subtle.ConstantTimeCompare([]byte(body.APIKey), []byte(s.cfg.AdminAPIKey)) != 1 {
		slog.Warn("web: invalid API key login attempt", "remote", r.RemoteAddr)
		writeError(w, http.StatusUnauthorized, "invalid_key", "invalid API key")
		return
	}

	tenantID := "default"
	user, err := s.store.EnsureAdminUser(r.Context(), tenantID)
	if err != nil {
		slog.Error("web: ensure admin user", "err", err)
		writeError(w, http.StatusInternalServerError, "server_error", "failed to create admin user")
		return
	}

	token, err := SignJWT(s.cfg.JWTSecret, Claims{
		Sub:      user.ID,
		TenantID: user.TenantID,
		Role:     user.Role,
	})
	if err != nil {
		slog.Error("web: sign jwt for apikey login", "user_id", user.ID, "err", err)
		writeError(w, http.StatusInternalServerError, "server_error", "failed to issue token")
		return
	}

	slog.Info("web: user authenticated via API key",
		"user_id", user.ID,
		"remote", r.RemoteAddr,
	)

	writeJSON(w, http.StatusOK, map[string]any{
		"data": map[string]any{
			"access_token": token,
			"token_type":   "Bearer",
			"expires_in":   86400,
			"user":         user,
		},
	})
}

// handleMe returns the current authenticated user's profile.
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	claims := requireClaims(w, r)
	if claims == nil {
		return
	}

	user, err := s.store.GetUser(r.Context(), claims.TenantID, claims.Sub)
	if err != nil {
		slog.Error("web: get user for /me", "user_id", claims.Sub, "err", err)
		writeError(w, http.StatusInternalServerError, "server_error", "failed to fetch user")
		return
	}
	if user == nil {
		writeError(w, http.StatusNotFound, "user_not_found", "user no longer exists")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"data": user})
}

// clearAuthCookie expires a cookie using the same security flags it was set
// with, so the Set-Cookie response matches the original cookie's scope and
// does not silently widen it.
func clearAuthCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
}
