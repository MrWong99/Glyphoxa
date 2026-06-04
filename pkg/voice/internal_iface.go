package voice

import (
	"context"

	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/snowflake/v2"
)

// The interfaces below are the seam over disgo's voice gateway: they expose
// exactly the surface [Manager] and [Session] use, so tests can substitute
// fakes (see pkg/voice/mock) without a live Discord token. The real
// implementations are disgo's voice.Manager and voice.Conn; thin adapters in
// manager.go bridge them to these interfaces.

// voiceManager is the subset of disgo's voice.Manager that a [Manager] drives:
// one connection per Guild, created lazily and removed on close.
type voiceManager interface {
	// CreateConn creates (or returns the existing) voice connection for guild.
	CreateConn(guild snowflake.ID) voiceConn
	// RemoveConn tears down and forgets the connection for guild.
	RemoveConn(guild snowflake.ID)
}

// voiceConn is the subset of disgo's voice.Conn a [Session] drives. It mirrors
// the disgo method set one-to-one so the production adapter is a no-op wrapper.
type voiceConn interface {
	// Open joins channel, optionally muted/deafened. Blocks until the gateway
	// and UDP transport are ready or ctx is cancelled.
	Open(ctx context.Context, channel snowflake.ID, selfMute, selfDeaf bool) error
	// SetOpusFrameProvider installs the outbound audio source. disgo spawns a
	// sender goroutine that polls the provider every 20ms.
	SetOpusFrameProvider(provider voice.OpusFrameProvider)
	// SetOpusFrameReceiver installs the inbound audio sink. disgo spawns a
	// receiver goroutine that resolves SSRC→UserID and calls the receiver.
	SetOpusFrameReceiver(receiver voice.OpusFrameReceiver)
	// Close leaves the channel and disconnects the gateway and UDP transport.
	Close(ctx context.Context)
}
