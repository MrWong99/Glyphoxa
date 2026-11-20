package voice

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/godave"
)

// keepaliveInterval is how often the keepalive datagram is written to the voice
// UDP socket. Every mature Discord voice library (discordgo, discord.js, eris)
// uses ~5s; Discord's voice server stops routing inbound RTP to a peer that
// stays outbound-silent for on the order of minutes, so the exact value only
// needs to be comfortably below that.
const keepaliveInterval = 5 * time.Second

// keepalivePacketSize is the size of one keepalive datagram: 8 bytes carrying a
// little-endian incrementing counter, the exact shape discordgo sends. The
// server does not parse it — any small non-RTP datagram from the established
// 5-tuple proves liveness — and its echo is shorter than an RTP header, so
// [keepaliveUDPConn.ReadPacket] skips it after counting it (the echoes field).
const keepalivePacketSize = 8

// retiredSocketLinger is how long a re-Open over a LIVE socket keeps the
// previous one alive before closing it — long enough for disgo's audio sender
// to finish any Write whose conn snapshot predates the swap, short enough that
// a reader parked on the old socket moves to the new one promptly.
const retiredSocketLinger = 250 * time.Millisecond

// Masks for the first RTP header byte, bits counted from the most significant
// as RFC 3550 §5.1 numbers them (0–1 version, 2 padding, 3 extension, 4–7 CSRC
// count).
const (
	rtpPaddingBit    = 0x20
	rtpExtensionBit  = 0x10
	rtpCSRCCountMask = 0x0F
)

