package web

import (
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

	// Clear the state cookie.
	http.SetCookie(w, &http.Cookie{
		Name:   "glyphoxa_oauth_state",
		Value:  "",
		Path:   "/",
		MaxAge: -1,
	})

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

	// Upsert user — for MVP, auto-assign to a default tenant.
	tenantID := "default"
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

// handleRefresh issues a new JWT from a valid existing token.
func (s *Server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	claims := ClaimsFromContext(r.Context())
	if claims == nil {
		writeError(w, http.StatusUnauthorized, "no_auth", "authentication required")
		return
	}

	// Re-fetch user to get current role/tenant.
	user, err := s.store.GetUser(r.Context(), claims.Sub)
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

// handleMe returns the current authenticated user's profile.
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	claims := ClaimsFromContext(r.Context())
	if claims == nil {
		writeError(w, http.StatusUnauthorized, "no_auth", "authentication required")
		return
	}

	user, err := s.store.GetUser(r.Context(), claims.Sub)
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
