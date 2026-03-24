package web

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// CursorPage holds cursor-based pagination parameters parsed from query params.
type CursorPage struct {
	Cursor string
	Limit  int
}

// PageMeta contains pagination metadata returned alongside a list response.
type PageMeta struct {
	NextCursor string `json:"next_cursor,omitempty"`
	HasMore    bool   `json:"has_more"`
	Limit      int    `json:"limit"`
}

// cursorData holds the decoded fields inside a cursor token.
type cursorData struct {
	UnixMicros int64
	ID         string
}

// ParseCursorPage extracts cursor-based pagination parameters from the request.
// It reads the "cursor" and "limit" query parameters, applying defaults and
// clamping the limit to [1, 100].
func ParseCursorPage(r *http.Request) CursorPage {
	cp := CursorPage{
		Cursor: r.URL.Query().Get("cursor"),
		Limit:  25,
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cp.Limit = n
		}
	}
	if cp.Limit > 100 {
		cp.Limit = 100
	}
	return cp
}

// EncodeCursor produces an opaque cursor string from a creation timestamp and
// an entity ID. The format is base64("unixmicros|id").
func EncodeCursor(createdAt time.Time, id string) string {
	raw := fmt.Sprintf("%d|%s", createdAt.UnixMicro(), id)
	return base64.URLEncoding.EncodeToString([]byte(raw))
}

// DecodeCursor parses a cursor string previously produced by EncodeCursor.
func DecodeCursor(cursor string) (cursorData, error) {
	raw, err := base64.URLEncoding.DecodeString(cursor)
	if err != nil {
		return cursorData{}, fmt.Errorf("web: decode cursor: %w", err)
	}
	parts := strings.SplitN(string(raw), "|", 2)
	if len(parts) != 2 {
		return cursorData{}, fmt.Errorf("web: malformed cursor")
	}
	micros, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return cursorData{}, fmt.Errorf("web: parse cursor timestamp: %w", err)
	}
	return cursorData{
		UnixMicros: micros,
		ID:         parts[1],
	}, nil
}
