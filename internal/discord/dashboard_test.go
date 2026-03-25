package discord

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
)

// stubSessionData implements SessionData for testing.
type stubSessionData struct {
	active        bool
	sessionID     string
	campaignName  string
	startedAt     time.Time
	npcCount      int
	mutedCount    int
	memoryEntries int
}

func (s *stubSessionData) IsActive() bool       { return s.active }
func (s *stubSessionData) SessionID() string    { return s.sessionID }
func (s *stubSessionData) CampaignName() string { return s.campaignName }
func (s *stubSessionData) StartedAt() time.Time { return s.startedAt }
func (s *stubSessionData) NPCCount() int        { return s.npcCount }
func (s *stubSessionData) MutedNPCCount() int   { return s.mutedCount }
func (s *stubSessionData) MemoryEntries() int   { return s.memoryEntries }

func TestBuildEmbed(t *testing.T) {
	t.Parallel()

	data := &stubSessionData{
		active:       true,
		sessionID:    "session-test-123",
		campaignName: "Lost Mines",
		startedAt:    time.Now().Add(-5 * time.Minute),
		npcCount:     3,
		mutedCount:   1,
	}

	embed := buildEmbed(data, Snapshot{})

	if embed.Title != "Session Dashboard" {
		t.Errorf("Title = %q, want %q", embed.Title, "Session Dashboard")
	}
	if embed.Color != embedColorGreen {
		t.Errorf("Color = %d, want %d", embed.Color, embedColorGreen)
	}
	if embed.Fields[0].Name != "Campaign" || embed.Fields[0].Value != "Lost Mines" {
		t.Errorf("Field[0] = %q:%q, want Campaign:Lost Mines", embed.Fields[0].Name, embed.Fields[0].Value)
	}
	if embed.Fields[1].Name != "Session ID" || embed.Fields[1].Value != "`session-test-123`" {
		t.Errorf("Field[1] = %q:%q, want Session ID:`session-test-123`", embed.Fields[1].Name, embed.Fields[1].Value)
	}
	if embed.Fields[3].Name != "Active NPCs" || embed.Fields[3].Value != "3 (1 muted)" {
		t.Errorf("Field[3] = %q:%q, want Active NPCs:3 (1 muted)", embed.Fields[3].Name, embed.Fields[3].Value)
	}
	if embed.Footer == nil || embed.Footer.Text != "Live session" {
		t.Errorf("Footer = %v, want 'Live session'", embed.Footer)
	}
}

func TestBuildEmbed_NoMuted(t *testing.T) {
	t.Parallel()

	data := &stubSessionData{
		active:       true,
		sessionID:    "session-test-456",
		campaignName: "Dragon Heist",
		startedAt:    time.Now().Add(-10 * time.Minute),
		npcCount:     2,
		mutedCount:   0,
	}

	embed := buildEmbed(data, Snapshot{})

	if embed.Fields[3].Value != "2" {
		t.Errorf("NPC field = %q, want %q (no muted suffix)", embed.Fields[3].Value, "2")
	}
}

func TestBuildEndedEmbed(t *testing.T) {
	t.Parallel()

	data := &stubSessionData{
		active:       false,
		sessionID:    "session-test-789",
		campaignName: "Curse of Strahd",
		startedAt:    time.Now().Add(-1 * time.Hour),
		npcCount:     4,
		mutedCount:   0,
	}

	embed := buildEndedEmbed(data, Snapshot{})

	if embed.Title != "Session Dashboard" {
		t.Errorf("Title = %q, want %q", embed.Title, "Session Dashboard")
	}
	if embed.Color != embedColorRed {
		t.Errorf("Color = %d, want %d", embed.Color, embedColorRed)
	}
	if embed.Description != "Session has ended." {
		t.Errorf("Description = %q, want %q", embed.Description, "Session has ended.")
	}
	if embed.Footer == nil || embed.Footer.Text != "Session ended" {
		t.Errorf("Footer = %v, want 'Session ended'", embed.Footer)
	}
}

func TestDashboard_StartStop(t *testing.T) {
	t.Parallel()

	data := &stubSessionData{
		active:       true,
		sessionID:    "session-lifecycle",
		campaignName: "Test Campaign",
		startedAt:    time.Now(),
		npcCount:     1,
		mutedCount:   0,
	}

	cfg := DashboardConfig{
		Rest:      nil,
		ChannelID: 123,
		Interval:  50 * time.Millisecond,
		GetData:   func() SessionData { return data },
	}

	d := NewDashboard(cfg)

	if d.interval != 50*time.Millisecond {
		t.Errorf("interval = %v, want 50ms", d.interval)
	}
	if d.channelID != 123 {
		t.Errorf("channelID = %v, want 123", d.channelID)
	}

	d2 := NewDashboard(DashboardConfig{
		ChannelID: 456,
		GetData:   func() SessionData { return data },
	})
	if d2.interval != defaultInterval {
		t.Errorf("default interval = %v, want %v", d2.interval, defaultInterval)
	}
}

