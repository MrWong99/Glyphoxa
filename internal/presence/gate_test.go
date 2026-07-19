package presence

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

const (
	testGuild  = "472093001100"
	otherGuild = "999999999999"
	operatorID = "111111111111"
	strangerID = "222222222222"
)

var (
	tenantA = uuid.MustParse("aaaaaaaa-0000-0000-0000-000000000000")
	tenantB = uuid.MustParse("bbbbbbbb-0000-0000-0000-000000000000")
)

// fakeTenants is a scripted TenantResolver: a Guild→Tenant map; an unlisted Guild
// resolves to storage.ErrNotFound (the unknown-Guild reject).
type fakeTenants map[string]uuid.UUID

func (f fakeTenants) TenantForGuild(_ context.Context, guildID string) (uuid.UUID, error) {
	if id, ok := f[guildID]; ok {
		return id, nil
	}
	return uuid.Nil, storage.ErrNotFound
}

// gmInTenants is a scripted per-Tenant GMChecker: a (tenantID,userID) is GM when
// listed.
type gmInTenants map[uuid.UUID]map[string]struct{}

func (g gmInTenants) IsGMInTenant(tenantID uuid.UUID, discordUserID string) bool {
	_, ok := g[tenantID][discordUserID]
	return ok
}

// gmIn builds a per-Tenant GMChecker granting userID GM standing in tenantID.
func gmIn(tenantID uuid.UUID, userIDs ...string) gmInTenants {
	m := gmInTenants{tenantID: {}}
	for _, id := range userIDs {
		m[tenantID][id] = struct{}{}
	}
	return m
}

func TestGateAuthorizeGuild(t *testing.T) {
	g := NewGate(gmInTenants{}, fakeTenants{testGuild: tenantA})
	ctx := context.Background()

	// A non-GM command from a known Guild passes and returns its Tenant.
	got, err := g.Authorize(ctx, testGuild, operatorID, false)
	if err != nil {
		t.Fatalf("Authorize(known guild) = %v, want nil", err)
	}
	if got != tenantA {
		t.Errorf("Authorize returned tenant %s, want %s", got, tenantA)
	}

	// An unknown Guild is denied ErrWrongGuild.
	if _, err := g.Authorize(ctx, otherGuild, operatorID, false); !errors.Is(err, ErrWrongGuild) {
		t.Errorf("Authorize(unknown guild) = %v, want ErrWrongGuild", err)
	}

	// A DM (empty Guild) is denied ErrWrongGuild — before any resolver call.
	if _, err := g.Authorize(ctx, "", operatorID, false); !errors.Is(err, ErrWrongGuild) {
		t.Errorf("Authorize(DM) = %v, want ErrWrongGuild", err)
	}
}

func TestGateAuthorizeGM(t *testing.T) {
	g := NewGate(gmIn(tenantA, operatorID), fakeTenants{testGuild: tenantA})
	ctx := context.Background()

	// GM of the resolved Tenant passes a GM-only command.
	if _, err := g.Authorize(ctx, testGuild, operatorID, true); err != nil {
		t.Errorf("Authorize(GM, gmOnly) = %v, want nil", err)
	}
	// A stranger in the same Guild is denied ErrNotOperator.
	if _, err := g.Authorize(ctx, testGuild, strangerID, true); !errors.Is(err, ErrNotOperator) {
		t.Errorf("Authorize(stranger, gmOnly) = %v, want ErrNotOperator", err)
	}
	// Unknown Guild fails on the Guild resolution before the GM check.
	if _, err := g.Authorize(ctx, otherGuild, operatorID, true); !errors.Is(err, ErrWrongGuild) {
		t.Errorf("Authorize(GM, unknown guild) = %v, want ErrWrongGuild", err)
	}
}

// TestGateAuthorizeCrossTenantEscalation is the escalation regression (#490 AC):
// tenant A's operator invoking a GM-only command in tenant B's Guild resolves to
// tenant B, where it is NOT a GM — so it is denied ErrNotOperator, never granted
// tenant A's standing in tenant B.
func TestGateAuthorizeCrossTenantEscalation(t *testing.T) {
	// operatorID is GM in tenant A only. guildB routes to tenant B.
	g := NewGate(gmIn(tenantA, operatorID), fakeTenants{testGuild: tenantA, otherGuild: tenantB})
	ctx := context.Background()

	if _, err := g.Authorize(ctx, otherGuild, operatorID, true); !errors.Is(err, ErrNotOperator) {
		t.Fatalf("tenant-A operator in tenant-B guild = %v, want ErrNotOperator (no cross-tenant escalation)", err)
	}
	// Sanity: the same operator IS GM in its own Guild.
	if _, err := g.Authorize(ctx, testGuild, operatorID, true); err != nil {
		t.Errorf("tenant-A operator in tenant-A guild = %v, want nil", err)
	}
}
