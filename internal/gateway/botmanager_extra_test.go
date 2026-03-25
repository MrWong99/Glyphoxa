package gateway

import "testing"

func TestIsBotConnected(t *testing.T) {
	t.Parallel()

	bm := NewBotManager()
	_ = bm.AddBot("tenant-1", nil, []string{"guild-a", "guild-b"})
	_ = bm.AddBot("tenant-2", nil, nil) // no guild allowlist

	t.Run("connected with guilds", func(t *testing.T) {
		t.Parallel()
		connected, count := bm.IsBotConnected("tenant-1")
		if !connected {
			t.Error("expected connected to be true")
		}
		if count != 2 {
			t.Errorf("got guild count %d, want 2", count)
		}
	})

	t.Run("connected no guilds", func(t *testing.T) {
		t.Parallel()
		connected, count := bm.IsBotConnected("tenant-2")
		if !connected {
			t.Error("expected connected to be true")
		}
		if count != 0 {
			t.Errorf("got guild count %d, want 0", count)
		}
	})

	t.Run("not connected", func(t *testing.T) {
		t.Parallel()
		connected, count := bm.IsBotConnected("nonexistent")
		if connected {
			t.Error("expected connected to be false")
		}
		if count != 0 {
			t.Errorf("got guild count %d, want 0", count)
		}
	})
}

func TestGetBot(t *testing.T) {
	t.Parallel()

	bm := NewBotManager()

	// AddBot without a GatewayBot.
	_ = bm.AddBot("tenant-1", nil, nil)

	t.Run("no gateway bot", func(t *testing.T) {
		t.Parallel()
		_, ok := bm.GetBot("tenant-1")
		if ok {
			t.Error("expected GetBot to return false when no GatewayBot is set")
		}
	})

	t.Run("not found", func(t *testing.T) {
		t.Parallel()
		_, ok := bm.GetBot("nonexistent")
		if ok {
			t.Error("expected GetBot to return false for missing tenant")
		}
	})
}

func TestRemoveBot_NilClient(t *testing.T) {
	t.Parallel()

	bm := NewBotManager()
	_ = bm.AddBot("tenant-1", nil, nil)

	// RemoveBot on an entry with nil client should succeed without panic.
	err := bm.RemoveBot("tenant-1")
	if err != nil {
		t.Errorf("RemoveBot failed: %v", err)
	}

	if _, ok := bm.Get("tenant-1"); ok {
		t.Error("tenant-1 should be removed")
	}
}

func TestAddBot_DuplicateGuildIDs(t *testing.T) {
	t.Parallel()

	bm := NewBotManager()
	err := bm.AddBot("tenant-1", nil, []string{"guild-a", "guild-a", "guild-b"})
	if err != nil {
		t.Fatalf("AddBot failed: %v", err)
	}

	// Even with duplicate guild IDs, the map should deduplicate them.
	connected, count := bm.IsBotConnected("tenant-1")
	if !connected {
		t.Error("expected connected")
	}
	if count != 2 {
		t.Errorf("got guild count %d, want 2 (deduped)", count)
	}
}
