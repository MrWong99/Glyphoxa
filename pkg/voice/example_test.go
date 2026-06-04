package voice_test

import (
	"bytes"
	"context"
	"fmt"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/snowflake/v2"

	"github.com/MrWong99/Glyphoxa/pkg/voice"
)

// Example shows the full public surface: wire DAVE at client construction, open
// a per-Guild Session, play an Opus stream, and consume inbound speaker frames.
// It compiles against the real API but does not connect (no // Output:), so it
// is a compile-time smoke test rather than a live integration test.
func Example() {
	// In production the client is built by disgo.New with DaveOption():
	//
	//	client, _ := disgo.New(token,
	//		bot.WithGatewayConfigOpts(gateway.WithIntents(gateway.IntentGuildVoiceStates)),
	//		voice.DaveOption(),
	//	)
	var client *bot.Client // obtained from disgo.New
	if client == nil {
		return // keep the example non-connecting
	}

	m := voice.NewManager(client, voice.WithInboundBuffer(128))
	defer m.Close()

	ctx := context.Background()
	sess, err := m.Open(ctx, snowflake.ID(123), snowflake.ID(456))
	if err != nil {
		return
	}

	pb, err := sess.Play(ctx, voice.OpusReader(bytes.NewReader(nil)))
	if err != nil {
		return
	}
	<-pb.Done()

	for frame := range sess.Inbound() {
		fmt.Printf("%s spoke %d Opus bytes\n", frame.UserID, len(frame.Opus))
	}
}

// ExampleOpusReader demonstrates the length-prefixed Opus framing a Source
// consumes; this one actually runs.
func ExampleOpusReader() {
	// Two frames: a 1-byte and a 2-byte payload, each prefixed with a
	// big-endian uint16 length.
	stream := []byte{
		0x00, 0x01, 0xAA, // len 1, payload AA
		0x00, 0x02, 0xBB, 0xCC, // len 2, payload BB CC
	}
	src := voice.OpusReader(bytes.NewReader(stream))
	for {
		frame, err := src.NextFrame(context.Background())
		if err != nil {
			break
		}
		fmt.Printf("%X\n", frame)
	}
	// Output:
	// AA
	// BBCC
}
