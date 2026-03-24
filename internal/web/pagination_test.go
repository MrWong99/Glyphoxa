package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestEncodeCursor_RoundTrip(t *testing.T) {
	t.Parallel()

	ts := time.Date(2025, 6, 15, 12, 30, 0, 0, time.UTC)
	id := "entity-123"

	cursor := EncodeCursor(ts, id)
	if cursor == "" {
		t.Fatal("EncodeCursor returned empty string")
	}

	cd, err := DecodeCursor(cursor)
	if err != nil {
		t.Fatalf("DecodeCursor: %v", err)
	}
	if cd.ID != id {
		t.Errorf("ID = %q, want %q", cd.ID, id)
	}

	got := time.UnixMicro(cd.UnixMicros).UTC()
	if !got.Equal(ts) {
		t.Errorf("timestamp = %v, want %v", got, ts)
	}
}

func TestDecodeCursor_Invalid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		cursor string
	}{
		{"not base64", "!!!invalid!!!"},
		{"missing pipe", "bm9waXBl"},      // base64("nopipe")
		{"bad timestamp", "YWJjfGRlZg=="}, // base64("abc|def")
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := DecodeCursor(tt.cursor)
			if err == nil {
				t.Error("expected error, got nil")
			}
		})
	}
}

func TestParseCursorPage_Defaults(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/campaigns", nil)
	page := ParseCursorPage(req)

	if page.Limit != 25 {
		t.Errorf("limit = %d, want 25", page.Limit)
	}
	if page.Cursor != "" {
		t.Errorf("cursor = %q, want empty", page.Cursor)
	}
}

func TestParseCursorPage_CustomLimit(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/campaigns?limit=50", nil)
	page := ParseCursorPage(req)

	if page.Limit != 50 {
		t.Errorf("limit = %d, want 50", page.Limit)
	}
}

func TestParseCursorPage_LimitClamped(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/campaigns?limit=200", nil)
	page := ParseCursorPage(req)

	if page.Limit != 100 {
		t.Errorf("limit = %d, want 100 (clamped)", page.Limit)
	}
}

func TestParseCursorPage_WithCursor(t *testing.T) {
	t.Parallel()

	cursor := EncodeCursor(time.Now(), "test-id")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/campaigns?cursor="+cursor+"&limit=10", nil)
	page := ParseCursorPage(req)

	if page.Cursor != cursor {
		t.Errorf("cursor = %q, want %q", page.Cursor, cursor)
	}
	if page.Limit != 10 {
		t.Errorf("limit = %d, want 10", page.Limit)
	}
}

func TestParseCursorPage_InvalidLimit(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/campaigns?limit=abc", nil)
	page := ParseCursorPage(req)

	if page.Limit != 25 {
		t.Errorf("limit = %d, want 25 (default for invalid input)", page.Limit)
	}
}
