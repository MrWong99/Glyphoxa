package rpc

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/auth"
)

// requireTenant resolves the operator's Tenant the auth interceptor stack put
// in the context (ADR-0039 thin pass-through). Behind the stack it is always
// present; a missing Tenant is treated as unauthenticated. Every handler that
// needs the Tenant goes through here, so the refusal is one shape everywhere.
func requireTenant(ctx context.Context) (uuid.UUID, error) {
	id, ok := auth.TenantID(ctx)
	if !ok {
		return uuid.Nil, connect.NewError(connect.CodeUnauthenticated, errors.New("no tenant in context"))
	}
	return id, nil
}
