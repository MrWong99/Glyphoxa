package web

import (
	"encoding/json"
	"net/http"
	"strconv"
)

// requireClaims extracts JWT claims from the request context, writing a 401
// error response if no authenticated user is present. Returns nil when auth
// is missing so callers can return early.
func requireClaims(w http.ResponseWriter, r *http.Request) *Claims {
	claims := ClaimsFromContext(r.Context())
	if claims == nil {
		writeError(w, http.StatusUnauthorized, "no_auth", "authentication required")
	}
	return claims
}

// decodeJSON reads the request body into v, writing a 400 error response on
// failure. Returns true on success.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "invalid JSON body")
		return false
	}
	return true
}

// requireCampaign loads the campaign identified by the "id" path parameter
// and verifies it belongs to the given tenant. Writes a 404 error and returns
// nil, "" if the campaign is missing or unauthorized. On success it returns
// the campaign and its ID.
func (s *Server) requireCampaign(w http.ResponseWriter, r *http.Request, tenantID string) (*Campaign, string) {
	id := r.PathValue("id")
	campaign, err := s.store.GetCampaign(r.Context(), tenantID, id)
	if err != nil || campaign == nil {
		writeError(w, http.StatusNotFound, "not_found", "campaign not found")
		return nil, ""
	}
	return campaign, id
}

// parseLimitOffset extracts "limit" and "offset" query parameters with a
// sensible default and maximum. Limit is clamped to [1, 100].
func parseLimitOffset(r *http.Request, defaultLimit int) (limit, offset int) {
	limit, _ = strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 100 {
		limit = defaultLimit
	}
	offset, _ = strconv.Atoi(r.URL.Query().Get("offset"))
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}