// keepaliveUDPConn is this package's implementation of disgo's [voice.UDPConn].
// It reimplements the pinned disgo udpConnImpl (see go.mod's disgo pin comment;
// re-diff voice/udp_conn.go on every deliberate bump) because the stock one has
// two faults this platform cannot live with:
//
//  1. It never writes ANYTHING to the UDP socket outside IP discovery and
//     actual speech frames. A Bot that is not speaking is outbound-silent, and
//     after ~13–15 minutes of that Discord's voice server silently stops
//     forwarding inbound RTP — every socket stays open, the voice websocket
//     stays healthy, and the session sits deaf until Idle Close reaps it.
//     This implementation runs a keepalive loop for the whole life of the
//     connection: one small datagram every [keepaliveInterval], written
//     directly to the raw socket (NOT through Write, which RTP-wraps,
//     DAVE-encrypts, and mutates the outbound sequence/timestamp state).
//
//  2. Its Open overwrites the previous socket without closing it, so a reader
//     blocked in a deadline-less Read on the old socket stays parked forever
//     after a voice-server migration. This Open retires the old socket, and
//     ReadPacket carries on reading from the new one.
//
//  3. It cannot survive its own conn's recovery. Since disgo v0.20.0 a
//     session-invalidating voice-gateway close makes the conn Close this
//     transport and then re-Open it for a fresh voice session, WITHOUT leaving
//     the channel. On the stock transport the Close hands net.ErrClosed to the
//     audio sender and receiver, both of which reap themselves permanently —
//     the conn comes back connected, deaf and mute. Here a Close is a PAUSE
//     (see the lifecycle below): the reader waits for the next socket, the
//     writer drops frames like a client on a lossy link, and audio resumes on
//     the re-Open with nobody in the channel having seen the Bot leave.
//
// Lifecycle. Three states, all guarded by connMu:
//
//   - open: a live socket and a keepalive loop.
//   - paused: Close was called (disgo's own recovery, or the first half of a
//     teardown). No socket, no keepalive loop, nothing leaked. Open resumes.
//     Write drops; ReadPacket blocks.
//   - terminated: terminate was called — by the conn guard (connguard.go) when
//     disgo discards the conn for good. Open is refused, and Write/ReadPacket
//     return net.ErrClosed, which is the contract disgo's audio goroutines and
//     [Session.Close]'s reap sequence depend on (#579, #586).
//
// It also counts every parsed inbound RTP voice packet (before decryption, so
// a DAVE key-roll hiccup still proves the media path is alive) — the signal
// the wirenpc media watchdog and the once-a-minute liveness log read through
// [Session.MediaLiveness].
//
// Concurrency contract (same as disgo's): one writer (disgo's audio sender
// goroutine calls Write), one reader (disgo's audio receiver goroutine calls
// ReadPacket), Open/Close serialized by connMu. The keepalive goroutine only
// writes raw datagrams on its captured net.Conn — kernel-level concurrent
// writes on a connected UDP socket are safe and it shares no RTP state with
// Write. The receive/decrypt buffers are reused across calls: a returned
// Packet's Opus payload is valid only until the next ReadPacket, exactly like
// disgo (the inboundDispatcher clones it).
type keepaliveUDPConn struct {
	logger  *slog.Logger
	metrics MetricsRecorder

	daveSession godave.Session
	ssrcLookup  voice.SsrcLookupFunc

	// dial and newTicker are injected seams (the idleclose discipline) so tests
	// drive the keepalive cadence by hand and hand Open an in-memory conn.
	dial      func(ctx context.Context, network, address string) (net.Conn, error)
	newTicker func(d time.Duration) (<-chan time.Time, func())

	// conn is the live socket; nil while paused or terminated. Guarded by connMu.
	conn   net.Conn
	connMu sync.Mutex
	// stopKeepalive ends the current keepalive goroutine; non-nil exactly while
	// one runs. Guarded by connMu.
	stopKeepalive chan struct{}
	// terminated latches in terminate: the conn this transport belongs to was
	// discarded, so a straggler voice-gateway Ready dispatched during teardown
	// cannot revive it — a revived Open would dial a socket and start a
	// keepalive goroutine that nothing ever stops. Guarded by connMu.
	terminated bool
	// changed is closed and replaced on every lifecycle transition (Open, Close,
	// terminate); a ReadPacket waiting out a pause selects on it. Guarded by
	// connMu.
	changed chan struct{}

	// encrypter and ssrc belong to the CURRENT voice session and are replaced by
	// a re-Open (ssrc) and the SetSecretKey that follows it (encrypter), on the
	// voice-gateway goroutine, while the sender and receiver goroutines are
	// mid-flight — so both are guarded by connMu and read as a snapshot. The
	// encrypter is nil from Open until the session's key arrives: frames are
	// dropped rather than sealed with the previous session's key.
	encrypter voice.Encrypter
	ssrc      uint32

	// sequence and timestamp are the outbound RTP counters, touched only by the
	// single writer.
	sequence  uint16
	timestamp uint32

	receiveBuffer []byte
	decryptBuffer []byte
	encryptBuffer []byte

	// packets counts parsed inbound RTP voice packets on the CURRENT socket
	// (reset on re-Open: a fresh path starts a fresh count). A counter rather
	// than a timestamp so the hot path reads no clock — the watchdog derives
	// recency by diffing it on its own tick, the idleclose marks discipline.
	packets atomic.Uint64
	// echoes counts keepalive echoes on the CURRENT socket (reset on re-Open):
	// inbound datagrams of exactly [keepalivePacketSize] bytes, the shape the
	// voice server reflects our keepalives back in. On a healthy socket they
	// arrive ~every 5s REGARDLESS of anyone speaking, which makes their
	// cessation the watchdog's speech-independent dead-socket signal — and if
	// a server generation stops echoing, the counter simply never arms that
	// signal (see mediawatch): absence of echoes is never read as death.
	echoes atomic.Uint64
	// keepalives counts keepalive datagrams successfully written, cumulatively
	// across re-Opens; the liveness log reports its per-interval delta.
	keepalives atomic.Uint64
}

var _ voice.UDPConn = (*keepaliveUDPConn)(nil)

