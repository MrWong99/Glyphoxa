package web

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
)

// handleGoogleLogin initiates the Google OAuth2 flow.
func (s *Server) handleGoogleLogin(w http.ResponseWriter, r *http.Request) {
	if s.cfg.GoogleClientID == "" {
		writeError(w, http.StatusNotFound, "not_configured", "Google login is not enabled")
		return
	}

	state, err := GenerateState()
	if err != nil {
		slog.Error("web: generate google oauth state", "err", err)
		writeError(w, http.StatusInternalServerError, "server_error", "failed to generate state")
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "glyphoxa_oauth_state",
		Value:    state,
		Path:     "/",
		MaxAge:   300,
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
		"client_id":     {s.cfg.GoogleClientID},
		"redirect_uri":  {s.cfg.GoogleRedirectURI},
		"response_type": {"code"},
		"scope":         {"openid email profile"},
		"state":         {state},
		"access_type":   {"offline"},
	}

	target := "https://accounts.google.com/o/oauth2/v2/auth?" + params.Encode()
	http.Redirect(w, r, target, http.StatusFound)
}

// handleGoogleCallback handles the Google OAuth2 callback.
func (s *Server) handleGoogleCallback(w http.ResponseWriter, r *http.Request) {
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

	if errParam := r.URL.Query().Get("error"); errParam != "" {
		desc := r.URL.Query().Get("error_description")
		slog.Warn("web: google oauth error", "error", errParam, "description", desc)
		writeError(w, http.StatusBadRequest, "google_error", fmt.Sprintf("google: %s", desc))
		return
	}

	oauthCfg := GoogleOAuthConfig{
		ClientID:     s.cfg.GoogleClientID,
		ClientSecret: s.cfg.GoogleClientSecret,
		RedirectURI:  s.cfg.GoogleRedirectURI,
	}

	accessToken, err := ExchangeGoogleCode(r.Context(), oauthCfg, code)
	if err != nil {
		slog.Error("web: google token exchange", "err", err)
		writeError(w, http.StatusBadGateway, "google_error", "failed to exchange authorization code")
		return
	}

	googleUser, err := FetchGoogleUser(r.Context(), accessToken)
	if err != nil {
		slog.Error("web: fetch google user", "err", err)
		writeError(w, http.StatusBadGateway, "google_error", "failed to fetch user profile")
		return
	}

	tenantID := ""
	var invite *Invite
	if inviteToken != "" {
		invite, err = s.store.GetInviteByToken(r.Context(), inviteToken)
		if err != nil {
			slog.Warn("web: get invite by token", "err", err)
		}
		if invite != nil {
			tenantID = invite.TenantID
		}
	}

	user, err := s.store.UpsertGoogleUser(
		r.Context(),
		googleUser.Sub,
		googleUser.Email,
		googleUser.DisplayName(),
		googleUser.Picture,
		tenantID,
	)
	if err != nil {
		slog.Error("web: upsert google user", "google_id", googleUser.Sub, "err", err)
		writeError(w, http.StatusInternalServerError, "server_error", "failed to create user")
		return
	}

	s.processInvite(r, user, invite)

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

	slog.Info("web: user authenticated via google",
		"user_id", user.ID,
		"google_id", googleUser.Sub,
		"display_name", user.DisplayName,
		"tenant_id", user.TenantID,
	)

	redirect := "/auth/callback?token=" + url.QueryEscape(token)
	if user.TenantID == "" {
		redirect += "&onboarding=true"
	}
	http.Redirect(w, r, redirect, http.StatusFound)
}

// handleGitHubLogin initiates the GitHub OAuth2 flow.
func (s *Server) handleGitHubLogin(w http.ResponseWriter, r *http.Request) {
	if s.cfg.GitHubClientID == "" {
		writeError(w, http.StatusNotFound, "not_configured", "GitHub login is not enabled")
		return
	}

	state, err := GenerateState()
	if err != nil {
		slog.Error("web: generate github oauth state", "err", err)
		writeError(w, http.StatusInternalServerError, "server_error", "failed to generate state")
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "glyphoxa_oauth_state",
		Value:    state,
		Path:     "/",
		MaxAge:   300,
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
		"client_id":    {s.cfg.GitHubClientID},
		"redirect_uri": {s.cfg.GitHubRedirectURI},
		"scope":        {"read:user user:email"},
		"state":        {state},
	}

	target := "https://github.com/login/oauth/authorize?" + params.Encode()
	http.Redirect(w, r, target, http.StatusFound)
}

// handleGitHubCallback handles the GitHub OAuth2 callback.
func (s *Server) handleGitHubCallback(w http.ResponseWriter, r *http.Request) {
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

	if errParam := r.URL.Query().Get("error"); errParam != "" {
		desc := r.URL.Query().Get("error_description")
		slog.Warn("web: github oauth error", "error", errParam, "description", desc)
		writeError(w, http.StatusBadRequest, "github_error", fmt.Sprintf("github: %s", desc))
		return
	}

	oauthCfg := GitHubOAuthConfig{
		ClientID:     s.cfg.GitHubClientID,
		ClientSecret: s.cfg.GitHubClientSecret,
		RedirectURI:  s.cfg.GitHubRedirectURI,
	}

	accessToken, err := ExchangeGitHubCode(r.Context(), oauthCfg, code)
	if err != nil {
		slog.Error("web: github token exchange", "err", err)
		writeError(w, http.StatusBadGateway, "github_error", "failed to exchange authorization code")
		return
	}

	githubUser, err := FetchGitHubUser(r.Context(), accessToken)
	if err != nil {
		slog.Error("web: fetch github user", "err", err)
		writeError(w, http.StatusBadGateway, "github_error", "failed to fetch user profile")
		return
	}

	tenantID := ""
	var invite *Invite
	if inviteToken != "" {
		invite, err = s.store.GetInviteByToken(r.Context(), inviteToken)
		if err != nil {
			slog.Warn("web: get invite by token", "err", err)
		}
		if invite != nil {
			tenantID = invite.TenantID
		}
	}

	user, err := s.store.UpsertGitHubUser(
		r.Context(),
		githubUser.GitHubID(),
		githubUser.Email,
		githubUser.DisplayName(),
		githubUser.AvatarURL,
		tenantID,
	)
	if err != nil {
		slog.Error("web: upsert github user", "github_id", githubUser.GitHubID(), "err", err)
		writeError(w, http.StatusInternalServerError, "server_error", "failed to create user")
		return
	}

	s.processInvite(r, user, invite)

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

	slog.Info("web: user authenticated via github",
		"user_id", user.ID,
		"github_id", githubUser.GitHubID(),
		"display_name", user.DisplayName,
		"tenant_id", user.TenantID,
	)

	redirect := "/auth/callback?token=" + url.QueryEscape(token)
	if user.TenantID == "" {
		redirect += "&onboarding=true"
	}
	http.Redirect(w, r, redirect, http.StatusFound)
}

// processInvite handles invite acceptance logic shared by all OAuth providers.
func (s *Server) processInvite(r *http.Request, user *User, invite *Invite) {
	if invite == nil {
		return
	}

	switch {
	case user.TenantID != "" && user.TenantID != invite.TenantID:
		slog.Warn("web: invite ignored, user already in tenant", "user_id", user.ID, "user_tenant", user.TenantID, "invite_tenant", invite.TenantID)
	case user.TenantID == "":
		if err := s.store.UpdateUserTenant(r.Context(), user.ID, invite.TenantID, invite.Role); err != nil {
			slog.Error("web: assign user to invite tenant", "err", err)
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
