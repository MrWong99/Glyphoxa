// Package voice is a small, idiomatic wrapper over disgo's voice gateway and
// godave's DAVE end-to-end encryption for streaming Discord voice.
//
// A [Manager] owns one disgo [bot.Client] and a [Session] per Guild. A Session
// presents a buffered inbound channel of per-speaker Opus [Frame]s for STT, an
// auto-interrupt-and-replace [Session.Play] returning a first-class [Playback]
// handle, and explicit [Session.State]/[Session.Close]. Many Guilds run
// concurrently; each Session is independent.
//
// Audio never crosses a process boundary here (ADR-0005): a Session talks the
// Discord voice WebSocket and UDP transport directly. DAVE (ADR-0006) is wired
// at client construction by the caller via [DaveOption]; it is mandatory in
// production but orthogonal to this package's testable surface.
//
// Auto-reconnect of the voice connection is out of scope for v1: a Session that
// loses its connection transitions to [Closed] and the caller re-[Manager.Open]s.
//
// Usage:
//
//	client, _ := disgo.New(token,
//		bot.WithGatewayConfigOpts(gateway.WithIntents(gateway.IntentGuildVoiceStates)),
//		voice.DaveOption(), // wires DAVE/MLS; omit to run without encryption
//	)
//	m := voice.NewManager(client)
//	defer m.Close()
//
//	sess, _ := m.Open(ctx, guildID, channelID)
//	pb, _ := sess.Play(ctx, voice.OpusReader(opusStream))
//	<-pb.Done()
//
//	for frame := range sess.Inbound() {
//		// frame.UserID, frame.Opus → STT
//	}
package voice