// newKeepaliveUDPConn builds the transport for one voice connection. logger and
// metrics must be non-nil (TransportOption defaults them).
func newKeepaliveUDPConn(daveSession godave.Session, ssrcLookup voice.SsrcLookupFunc, logger *slog.Logger, metrics MetricsRecorder) *keepaliveUDPConn {
	return &keepaliveUDPConn{
		logger:      logger.With(slog.String("name", "voice_conn_udp_conn")),
		metrics:     metrics,
		daveSession: daveSession,
		ssrcLookup:  ssrcLookup,
		dial:        (&net.Dialer{Timeout: voice.UDPTimeout}).DialContext,
		newTicker: func(d time.Duration) (<-chan time.Time, func()) {
			t := time.NewTicker(d)
			return t.C, t.Stop
		},
		changed:       make(chan struct{}),
		receiveBuffer: make([]byte, 1400),
		decryptBuffer: make([]byte, 512),
		encryptBuffer: make([]byte, 512),
	}
}

// LocalAddr returns the live socket's local address, or nil while there is no
// socket (paused or terminated).
func (u *keepaliveUDPConn) LocalAddr() net.Addr {
	u.connMu.Lock()
	defer u.connMu.Unlock()
	if u.conn == nil {
		return nil
	}
	return u.conn.LocalAddr()
}

// RemoteAddr returns the live socket's remote address, or nil while there is
// no socket (paused or terminated).
func (u *keepaliveUDPConn) RemoteAddr() net.Addr {
	u.connMu.Lock()
	defer u.connMu.Unlock()
	if u.conn == nil {
		return nil
	}
	return u.conn.RemoteAddr()
}

func (u *keepaliveUDPConn) SetSecretKey(encryptionMode voice.EncryptionMode, secretKey []byte) error {
	e, err := voice.NewEncrypter(encryptionMode, secretKey)
	if err != nil {
		return fmt.Errorf("failed to create encrypter: %w", err)
	}
	u.connMu.Lock()
	u.encrypter = e
	u.connMu.Unlock()
	return nil
}

func (u *keepaliveUDPConn) SetDeadline(t time.Time) error {
	return u.withSocket(func(c net.Conn) error { return c.SetDeadline(t) })
}

func (u *keepaliveUDPConn) SetReadDeadline(t time.Time) error {
	return u.withSocket(func(c net.Conn) error { return c.SetReadDeadline(t) })
}

func (u *keepaliveUDPConn) SetWriteDeadline(t time.Time) error {
	return u.withSocket(func(c net.Conn) error { return c.SetWriteDeadline(t) })
}

// withSocket runs fn on the live socket, or reports net.ErrClosed when there
// is none (paused or terminated) instead of dereferencing a nil conn.
func (u *keepaliveUDPConn) withSocket(fn func(net.Conn) error) error {
	u.connMu.Lock()
	defer u.connMu.Unlock()
	if u.conn == nil {
		return net.ErrClosed
	}
	return fn(u.conn)
}

// notifyLocked wakes every ReadPacket waiting on a lifecycle transition. Caller
// holds connMu.
func (u *keepaliveUDPConn) notifyLocked() {
	close(u.changed)
	u.changed = make(chan struct{})
}