func TestFormatDuration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		d    time.Duration
		want string
	}{
		{"seconds only", 45 * time.Second, "45s"},
		{"minutes and seconds", 3*time.Minute + 15*time.Second, "3m 15s"},
		{"hours minutes seconds", 2*time.Hour + 30*time.Minute + 5*time.Second, "2h 30m 5s"},
		{"zero", 0, "0s"},
		{"sub-second truncated", 500 * time.Millisecond, "0s"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := formatDuration(tt.d)
			if got != tt.want {
				t.Errorf("formatDuration(%v) = %q, want %q", tt.d, got, tt.want)
			}
		})
	}
}

func TestPipelineStats_Snapshot(t *testing.T) {
	t.Parallel()

	ps := NewPipelineStats(100)
	ps.RecordSTT(50 * time.Millisecond)
	ps.RecordSTT(100 * time.Millisecond)
	ps.RecordLLM(200 * time.Millisecond)
	ps.IncrUtterances()
	ps.IncrUtterances()
	ps.IncrErrors()

	snap := ps.Snapshot()
	if snap.Utterances != 2 {
		t.Errorf("Utterances = %d, want 2", snap.Utterances)
	}
	if snap.Errors != 1 {
		t.Errorf("Errors = %d, want 1", snap.Errors)
	}
	if snap.STT.P50 == 0 {
		t.Error("expected non-zero STT p50")
	}
	if snap.LLM.P50 == 0 {
		t.Error("expected non-zero LLM p50")
	}
}

func TestFormatLatencyField_Empty(t *testing.T) {
	t.Parallel()

	result := formatLatencyField(Snapshot{})
	if result != "" {
		t.Errorf("expected empty string for zero snapshot, got %q", result)
	}
}

func TestFormatLatencyField_AllStages(t *testing.T) {
	t.Parallel()

	snap := Snapshot{
		STT: LatencyPercentiles{P50: 50 * time.Millisecond, P95: 100 * time.Millisecond},
		LLM: LatencyPercentiles{P50: 200 * time.Millisecond, P95: 400 * time.Millisecond},
		TTS: LatencyPercentiles{P50: 80 * time.Millisecond, P95: 150 * time.Millisecond},
		S2S: LatencyPercentiles{P50: 500 * time.Millisecond, P95: 900 * time.Millisecond},
	}

	result := formatLatencyField(snap)
	if result == "" {
		t.Fatal("expected non-empty latency field")
	}
	// Should contain code block markers.
	if !strings.Contains(result, "```") {
		t.Error("expected code block markers in output")
	}
	// Should contain all stages.
	if !strings.Contains(result, "STT:") {
		t.Error("expected STT line in output")
	}
	if !strings.Contains(result, "LLM:") {
		t.Error("expected LLM line in output")
	}
	if !strings.Contains(result, "TTS:") {
		t.Error("expected TTS line in output")
	}
	if !strings.Contains(result, "Total:") {
		t.Error("expected Total line in output")
	}
}

func TestFormatLatencyField_PartialStages(t *testing.T) {
	t.Parallel()

	// Only STT has data.
	snap := Snapshot{
		STT: LatencyPercentiles{P50: 30 * time.Millisecond, P95: 60 * time.Millisecond},
	}

	result := formatLatencyField(snap)
	if result == "" {
		t.Fatal("expected non-empty latency field for partial data")
	}
	if !strings.Contains(result, "STT:") {
		t.Error("expected STT line")
	}
	if strings.Contains(result, "LLM:") {
		t.Error("did not expect LLM line")
	}
	if strings.Contains(result, "TTS:") {
		t.Error("did not expect TTS line")
	}
	if strings.Contains(result, "Total:") {
		t.Error("did not expect Total line")
	}
}

func TestDashboard_Stats(t *testing.T) {
	t.Parallel()

	stats := NewPipelineStats(100)
	data := &stubSessionData{
		active:       true,
		sessionID:    "stats-test",
		campaignName: "Test",
		startedAt:    time.Now(),
	}

	d := NewDashboard(DashboardConfig{
		ChannelID: 123,
		GetData:   func() SessionData { return data },
		Stats:     stats,
	})

	got := d.Stats()
	if got != stats {
		t.Error("Stats() returned unexpected value")
	}
}

