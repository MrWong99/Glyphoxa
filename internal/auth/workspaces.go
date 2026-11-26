package auth

import (
	"context"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// WorkspaceStore is the persistence behind [Workspaces]. *storage.Store
// satisfies it.
type WorkspaceStore interface {
	ResolveWorkspace(ctx context.Context, userID uuid.UUID, sessionToken string, ownTenantNeedsGrant bool) (storage.Workspace, error)
	OwnTenantForUser(ctx context.Context, userID uuid.UUID, needsGrant bool) (uuid.UUID, error)
}

// Workspaces is the production [WorkspaceResolver] (ADR-0065): the store's
// one-query resolution, told the one thing the database cannot know — whether
// this user may act in a Tenant they operate without an Access Grant.
//
// That question only arises because Campaign Guests are admitted on every
// deployment. On an `allowlist` deployment, operating a Tenant is something a
// Platform Admin grants and revokes; before guests existed the OAuth callback
// alone enforced that, because someone whose grant was revoked could not sign
// in at all. Now they can — as a guest of somebody else's Campaign — and a
// session that then fell back to "their own Tenant" would undo the revocation.
// So outside `open` mode the own-Tenant workspace requires a Platform Admin or
// a live Access Grant on every request, exactly as the callback does at
// sign-in.
//
// The zero value needs a grant from everyone and has no store: it must not be
// used unwired.
type Workspaces struct {
	Store  WorkspaceStore
	Admins PlatformAdmins
	Mode   AdmissionMode
}

// ownTenantNeedsGrant reports whether u must hold an Access Grant to act in a
// Tenant they operate. Anything but `open` is treated as `allowlist`, so an
// unset mode fails closed.
func (w Workspaces) ownTenantNeedsGrant(u storage.User) bool {
	return w.Mode != AdmissionOpen && !w.Admins.Is(u)
}

// ResolveWorkspace implements [WorkspaceResolver].
func (w Workspaces) ResolveWorkspace(ctx context.Context, u storage.User, sessionToken string) (storage.Workspace, error) {
	return w.Store.ResolveWorkspace(ctx, u.ID, sessionToken, w.ownTenantNeedsGrant(u))
}

// OwnTenant returns the Tenant u operates and may act in, or
// storage.ErrNotFound — the same rule [Workspaces.ResolveWorkspace] applies,
// so "has a workspace of their own" means the same thing everywhere.
func (w Workspaces) OwnTenant(ctx context.Context, u storage.User) (uuid.UUID, error) {
	return w.Store.OwnTenantForUser(ctx, u.ID, w.ownTenantNeedsGrant(u))
}