// Open dials the voice server and performs Discord's IP discovery, then starts
// the keepalive loop. disgo calls it more than once on one instance — after a
// Close when its conn recovers with a fresh voice session, or straight over a
// live socket — and each call retires whatever socket and keepalive loop are
// still there. Only a terminated transport refuses.
func (u *keepaliveUDPConn) Open(ctx context.Context, ip string, port int, ssrc uint32) (string, int, error) {
	u.connMu.Lock()
	defer u.connMu.Unlock()

	if u.terminated {
		return "", 0, fmt.Errorf("voice: transport already closed; refusing to reopen")
	}

	u.stopKeepaliveLocked()
	if old := u.conn; old != nil {
		// Retire the previous socket on a LINGER, not immediately, so an
		// in-flight sender Write holding a snapshot of it finishes on the old
		// socket (harmlessly, toward the old server) instead of failing. Write
		// and ReadPacket both tolerate the close when it comes — the linger only
		// spares a frame; a reader parked in a deadline-less Read on the old
		// socket gets its wake a beat later and moves to the new one.
		time.AfterFunc(retiredSocketLinger, func() { _ = old.Close() })
	}

	host := net.JoinHostPort(ip, strconv.Itoa(port))
	u.logger.Debug("opening voice UDP connection", slog.String("host", host))
	conn, err := u.dial(ctx, "udp", host)
	if err != nil {
		return "", 0, fmt.Errorf("failed to open UDPConn connection: %w", err)
	}

	// IP discovery: https://discord.com/developers/docs/topics/voice-connections#ip-discovery
	sb := make([]byte, 74)
	binary.BigEndian.PutUint16(sb[:2], 1)      // 1 = send
	binary.BigEndian.PutUint16(sb[2:4], 70)    // 70 = length
	binary.BigEndian.PutUint32(sb[4:74], ssrc) // ssrc

	if err = conn.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		_ = conn.Close()
		return "", 0, fmt.Errorf("failed to set write deadline on UDPConn connection: %w", err)
	}
	if _, err = conn.Write(sb); err != nil {
		_ = conn.Close()
		return "", 0, fmt.Errorf("failed to write ssrc to UDPConn connection: %w", err)
	}
	_ = conn.SetWriteDeadline(time.Time{})

	rb := make([]byte, 74)
	if err = conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		_ = conn.Close()
		return "", 0, fmt.Errorf("failed to set read deadline on UDPConn connection: %w", err)
	}
	if _, err = conn.Read(rb); err != nil {
		_ = conn.Close()
		return "", 0, fmt.Errorf("failed to read ip discovery from UDPConn connection: %w", err)
	}
	_ = conn.SetReadDeadline(time.Time{})

	if binary.BigEndian.Uint16(rb[0:2]) != 2 {
		_ = conn.Close()
		return "", 0, fmt.Errorf("invalid ip discovery response")
	}
	if size := binary.BigEndian.Uint16(rb[2:4]); size != 70 {
		_ = conn.Close()
		return "", 0, fmt.Errorf("invalid ip discovery response size")
	}

	returnedSSRC := binary.BigEndian.Uint32(rb[4:8])   // ssrc
	ourAddress := string(bytes.Trim(rb[8:72], "\x00")) // our ip (null-padded)
	ourPort := int(binary.BigEndian.Uint16(rb[72:74])) // our port

	if returnedSSRC != ssrc {
		_ = conn.Close()
		return "", 0, fmt.Errorf("invalid ssrc in ip discovery response")
	}

	u.ssrc = ssrc
	u.daveSession.AssignSsrcToCodec(ssrc, godave.CodecOpus)
	// A (re-)Open always belongs to a NEW voice session (a resume never sends
	// Ready), whose transport key arrives in the SetSecretKey that follows.
	// Until then there is nothing valid to seal or open packets with.
	u.encrypter = nil

	u.conn = conn
	u.packets.Store(0)
	u.echoes.Store(0)
	stop := make(chan struct{})
	u.stopKeepalive = stop
	go u.keepaliveLoop(conn, stop)
	u.notifyLocked()

	return ourAddress, ourPort, nil
}

// stopKeepaliveLocked ends the current keepalive goroutine, if any. Caller
// holds connMu.
func (u *keepaliveUDPConn) stopKeepaliveLocked() {
	if u.stopKeepalive != nil {
		close(u.stopKeepalive)
		u.stopKeepalive = nil
	}
}

// keepaliveLoop writes one keepalive datagram immediately and then one per
// tick, until stop closes or the socket dies under it. It writes on its own
// captured conn — never the struct field — so a concurrent re-Open can never
// point it at the new socket; the re-Open closes this conn, the next write
// returns net.ErrClosed, and this loop exits while the new one takes over.
func (u *keepaliveUDPConn) keepaliveLoop(conn net.Conn, stop <-chan struct{}) {
	ticks, stopTicker := u.newTicker(keepaliveInterval)
	defer stopTicker()
	packet := make([]byte, keepalivePacketSize)
	var seq uint64
	for {
		binary.LittleEndian.PutUint64(packet, seq)
		seq++
		if _, err := conn.Write(packet); err != nil {
			if errors.Is(err, net.ErrClosed) {
				return // socket closed or replaced; a re-Open runs its own loop
			}
			u.metrics.UDPKeepaliveSendError()
			// Debug: a transient send failure self-heals on the next tick, and the
			// counter above is the operational signal. The message deliberately
			// avoids the exact strings internal/observe's disgo log filter matches.
			u.logger.Debug("voice UDP keepalive write failed", slog.Any("err", err))
		} else {
			u.keepalives.Add(1)
			u.metrics.UDPKeepaliveSent()
		}
		select {
		case <-stop:
			return
		case <-ticks:
		}
	}
}