func TestDashboard_Stats_Nil(t *testing.T) {
	t.Parallel()

	data := &stubSessionData{
		active:       true,
		sessionID:    "nil-stats",
		campaignName: "Test",
		startedAt:    time.Now(),
	}

	d := NewDashboard(DashboardConfig{
		ChannelID: 456,
		GetData:   func() SessionData { return data },
		Stats:     nil,
	})

	if got := d.Stats(); got != nil {
		t.Errorf("Stats() = %v, want nil", got)
	}
}

// mockMessenger implements dashboardMessenger for tests.
type mockMessenger struct {
	mu            sync.Mutex
	createCalls   int
	updateCalls   int
	lastMessageID snowflake.ID
	createErr     error
	updateErr     error
}

func (m *mockMessenger) CreateMessage(_ snowflake.ID, _ discord.MessageCreate, _ ...rest.RequestOpt) (*discord.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.createCalls++
	if m.createErr != nil {
		return nil, m.createErr
	}
	m.lastMessageID = snowflake.ID(42)
	return &discord.Message{ID: m.lastMessageID}, nil
}

func (m *mockMessenger) UpdateMessage(_ snowflake.ID, _ snowflake.ID, _ discord.MessageUpdate, _ ...rest.RequestOpt) (*discord.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.updateCalls++
	if m.updateErr != nil {
		return nil, m.updateErr
	}
	return &discord.Message{ID: m.lastMessageID}, nil
}

func (m *mockMessenger) getCreateCalls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.createCalls
}

func (m *mockMessenger) getUpdateCalls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.updateCalls
}

func TestDashboard_UpdateCreatesMessage(t *testing.T) {
	t.Parallel()

	data := &stubSessionData{
		active:       true,
		sessionID:    "update-test",
		campaignName: "Test",
		startedAt:    time.Now(),
		npcCount:     1,
	}

	mock := &mockMessenger{}
	d := NewDashboard(DashboardConfig{
		Rest:      mock,
		ChannelID: 100,
		Interval:  time.Hour, // won't tick during test
		GetData:   func() SessionData { return data },
	})

	// First update creates the message.
	d.update(nil)
	if mock.getCreateCalls() != 1 {
		t.Errorf("expected 1 create call, got %d", mock.getCreateCalls())
	}
	if mock.getUpdateCalls() != 0 {
		t.Errorf("expected 0 update calls, got %d", mock.getUpdateCalls())
	}

	// Second update edits the message.
	d.update(nil)
	if mock.getCreateCalls() != 1 {
		t.Errorf("expected 1 create call, got %d", mock.getCreateCalls())
	}
	if mock.getUpdateCalls() != 1 {
		t.Errorf("expected 1 update call, got %d", mock.getUpdateCalls())
	}
}

func TestDashboard_UpdateCreateError(t *testing.T) {
	t.Parallel()

	data := &stubSessionData{
		active:       true,
		sessionID:    "err-test",
		campaignName: "Test",
		startedAt:    time.Now(),
	}

	mock := &mockMessenger{createErr: errors.New("create failed")}
	d := NewDashboard(DashboardConfig{
		Rest:      mock,
		ChannelID: 100,
		Interval:  time.Hour,
		GetData:   func() SessionData { return data },
	})

	// Should not panic on create error.
	d.update(nil)
	if mock.getCreateCalls() != 1 {
		t.Errorf("expected 1 create call, got %d", mock.getCreateCalls())
	}
	// messageID should still be 0, so next update tries to create again.
	if d.messageID != 0 {
		t.Errorf("expected messageID=0 after create error, got %d", d.messageID)
	}
}

func TestDashboard_UpdateWithStats(t *testing.T) {
	t.Parallel()

	data := &stubSessionData{
		active:       true,
		sessionID:    "stats-update",
		campaignName: "Test",
		startedAt:    time.Now(),
	}

	stats := NewPipelineStats(100)
	stats.RecordSTT(50 * time.Millisecond)
	stats.IncrUtterances()

	mock := &mockMessenger{}
	d := NewDashboard(DashboardConfig{
		Rest:      mock,
		ChannelID: 100,
		Interval:  time.Hour,
		GetData:   func() SessionData { return data },
		Stats:     stats,
	})

	// Should use stats in embed.
	d.update(nil)
	if mock.getCreateCalls() != 1 {
		t.Errorf("expected 1 create call, got %d", mock.getCreateCalls())
	}
}

