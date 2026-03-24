package gateway

import "sync"

// SessionControllerRegistry maps tenant IDs to their SessionController.
// It is populated by the per-tenant command setup callback and used by the
// ManagementServer to dispatch session start/stop from the web UI.
//
// All methods are safe for concurrent use.
type SessionControllerRegistry struct {
	mu    sync.RWMutex
	ctrls map[string]SessionController
}

// NewSessionControllerRegistry creates a ready-to-use registry.
func NewSessionControllerRegistry() *SessionControllerRegistry {
	return &SessionControllerRegistry{ctrls: make(map[string]SessionController)}
}

// Register adds a session controller for the given tenant, replacing any
// existing entry (e.g., when a bot reconnects).
func (r *SessionControllerRegistry) Register(tenantID string, ctrl SessionController) {
	r.mu.Lock()
	r.ctrls[tenantID] = ctrl
	r.mu.Unlock()
}

// Lookup returns the session controller for the given tenant.
func (r *SessionControllerRegistry) Lookup(tenantID string) (SessionController, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ctrl, ok := r.ctrls[tenantID]
	return ctrl, ok
}

// Remove removes the entry for the given tenant.
func (r *SessionControllerRegistry) Remove(tenantID string) {
	r.mu.Lock()
	delete(r.ctrls, tenantID)
	r.mu.Unlock()
}