// Write RTP-wraps, DAVE-encrypts, and transport-encrypts one outbound Opus
// frame — the disgo implementation's wire format and error strings
// (internal/observe matches some of them), with this transport's lifecycle:
//
//   - terminated: net.ErrClosed, on which disgo's sender reaps itself — the
//     teardown contract [Session.Close] relies on.
//   - paused, or open but still waiting for the new session's key: the frame is
//     DROPPED and reported as written. The RTP counters still advance, so the
//     receiving side sees ordinary packet loss. Returning an error here would
//     end the sender for the rest of the connection's life.
func (u *keepaliveUDPConn) Write(p []byte) (int, error) {
	u.connMu.Lock()
	conn, encrypter, ssrc, terminated := u.conn, u.encrypter, u.ssrc, u.terminated
	u.connMu.Unlock()

	if terminated {
		return 0, fmt.Errorf("failed to write packet: %w", net.ErrClosed)
	}

	var header [voice.RTPHeaderSize]byte
	header[0] = voice.RTPVersionPadExtend
	header[1] = voice.RTPPayloadType
	binary.BigEndian.PutUint16(header[2:4], u.sequence)
	u.sequence++
	binary.BigEndian.PutUint32(header[4:8], u.timestamp)
	u.timestamp += voice.OpusFrameSize
	binary.BigEndian.PutUint32(header[8:], ssrc)

	if conn == nil || encrypter == nil {
		return len(p), nil
	}

	// Sized by LENGTH, not capacity — a deliberate divergence from the pinned
	// disgo udp_conn.go, which only grows cap: every godave.Session bounds its
	// output by len(out), so a len-512 buffer silently truncated any frame over
	// 512 bytes. Keep this on the next upstream re-diff.
	u.encryptBuffer = sizedBuffer(u.encryptBuffer, u.daveSession.MaxEncryptedFrameSize(len(p)))

	n, err := u.daveSession.Encrypt(ssrc, p, u.encryptBuffer)
	if err != nil {
		return 0, fmt.Errorf("failed to encrypt packet: %w", err)
	}

	if _, err = conn.Write(encrypter.Encrypt(header, u.encryptBuffer[:n])); err != nil {
		// The socket closed between the snapshot and the write: a pause or a
		// re-Open raced this frame. Only a terminated transport may surface
		// net.ErrClosed to the sender.
		if errors.Is(err, net.ErrClosed) && !u.isTerminated() {
			return len(p), nil
		}
		return 0, fmt.Errorf("failed to write packet: %w", err)
	}

	return len(p), nil
}

// sessionKey returns the current voice session's transport encrypter, nil
// while there is none (paused, or re-opened and still waiting for the key).
func (u *keepaliveUDPConn) sessionKey() voice.Encrypter {
	u.connMu.Lock()
	defer u.connMu.Unlock()
	return u.encrypter
}

func (u *keepaliveUDPConn) isTerminated() bool {
	u.connMu.Lock()
	defer u.connMu.Unlock()
	return u.terminated
}

func (u *keepaliveUDPConn) Read(p []byte) (int, error) {
	packet, err := u.ReadPacket()
	if err != nil {
		return 0, err
	}
	return copy(p, packet.Opus), nil
}

