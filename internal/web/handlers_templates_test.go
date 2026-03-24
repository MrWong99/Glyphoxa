package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandleListNPCTemplates_All(t *testing.T) {
	t.Parallel()

	srv, _, _, secret := testServerWithStores(t)
	srv.registerRoutes()

	req := authReq(t, http.MethodGet, "/api/v1/npc-templates", nil, secret, "user-1", "tenant-1", "viewer")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	var resp struct {
		Data []NPCTemplate `json:"data"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Data) != len(builtinTemplates) {
		t.Errorf("got %d templates, want %d", len(resp.Data), len(builtinTemplates))
	}
}

func TestHandleListNPCTemplates_FilterBySystem(t *testing.T) {
	t.Parallel()

	srv, _, _, secret := testServerWithStores(t)
	srv.registerRoutes()

	req := authReq(t, http.MethodGet, "/api/v1/npc-templates?system=D%26D+5e", nil, secret, "user-1", "tenant-1", "viewer")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	var resp struct {
		Data []NPCTemplate `json:"data"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, tmpl := range resp.Data {
		if tmpl.System != "D&D 5e" {
			t.Errorf("template %q has system %q, want %q", tmpl.ID, tmpl.System, "D&D 5e")
		}
	}
}

func TestHandleListNPCTemplates_FilterByCategory(t *testing.T) {
	t.Parallel()

	srv, _, _, secret := testServerWithStores(t)
	srv.registerRoutes()

	req := authReq(t, http.MethodGet, "/api/v1/npc-templates?category=merchant", nil, secret, "user-1", "tenant-1", "viewer")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	var resp struct {
		Data []NPCTemplate `json:"data"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Data) != 2 {
		t.Errorf("got %d merchant templates, want 2", len(resp.Data))
	}
	for _, tmpl := range resp.Data {
		if tmpl.Category != "merchant" {
			t.Errorf("template %q has category %q, want %q", tmpl.ID, tmpl.Category, "merchant")
		}
	}
}

func TestHandleListNPCTemplates_EmptyForUnknown(t *testing.T) {
	t.Parallel()

	srv, _, _, secret := testServerWithStores(t)
	srv.registerRoutes()

	req := authReq(t, http.MethodGet, "/api/v1/npc-templates?system=Shadowrun", nil, secret, "user-1", "tenant-1", "viewer")
	rr := httptest.NewRecorder()
	srv.mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	var resp struct {
		Data []NPCTemplate `json:"data"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Data == nil {
		t.Error("data should be empty array, not null")
	}
	if len(resp.Data) != 0 {
		t.Errorf("got %d templates, want 0", len(resp.Data))
	}
}
