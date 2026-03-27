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

// ForEach calls fn for every registered controller. The callback receives a
// snapshot of the registry entries so it is safe to call other registry methods
// inside fn. If fn returns false, iteration stops.
func (r *SessionControllerRegistry) ForEach(fn func(tenantID string, ctrl SessionController) bool) {
	r.mu.RLock()
	// Snapshot to avoid holding the lock during callbacks.
	snapshot := make(map[string]SessionController, len(r.ctrls))
	for k, v := range r.ctrls {
		snapshot[k] = v
	}
	r.mu.RUnlock()

	for k, v := range snapshot {
		if !fn(k, v) {
			return
		}
	}
}