// ReadPacket reads, parses, and decrypts one inbound RTP voice packet — the
// pinned disgo implementation plus the packet count that feeds the media
// watchdog. The count is stamped right after the payload-type check and BEFORE
// decryption: an undecryptable packet still proves RTP is arriving, which is
// exactly the discrimination the watchdog needs. Keepalive echoes and other
// non-RTP datagrams fall out at the length/type checks, counting nothing.
//
// It follows the transport's lifecycle rather than a single socket: while
// paused it BLOCKS until the next Open (or terminate), and a socket that is
// closed or replaced under a parked read is not an error — the loop moves to
// the current one. net.ErrClosed is returned only once the transport is
// terminated, which is when disgo's receiver is meant to reap itself.
func (u *keepaliveUDPConn) ReadPacket() (*voice.Packet, error) {
	for {
		u.connMu.Lock()
		conn, terminated, changed := u.conn, u.terminated, u.changed
		u.connMu.Unlock()

		if terminated {
			return nil, fmt.Errorf("failed to read packet: %w", net.ErrClosed)
		}
		if conn == nil {
			<-changed // paused: wait for the re-Open or the terminate
			continue
		}

		n, err := conn.Read(u.receiveBuffer)
		if err != nil {
			if u.superseded(conn) {
				continue // closed or replaced under us; not this reader's error
			}
			return nil, fmt.Errorf("failed to read packet: %w", err)
		}

		if n < voice.RTPHeaderSize {
			if n == keepalivePacketSize {
				u.echoes.Add(1)
			}
			continue
		}

		packetType := u.receiveBuffer[1]
		if packetType != voice.RTPPayloadType {
			// ignore non-voice packets
			continue
		}

		u.packets.Add(1)

		// RTP padding lives at the end of the payload, which is encrypted, so it
		// cannot be measured until after transport decryption; it is removed
		// further down (the upstream disgo v0.20.0 fix — the pinned code tested
		// the wrong header bit and read the count from the last WIRE byte).
		hasPadding := (u.receiveBuffer[0] & rtpPaddingBit) != 0

		p := voice.Packet{
			Type:         packetType,
			Sequence:     binary.BigEndian.Uint16(u.receiveBuffer[2:4]),
			Timestamp:    binary.BigEndian.Uint32(u.receiveBuffer[4:8]),
			SSRC:         binary.BigEndian.Uint32(u.receiveBuffer[8:voice.RTPHeaderSize]),
			HasExtension: (u.receiveBuffer[0] & rtpExtensionBit) != 0,
		}

		cc := int(u.receiveBuffer[0] & rtpCSRCCountMask)
		headerLen := voice.RTPHeaderSize + (4 * cc)
		if n < headerLen {
			continue
		}

		p.CSRC = make([]uint32, cc)
		for i := range cc {
			p.CSRC[i] = binary.BigEndian.Uint32(u.receiveBuffer[voice.RTPHeaderSize+i*4 : voice.RTPHeaderSize+i*4+4])
		}

		var extensionLenWords uint16
		if p.HasExtension {
			if n < headerLen+4 {
				continue
			}
			p.ExtensionID = int(binary.BigEndian.Uint16(u.receiveBuffer[headerLen : headerLen+2]))
			extensionLenWords = binary.BigEndian.Uint16(u.receiveBuffer[headerLen+2 : headerLen+4])
			headerLen += 4
		}

		p.HeaderSize = headerLen
		if n < p.HeaderSize+4 {
			continue
		}

		// Read the key only now, AFTER the packet arrived: the read above can
		// park for a long time, and a key snapshotted before it would still be
		// the "no key yet" of a fresh re-Open when the first packet lands.
		encrypter := u.sessionKey()
		if encrypter == nil {
			// Media on the new socket ahead of the session's key: counted above
			// (the path is alive), but not yet decryptable.
			continue
		}

		decrypted, err := encrypter.Decrypt(p.HeaderSize, u.receiveBuffer[:n])
		if err != nil {
			return nil, fmt.Errorf("failed to decrypt packet: %w", err)
		}

		var decryptedOffset int
		if p.HasExtension {
			extensionLen := int(extensionLenWords) * 4
			if decryptedOffset+extensionLen > len(decrypted) {
				continue
			}
			p.Extension = decrypted[decryptedOffset : decryptedOffset+extensionLen]
			decryptedOffset += extensionLen
		}

		decrypted = decrypted[decryptedOffset:]

		if hasPadding {
			decrypted = stripRTPPadding(decrypted)
		}
		// A padding-only packet carries no media: it advances the sequence number
		// without advancing the timestamp, and has nothing to DAVE-decrypt.
		if len(decrypted) == 0 {
			continue
		}

		userID := godave.UserID(u.ssrcLookup(p.SSRC).String())
		// Sized by LENGTH (see Write): with the upstream cap-only growth a
		// high-bitrate channel's >512-byte Opus payloads decoded as corrupted
		// audio because Decrypt copied only len(out) bytes.
		u.decryptBuffer = sizedBuffer(u.decryptBuffer, u.daveSession.MaxDecryptedFrameSize(userID, len(decrypted)))

		n, err = u.daveSession.Decrypt(userID, decrypted, u.decryptBuffer)
		if err != nil {
			return nil, fmt.Errorf("failed to DAVE decrypt packet: %w", err)
		}

		p.Opus = u.decryptBuffer[:n]

		return &p, nil
	}
}

