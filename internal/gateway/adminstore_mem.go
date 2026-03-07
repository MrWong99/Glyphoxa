package gateway

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sync"
)

// Compile-time interface assertion.
var _ AdminStore = (*MemAdminStore)(nil)

// MemAdminStore is an in-memory implementation of AdminStore.
// Useful for tests and single-process full mode where persistence is not required.
//
// All methods are safe for concurrent use.
type MemAdminStore struct {
	mu      sync.RWMutex
	tenants map[string]Tenant
}

// NewMemAdminStore creates an empty in-memory admin store.
func NewMemAdminStore() *MemAdminStore {
	return &MemAdminStore{
		tenants: make(map[string]Tenant),
	}
}

// CreateTenant adds a new tenant. Returns an error if the ID already exists.
func (s *MemAdminStore) CreateTenant(_ context.Context, t Tenant) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.tenants[t.ID]; exists {
		return fmt.Errorf("gateway: tenant %q already exists", t.ID)
	}
	s.tenants[t.ID] = t
	return nil
}

// GetTenant returns a tenant by ID. Returns an error if not found.
func (s *MemAdminStore) GetTenant(_ context.Context, id string) (Tenant, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	t, ok := s.tenants[id]
	if !ok {
		return Tenant{}, fmt.Errorf("gateway: tenant %q not found", id)
	}
	return t, nil
}

// UpdateTenant replaces the tenant record. Returns an error if not found.
func (s *MemAdminStore) UpdateTenant(_ context.Context, t Tenant) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.tenants[t.ID]; !exists {
		return fmt.Errorf("gateway: tenant %q not found", t.ID)
	}
	s.tenants[t.ID] = t
	return nil
}

// DeleteTenant removes a tenant by ID. Returns an error if not found.
func (s *MemAdminStore) DeleteTenant(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.tenants[id]; !exists {
		return fmt.Errorf("gateway: tenant %q not found", id)
	}
	delete(s.tenants, id)
	return nil
}

// ListTenants returns all tenants sorted by ID.
func (s *MemAdminStore) ListTenants(_ context.Context) ([]Tenant, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := slices.Collect(maps.Values(s.tenants))
	slices.SortFunc(result, func(a, b Tenant) int {
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	return result, nil
}
