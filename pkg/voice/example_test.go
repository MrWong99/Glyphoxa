package voice_test

import (
	"context"
	"fmt"
	"io"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/snowflake/v2"

	"github.com/MrWong99/Glyphoxa/pkg/voice"
)

// frames is a minimal [voice.Source] over pre-encoded 20 ms Opus frames.
type frames [][]byte

func (f *frames) NextFrame(ctx context.Context) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(*f) == 0 {
		return nil, io.EOF
	}
	next := (*f)[0]
	*f = (*f)[1:]
	return next, nil
}

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

	pb, err := sess.Play(ctx, &frames{{0xF8, 0xFF, 0xFE}})
	if err != nil {
		return
	}
	<-pb.Done()

	for frame := range sess.Inbound() {
		fmt.Printf("%s spoke %d Opus bytes\n", frame.UserID, len(frame.Opus))
	}
}