// superseded reports whether conn is no longer the socket a reader should be
// on: the transport was paused, re-opened onto another socket, or terminated
// since the reader snapshotted it. The reader then re-evaluates the lifecycle
// instead of surfacing the dead socket's error.
func (u *keepaliveUDPConn) superseded(conn net.Conn) bool {
	u.connMu.Lock()
	defer u.connMu.Unlock()
	return u.terminated || u.conn != conn
}

// stripRTPPadding removes RTP padding from a transport-decrypted payload. Per
// RFC 3550 §5.1 the final octet holds the number of padding octets, itself
// included. A payload whose count is missing or implausible is returned
// unchanged rather than truncated, so a malformed packet degrades to a DAVE
// decrypt failure instead of silent corruption.
func stripRTPPadding(payload []byte) []byte {
	if len(payload) == 0 {
		return payload
	}
	paddingLen := int(payload[len(payload)-1])
	if paddingLen == 0 || paddingLen > len(payload) {
		return payload
	}
	return payload[:len(payload)-paddingLen]
}

// sizedBuffer returns buf with len(buf) == n, reusing the backing array when its
// capacity allows and allocating a fresh one otherwise. The DAVE encrypt/decrypt
// calls write into out[:len(out)], so the LENGTH is the contract, not the cap.
func sizedBuffer(buf []byte, n int) []byte {
	if cap(buf) < n {
		return make([]byte, n)
	}
	return buf[:n]
}

// Close PAUSES the transport: it stops the keepalive loop and closes the
// socket, leaving nothing running, but a later Open resumes it. This is what
// disgo's conn calls — both when it recovers from a session-invalidating
// voice-gateway close (Close, then re-Open for a fresh session) and as one
// step of discarding the conn. Only the second is final, and the conn guard
// (connguard.go) marks it with terminate; Close alone must not end the audio
// goroutines, or every in-place recovery would come back deaf and mute.
func (u *keepaliveUDPConn) Close() error {
	u.connMu.Lock()
	defer u.connMu.Unlock()
	return u.closeSocketLocked()
}

// terminate ends the transport for good: the socket and keepalive loop are
// closed, Open is refused from here on, and the blocked ReadPacket and the
// next Write return net.ErrClosed — the contract disgo's audio goroutines and
// [Session.Close]'s reap sequence depend on (#579, #586). Idempotent. A
// straggler Open that is mid-dial holds connMu, so terminate runs after it and
// closes the socket it just opened; one that arrives later is refused.
func (u *keepaliveUDPConn) terminate() {
	u.connMu.Lock()
	defer u.connMu.Unlock()
	if u.terminated {
		return
	}
	u.terminated = true
	_ = u.closeSocketLocked()
}

// closeSocketLocked stops the keepalive loop, closes and forgets the live
// socket, drops the session key, and wakes a waiting reader. Caller holds
// connMu.
func (u *keepaliveUDPConn) closeSocketLocked() error {
	u.stopKeepaliveLocked()
	conn := u.conn
	u.conn = nil
	u.encrypter = nil
	u.notifyLocked()
	if conn == nil {
		return nil
	}
	return conn.Close()
}