func TestDashboard_StartStop_WithMock(t *testing.T) {
	t.Parallel()

	data := &stubSessionData{
		active:       true,
		sessionID:    "lifecycle",
		campaignName: "Test",
		startedAt:    time.Now(),
		npcCount:     1,
	}

	mock := &mockMessenger{}
	d := NewDashboard(DashboardConfig{
		Rest:      mock,
		ChannelID: 100,
		Interval:  10 * time.Millisecond,
		GetData:   func() SessionData { return data },
	})

	ctx, cancel := context.WithCancel(context.Background())
	d.Start(ctx)

	// Wait for at least one update cycle.
	time.Sleep(50 * time.Millisecond)

	// Stop the dashboard.
	d.Stop(ctx)
	cancel()

	// Verify at least one create was called (the initial update).
	if mock.getCreateCalls() < 1 {
		t.Errorf("expected at least 1 create call, got %d", mock.getCreateCalls())
	}
}

func TestDashboard_PostFinalEmbed(t *testing.T) {
	t.Parallel()

	data := &stubSessionData{
		active:       false,
		sessionID:    "final-embed",
		campaignName: "Test",
		startedAt:    time.Now(),
	}

	mock := &mockMessenger{}
	d := NewDashboard(DashboardConfig{
		Rest:      mock,
		ChannelID: 100,
		Interval:  time.Hour,
		GetData:   func() SessionData { return data },
	})

	// First, create a message via update.
	d.update(nil)
	if d.messageID == 0 {
		t.Fatal("expected messageID to be set after update")
	}

	// Now post final embed (simulates Stop behavior).
	d.postFinalEmbed(nil)
	if mock.getUpdateCalls() != 1 {
		t.Errorf("expected 1 update call for final embed, got %d", mock.getUpdateCalls())
	}
}

func TestDashboard_PostFinalEmbed_NoMessage(t *testing.T) {
	t.Parallel()

	data := &stubSessionData{
		active:    false,
		sessionID: "no-message",
		startedAt: time.Now(),
	}

	mock := &mockMessenger{}
	d := NewDashboard(DashboardConfig{
		Rest:      mock,
		ChannelID: 100,
		Interval:  time.Hour,
		GetData:   func() SessionData { return data },
	})

	// postFinalEmbed with no prior message should be a no-op.
	d.postFinalEmbed(nil)
	if mock.getUpdateCalls() != 0 {
		t.Errorf("expected 0 update calls, got %d", mock.getUpdateCalls())
	}
}

func TestDashboard_StopIdempotent(t *testing.T) {
	t.Parallel()

	data := &stubSessionData{
		active:    true,
		sessionID: "idempotent",
		startedAt: time.Now(),
	}

	mock := &mockMessenger{}
	d := NewDashboard(DashboardConfig{
		Rest:      mock,
		ChannelID: 100,
		Interval:  time.Hour,
		GetData:   func() SessionData { return data },
	})

	// Create initial message.
	d.update(nil)

	ctx := context.Background()
	// Multiple stops should only run once.
	d.Stop(ctx)
	d.Stop(ctx)
	d.Stop(ctx)

	// Should only have 1 update call (from the single postFinalEmbed).
	if mock.getUpdateCalls() != 1 {
		t.Errorf("expected 1 update call (single Stop), got %d", mock.getUpdateCalls())
	}
}

func TestBuildEmbed_WithLatencyData(t *testing.T) {
	t.Parallel()

	data := &stubSessionData{
		active:        true,
		sessionID:     "latency-test",
		campaignName:  "Latency Campaign",
		startedAt:     time.Now().Add(-2 * time.Minute),
		npcCount:      2,
		mutedCount:    0,
		memoryEntries: 5,
	}

	snap := Snapshot{
		STT:        LatencyPercentiles{P50: 40 * time.Millisecond, P95: 80 * time.Millisecond},
		LLM:        LatencyPercentiles{P50: 150 * time.Millisecond, P95: 300 * time.Millisecond},
		Utterances: 10,
		Errors:     2,
	}

	embed := buildEmbed(data, snap)

	// Check that utterances and errors fields are present.
	found := false
	for _, f := range embed.Fields {
		if f.Name == "Utterances" && f.Value == "10" {
			found = true
		}
	}
	if !found {
		t.Error("expected Utterances field with value '10'")
	}

	// Check memory entries field.
	memFound := false
	for _, f := range embed.Fields {
		if f.Name == "Memory Entries" && f.Value == "5" {
			memFound = true
		}
	}
	if !memFound {
		t.Error("expected Memory Entries field with value '5'")
	}

	// Check that pipeline latency field is present (STT and LLM have data).
	latencyFound := false
	for _, f := range embed.Fields {
		if f.Name == "Pipeline Latency" {
			latencyFound = true
		}
	}
	if !latencyFound {
		t.Error("expected Pipeline Latency field when latency data is present")
	}
}

func TestFormatMs(t *testing.T) {
	t.Parallel()

	got := formatMs(150 * time.Millisecond)
	if got != "150.0ms" {
		t.Errorf("formatMs(150ms) = %q, want %q", got, "150.0ms")
	}
}
