package gateway

import (
	"io"
	"testing"

	"github.com/MrWong99/glyphoxa/internal/gateway/audiobridge"
	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/snowflake/v2"
)

func TestVoiceBridgeReceiver_ReceiveOpusFrame(t *testing.T) {
	t.Parallel()

	srv := audiobridge.NewServer()

	t.Run("nil packet", func(t *testing.T) {
		t.Parallel()
		bridge := srv.NewSessionBridge("sess-nil-pkt")
		defer srv.RemoveBridge("sess-nil-pkt")

		receiver := &voiceBridgeReceiver{
			bridge:    bridge,
			sessionID: "sess-nil-pkt",
			done:      make(chan struct{}),
		}
		err := receiver.ReceiveOpusFrame(snowflake.ID(123), nil)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("empty opus data", func(t *testing.T) {
		t.Parallel()
		bridge := srv.NewSessionBridge("sess-empty")
		defer srv.RemoveBridge("sess-empty")

		receiver := &voiceBridgeReceiver{
			bridge:    bridge,
			sessionID: "sess-empty",
			done:      make(chan struct{}),
		}
		err := receiver.ReceiveOpusFrame(snowflake.ID(123), &voice.Packet{Opus: nil})
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("valid frame", func(t *testing.T) {
		t.Parallel()
		bridge := srv.NewSessionBridge("sess-recv-valid")
		defer srv.RemoveBridge("sess-recv-valid")

		r := &voiceBridgeReceiver{
			bridge:    bridge,
			sessionID: "sess-recv-valid",
			done:      make(chan struct{}),
		}

		err := r.ReceiveOpusFrame(snowflake.ID(456), &voice.Packet{
			Opus: []byte{0x01, 0x02, 0x03},
			SSRC: 12345,
		})
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})
}

func TestVoiceBridgeReceiver_CleanupUser(t *testing.T) {
	t.Parallel()

	receiver := &voiceBridgeReceiver{
		sessionID: "sess-cleanup",
		done:      make(chan struct{}),
	}
	// Should not panic.
	receiver.CleanupUser(snowflake.ID(789))
}

func TestVoiceBridgeReceiver_Close(t *testing.T) {
	t.Parallel()

	receiver := &voiceBridgeReceiver{
		sessionID: "sess-close",
		done:      make(chan struct{}),
	}

	receiver.Close()
	// Second close should not panic (once semantics).
	receiver.Close()
}

func TestVoiceBridgeProvider_ProvideOpusFrame_NoData(t *testing.T) {
	t.Parallel()

	srv := audiobridge.NewServer()
	bridge := srv.NewSessionBridge("sess-prov")
	defer srv.RemoveBridge("sess-prov")

	provider := &voiceBridgeProvider{
		bridge:    bridge,
		sessionID: "sess-prov",
		done:      make(chan struct{}),
	}

	// With no frames available, should return nil (silence).
	data, err := provider.ProvideOpusFrame()
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if data != nil {
		t.Errorf("expected nil data for silence, got %v", data)
	}
}

func TestVoiceBridgeProvider_ProvideOpusFrame_AfterClose(t *testing.T) {
	t.Parallel()

	srv := audiobridge.NewServer()
	bridge := srv.NewSessionBridge("sess-prov-eof")
	defer srv.RemoveBridge("sess-prov-eof")

	provider := &voiceBridgeProvider{
		bridge:    bridge,
		sessionID: "sess-prov-eof",
		done:      make(chan struct{}),
	}

	provider.Close()
	data, err := provider.ProvideOpusFrame()
	if err != io.EOF {
		t.Errorf("expected io.EOF after close, got %v", err)
	}
	if data != nil {
		t.Errorf("expected nil data after close, got %v", data)
	}
}

func TestVoiceBridgeProvider_Close(t *testing.T) {
	t.Parallel()

	srv := audiobridge.NewServer()
	bridge := srv.NewSessionBridge("sess-prov-close")
	defer srv.RemoveBridge("sess-prov-close")

	provider := &voiceBridgeProvider{
		bridge:    bridge,
		sessionID: "sess-prov-close",
		done:      make(chan struct{}),
	}

	provider.Close()
	// Second close should not panic (once semantics).
	provider.Close()
}

func TestVoiceBridgeReceiver_FrameCount(t *testing.T) {
	t.Parallel()

	srv := audiobridge.NewServer()
	bridge := srv.NewSessionBridge("sess-count")
	defer srv.RemoveBridge("sess-count")

	receiver := &voiceBridgeReceiver{
		bridge:    bridge,
		sessionID: "sess-count",
		done:      make(chan struct{}),
	}

	for i := range 5 {
		_ = receiver.ReceiveOpusFrame(snowflake.ID(100), &voice.Packet{
			Opus: []byte{byte(i)},
			SSRC: 1,
		})
	}

	if receiver.frameCount != 5 {
		t.Errorf("frameCount = %d, want 5", receiver.frameCount)
	}
}

func TestVoiceBridgeProvider_GotFirst(t *testing.T) {
	t.Parallel()

	srv := audiobridge.NewServer()
	bridge := srv.NewSessionBridge("sess-first")
	defer srv.RemoveBridge("sess-first")

	provider := &voiceBridgeProvider{
		bridge:    bridge,
		sessionID: "sess-first",
		done:      make(chan struct{}),
	}

	if provider.gotFirst {
		t.Error("gotFirst should be false initially")
	}
}
