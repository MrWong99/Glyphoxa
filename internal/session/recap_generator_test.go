package session_test

import (
	"context"
	"testing"
	"time"

	"github.com/MrWong99/glyphoxa/internal/session"
	"github.com/MrWong99/glyphoxa/pkg/memory"
	memorymock "github.com/MrWong99/glyphoxa/pkg/memory/mock"
	"github.com/MrWong99/glyphoxa/pkg/provider/llm"
	llmmock "github.com/MrWong99/glyphoxa/pkg/provider/llm/mock"
	"github.com/MrWong99/glyphoxa/pkg/provider/tts"
	ttsmock "github.com/MrWong99/glyphoxa/pkg/provider/tts/mock"
)

func TestRecapGenerator_Generate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		entries       []memory.TranscriptEntry
		llmResponse   *llm.CompletionResponse
		llmErr        error
		ttsChunks     [][]byte
		ttsErr        error
		storeErr      error
		wantErr       bool
		wantText      string
		wantSaveCalls int
	}{
		{
			name: "successful generation",
			entries: []memory.TranscriptEntry{
				{SpeakerName: "Player1", Text: "I attack the dragon!", Timestamp: time.Now()},
				{SpeakerName: "Greymantle", Text: "The dragon roars!", NPCID: "npc-0", Timestamp: time.Now()},
			},
			llmResponse:   &llm.CompletionResponse{Content: "Previously, brave heroes faced the dragon..."},
			ttsChunks:     [][]byte{{0x01, 0x02}, {0x03, 0x04}},
			wantText:      "Previously, brave heroes faced the dragon...",
			wantSaveCalls: 1,
		},
		{
			name:    "empty transcript",
			entries: []memory.TranscriptEntry{},
			wantErr: true,
		},
		{
			name: "llm error",
			entries: []memory.TranscriptEntry{
				{SpeakerName: "Player1", Text: "Hello", Timestamp: time.Now()},
			},
			llmErr:  context.DeadlineExceeded,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			sessionStore := &memorymock.SessionStore{
				GetRecentResult: tt.entries,
			}
			recapStore := &memorymock.RecapStore{
				SaveRecapErr: tt.storeErr,
			}
			llmProv := &llmmock.Provider{
				CompleteResponse: tt.llmResponse,
				CompleteErr:      tt.llmErr,
			}
			ttsProv := &ttsmock.Provider{
				SynthesizeChunks: tt.ttsChunks,
				SynthesizeErr:    tt.ttsErr,
			}

			gen := session.NewRecapGenerator(llmProv, ttsProv, recapStore)

			voice := tts.VoiceProfile{ID: "narrator", Provider: "test"}
			recap, err := gen.Generate(context.Background(), "session-1", "campaign-1", sessionStore, voice)

			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if recap.Text != tt.wantText {
				t.Errorf("text = %q, want %q", recap.Text, tt.wantText)
			}
			if recap.SessionID != "session-1" {
				t.Errorf("session_id = %q, want %q", recap.SessionID, "session-1")
			}
			if recapStore.CallCount("SaveRecap") != tt.wantSaveCalls {
				t.Errorf("SaveRecap calls = %d, want %d", recapStore.CallCount("SaveRecap"), tt.wantSaveCalls)
			}
		})
	}
}
