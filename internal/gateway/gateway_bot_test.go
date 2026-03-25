package gateway

import (
	"testing"

	"github.com/disgoorg/snowflake/v2"
)

func TestGatewayBot_Accessors(t *testing.T) {
	t.Parallel()

	guildIDs := []snowflake.ID{snowflake.ID(123), snowflake.ID(456)}
	gwBot := NewGatewayBot(nil, nil, nil, "tenant-42", guildIDs)

	t.Run("TenantID", func(t *testing.T) {
		t.Parallel()
		if got := gwBot.TenantID(); got != "tenant-42" {
			t.Errorf("TenantID() = %q, want %q", got, "tenant-42")
		}
	})

	t.Run("Client", func(t *testing.T) {
		t.Parallel()
		if got := gwBot.Client(); got != nil {
			t.Errorf("Client() = %v, want nil", got)
		}
	})

	t.Run("Router", func(t *testing.T) {
		t.Parallel()
		if got := gwBot.Router(); got != nil {
			t.Errorf("Router() = %v, want nil", got)
		}
	})

	t.Run("Permissions", func(t *testing.T) {
		t.Parallel()
		if got := gwBot.Permissions(); got != nil {
			t.Errorf("Permissions() = %v, want nil", got)
		}
	})

	t.Run("GuildIDList", func(t *testing.T) {
		t.Parallel()
		ids := gwBot.GuildIDList()
		if len(ids) != 2 {
			t.Fatalf("GuildIDList() len = %d, want 2", len(ids))
		}
		if ids[0] != snowflake.ID(123) {
			t.Errorf("GuildIDList()[0] = %v, want 123", ids[0])
		}
	})
}

func TestGatewayBot_SuspendGateway_NilClient(t *testing.T) {
	t.Parallel()

	gwBot := NewGatewayBot(nil, nil, nil, "tenant-1", nil)
	// Should not panic with nil client.
	gwBot.SuspendGateway(t.Context())
}

func TestGatewayBot_ResumeGateway_NilClient(t *testing.T) {
	t.Parallel()

	gwBot := NewGatewayBot(nil, nil, nil, "tenant-1", nil)
	// Should return nil with nil client.
	if err := gwBot.ResumeGateway(t.Context()); err != nil {
		t.Errorf("ResumeGateway() = %v, want nil", err)
	}
}
