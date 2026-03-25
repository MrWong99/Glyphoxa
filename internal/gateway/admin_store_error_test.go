package gateway_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/MrWong99/glyphoxa/internal/gateway"
)

// failingAdminStore always returns errors for specified operations.
type failingAdminStore struct {
	gateway.MemAdminStore // embed for methods we don't override
	listErr               error
	updateErr             error
}

func newFailingStore() *failingAdminStore {
	return &failingAdminStore{}
}

func (s *failingAdminStore) ListTenants(_ context.Context) ([]gateway.Tenant, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return nil, nil
}

func (s *failingAdminStore) CreateTenant(_ context.Context, t gateway.Tenant) error {
	return nil
}

func (s *failingAdminStore) GetTenant(_ context.Context, id string) (gateway.Tenant, error) {
	return gateway.Tenant{ID: id}, nil
}

func (s *failingAdminStore) UpdateTenant(_ context.Context, _ gateway.Tenant) error {
	if s.updateErr != nil {
		return s.updateErr
	}
	return nil
}

func (s *failingAdminStore) DeleteTenant(_ context.Context, _ string) error {
	return nil
}

func TestAdminAPI_ListTenants_StoreError(t *testing.T) {
	t.Parallel()

	store := newFailingStore()
	store.listErr = fmt.Errorf("database unavailable")
	api := gateway.NewAdminAPI(store, testAPIKey, nil)
	handler := api.Handler()

	rr := doRequest(t, handler, "GET", "/api/v1/tenants", nil)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("got status %d, want %d", rr.Code, http.StatusInternalServerError)
	}
}

func TestAdminAPI_UpdateTenant_StoreError(t *testing.T) {
	t.Parallel()

	store := newFailingStore()
	store.updateErr = fmt.Errorf("write conflict")
	api := gateway.NewAdminAPI(store, testAPIKey, nil)
	handler := api.Handler()

	rr := doRequest(t, handler, "PUT", "/api/v1/tenants/anyid",
		gateway.TenantUpdateRequest{LicenseTier: "shared"})
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("got status %d, want %d", rr.Code, http.StatusInternalServerError)
	}
}
