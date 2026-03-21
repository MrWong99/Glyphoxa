package gateway

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/disgoorg/disgo/bot"
)

func TestAddBot(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		setup     func(*BotManager)
		tenantID  string
		wantErr   bool
		errSubstr string
	}{
		{
			name:     "success",
			setup:    func(_ *BotManager) {},
			tenantID: "tenant-1",
			wantErr:  false,
		},
		{
			name: "duplicate error",
			setup: func(bm *BotManager) {
				_ = bm.AddBot("tenant-1", nil, nil)
			},
			tenantID:  "tenant-1",
			wantErr:   true,
			errSubstr: "already registered",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			bm := NewBotManager()
			tt.setup(bm)

			err := bm.AddBot(tt.tenantID, nil, nil)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if tt.errSubstr != "" && !containsSubstr(err.Error(), tt.errSubstr) {
					t.Errorf("error %q does not contain %q", err, tt.errSubstr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestRemoveBot(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		setup     func(*BotManager)
		tenantID  string
		wantErr   bool
		errSubstr string
	}{
		{
			name: "success",
			setup: func(bm *BotManager) {
				_ = bm.AddBot("tenant-1", nil, nil)
			},
			tenantID: "tenant-1",
			wantErr:  false,
		},
		{
			name:      "not found",
			setup:     func(_ *BotManager) {},
			tenantID:  "nonexistent",
			wantErr:   true,
			errSubstr: "no bot registered",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			bm := NewBotManager()
			tt.setup(bm)

			err := bm.RemoveBot(tt.tenantID)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if tt.errSubstr != "" && !containsSubstr(err.Error(), tt.errSubstr) {
					t.Errorf("error %q does not contain %q", err, tt.errSubstr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			// Verify the bot was actually removed.
			if _, ok := bm.Get(tt.tenantID); ok {
				t.Error("bot still present after RemoveBot")
			}
		})
	}
}

func TestGet(t *testing.T) {
	t.Parallel()

	bm := NewBotManager()
	_ = bm.AddBot("tenant-1", nil, nil)

	t.Run("found", func(t *testing.T) {
		t.Parallel()
		client, ok := bm.Get("tenant-1")
		if !ok {
			t.Fatal("expected bot to be found")
		}
		// We stored nil, so client should be nil.
		if client != nil {
			t.Error("expected nil client")
		}
	})

	t.Run("not found", func(t *testing.T) {
		t.Parallel()
		_, ok := bm.Get("nonexistent")
		if ok {
			t.Error("expected bot to not be found")
		}
	})
}

func TestRouteEvent(t *testing.T) {
	t.Parallel()

	t.Run("found", func(t *testing.T) {
		t.Parallel()

		bm := NewBotManager()
		_ = bm.AddBot("tenant-1", nil, nil)

		called := false
		bm.RouteEvent("tenant-1", func(c *bot.Client) {
			called = true
			// We stored nil, so c should be nil.
			if c != nil {
				t.Error("expected nil client in handler")
			}
		})

		if !called {
			t.Error("handler was not called")
		}
	})

	t.Run("not found", func(t *testing.T) {
		t.Parallel()

		bm := NewBotManager()

		called := false
		bm.RouteEvent("nonexistent", func(_ *bot.Client) {
			called = true
		})

		if called {
			t.Error("handler should not have been called for missing tenant")
		}
	})
}

func TestRouteEventForGuild(t *testing.T) {
	t.Parallel()

	t.Run("allowed guild", func(t *testing.T) {
		t.Parallel()

		bm := NewBotManager()
		_ = bm.AddBot("tenant-1", nil, []string{"guild-a", "guild-b"})

		called := false
		bm.RouteEventForGuild("tenant-1", "guild-a", func(_ *bot.Client) {
			called = true
		})

		if !called {
			t.Error("handler was not called for allowed guild")
		}
	})

	t.Run("blocked guild", func(t *testing.T) {
		t.Parallel()

		bm := NewBotManager()
		_ = bm.AddBot("tenant-1", nil, []string{"guild-a", "guild-b"})

		called := false
		bm.RouteEventForGuild("tenant-1", "guild-c", func(_ *bot.Client) {
			called = true
		})

		if called {
			t.Error("handler should not have been called for blocked guild")
		}
	})

	t.Run("empty allowlist allows all", func(t *testing.T) {
		t.Parallel()

		bm := NewBotManager()
		_ = bm.AddBot("tenant-1", nil, nil)

		called := false
		bm.RouteEventForGuild("tenant-1", "any-guild", func(_ *bot.Client) {
			called = true
		})

		if !called {
			t.Error("handler was not called — empty allowlist should allow all guilds")
		}
	})

	t.Run("not found tenant", func(t *testing.T) {
		t.Parallel()

		bm := NewBotManager()

		called := false
		bm.RouteEventForGuild("nonexistent", "guild-a", func(_ *bot.Client) {
			called = true
		})

		if called {
			t.Error("handler should not have been called for missing tenant")
		}
	})
}

func TestClose(t *testing.T) {
	t.Parallel()

	bm := NewBotManager()
	_ = bm.AddBot("tenant-1", nil, nil)
	_ = bm.AddBot("tenant-2", nil, nil)

	bm.Close()

	// After Close, all bots should be gone.
	if _, ok := bm.Get("tenant-1"); ok {
		t.Error("tenant-1 still present after Close")
	}
	if _, ok := bm.Get("tenant-2"); ok {
		t.Error("tenant-2 still present after Close")
	}
}

func TestConcurrentAccess(t *testing.T) {
	t.Parallel()

	bm := NewBotManager()

	const numTenants = 50
	const numOps = 100

	// Pre-add half the tenants so RouteEvent and RemoveBot have targets.
	for i := range numTenants / 2 {
		tid := tenantID(i)
		_ = bm.AddBot(tid, nil, nil)
	}

	var wg sync.WaitGroup
	var routeCount atomic.Int64

	// Concurrent AddBot for the other half.
	for i := numTenants / 2; i < numTenants; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			_ = bm.AddBot(tenantID(id), nil, nil)
		}(i)
	}

	// Concurrent RouteEvent.
	for range numOps {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			bm.RouteEvent(tenantID(id%numTenants), func(_ *bot.Client) {
				routeCount.Add(1)
			})
		}(0)
	}

	// Concurrent Get.
	for i := range numOps {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			bm.Get(tenantID(id % numTenants))
		}(i)
	}

	wg.Wait()

	if routeCount.Load() == 0 {
		t.Error("expected at least one RouteEvent handler to execute")
	}
}

// tenantID returns a deterministic tenant ID string for index i.
func tenantID(i int) string {
	return "tenant-" + itoa(i)
}

// itoa is a simple int-to-string without importing strconv.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	neg := i < 0
	if neg {
		i = -i
	}
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

// containsSubstr reports whether s contains substr.
func containsSubstr(s, substr string) bool {
	return len(s) >= len(substr) && searchSubstr(s, substr)
}

func searchSubstr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
