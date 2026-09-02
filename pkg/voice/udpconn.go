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

// retiredSocketLinger is how long a re-Open keeps the previous socket alive
// before closing it — long enough for disgo's audio sender to finish any Write
// whose conn snapshot predates the swap (a net.ErrClosed there self-reaps the
// sender permanently), short enough that a reader parked on the old socket is
// released promptly.
const retiredSocketLinger = 250 * time.Millisecond

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
//     after a voice-server migration. This Open closes the old socket first;
//     the blocked read returns net.ErrClosed, disgo's receiver reaps itself,
//     and the wirenpc cycle rebuilds cleanly.
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

	conn   net.Conn
	connMu sync.Mutex
	// stopKeepalive ends the current keepalive goroutine; non-nil exactly while
	// one runs. Guarded by connMu.
	stopKeepalive chan struct{}
	// closed latches on Close so a straggler voice-gateway Ready dispatched
	// during teardown cannot revive the transport — a revived Open would dial a
	// socket and start a keepalive goroutine that nothing ever stops. Guarded
	// by connMu. A closed transport refuses Open with a clear error; the join
	// then fails visibly and the caller's retry builds a fresh conn.
	closed bool

	encrypter voice.Encrypter

	header    [12]byte
	ssrc      uint32
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
		receiveBuffer: make([]byte, 1400),
		decryptBuffer: make([]byte, 512),
		encryptBuffer: make([]byte, 512),
	}
}

func (u *keepaliveUDPConn) LocalAddr() net.Addr {
	u.connMu.Lock()
	defer u.connMu.Unlock()
	return u.conn.LocalAddr()
}

func (u *keepaliveUDPConn) RemoteAddr() net.Addr {
	u.connMu.Lock()
	defer u.connMu.Unlock()
	return u.conn.RemoteAddr()
}

func (u *keepaliveUDPConn) SetSecretKey(encryptionMode voice.EncryptionMode, secretKey []byte) error {
	e, err := voice.NewEncrypter(encryptionMode, secretKey)
	if err != nil {
		return fmt.Errorf("failed to create encrypter: %w", err)
	}
	u.encrypter = e
	return nil
}

func (u *keepaliveUDPConn) SetDeadline(t time.Time) error {
	u.connMu.Lock()
	defer u.connMu.Unlock()
	return u.conn.SetDeadline(t)
}

func (u *keepaliveUDPConn) SetReadDeadline(t time.Time) error {
	u.connMu.Lock()
	defer u.connMu.Unlock()
	return u.conn.SetReadDeadline(t)
}

func (u *keepaliveUDPConn) SetWriteDeadline(t time.Time) error {
	u.connMu.Lock()
	defer u.connMu.Unlock()
	return u.conn.SetWriteDeadline(t)
}

// Open dials the voice server and performs Discord's IP discovery, then starts
// the keepalive loop. disgo calls it more than once on one instance (a voice
// server migration, a resume-rejected fresh session): each call retires the
// previous socket and keepalive loop first — see the type doc for why closing
// the old socket matters.
func (u *keepaliveUDPConn) Open(ctx context.Context, ip string, port int, ssrc uint32) (string, int, error) {
	u.connMu.Lock()
	defer u.connMu.Unlock()

	if u.closed {
		return "", 0, fmt.Errorf("voice: transport already closed; refusing to reopen")
	}

	u.stopKeepaliveLocked()
	if old := u.conn; old != nil {
		// Retire the previous socket on a LINGER, not immediately: disgo's audio
		// sender may hold a Write-time snapshot of it, and net.ErrClosed makes
		// the sender PERMANENTLY self-reap (audio_sender handleErr) — a mute NPC
		// inside a cycle that keeps serving. The linger lets any in-flight write
		// finish on the old socket (harmlessly, toward the old server); a reader
		// parked in a deadline-less Read still gets its net.ErrClosed wake, just
		// a beat later, instead of being stranded forever like disgo strands it.
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

	u.header[0] = voice.RTPVersionPadExtend
	u.header[1] = voice.RTPPayloadType
	binary.BigEndian.PutUint32(u.header[8:], ssrc)

	u.ssrc = ssrc
	u.daveSession.AssignSsrcToCodec(ssrc, godave.CodecOpus)

	u.conn = conn
	u.packets.Store(0)
	u.echoes.Store(0)
	stop := make(chan struct{})
	u.stopKeepalive = stop
	go u.keepaliveLoop(conn, stop)

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
// frame — byte-for-byte the pinned disgo implementation, error strings
// included (internal/observe matches some of them).
func (u *keepaliveUDPConn) Write(p []byte) (int, error) {
	u.connMu.Lock()
	conn := u.conn
	u.connMu.Unlock()

	binary.BigEndian.PutUint16(u.header[2:4], u.sequence)
	u.sequence++

	binary.BigEndian.PutUint32(u.header[4:8], u.timestamp)
	u.timestamp += voice.OpusFrameSize

	// Sized by LENGTH, not capacity — a deliberate divergence from the pinned
	// disgo udp_conn.go, which only grows cap: every godave.Session bounds its
	// output by len(out), so a len-512 buffer silently truncated any frame over
	// 512 bytes. Keep this on the next upstream re-diff.
	u.encryptBuffer = sizedBuffer(u.encryptBuffer, u.daveSession.MaxEncryptedFrameSize(len(p)))

	n, err := u.daveSession.Encrypt(u.ssrc, p, u.encryptBuffer)
	if err != nil {
		return 0, fmt.Errorf("failed to encrypt packet: %w", err)
	}

	if _, err = conn.Write(u.encrypter.Encrypt(u.header, u.encryptBuffer[:n])); err != nil {
		return 0, fmt.Errorf("failed to write packet: %w", err)
	}

	return len(p), nil
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
func (u *keepaliveUDPConn) ReadPacket() (*voice.Packet, error) {
	u.connMu.Lock()
	conn := u.conn
	u.connMu.Unlock()

	for {
		n, err := conn.Read(u.receiveBuffer)
		if err != nil {
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

		hasPadding := (u.receiveBuffer[0] & 0x04) != 0
		if hasPadding {
			paddingLen := int(u.receiveBuffer[n-1])
			if paddingLen <= 0 || paddingLen > n-voice.RTPHeaderSize {
				continue
			}
			n -= paddingLen
		}

		p := voice.Packet{
			Type:         packetType,
			Sequence:     binary.BigEndian.Uint16(u.receiveBuffer[2:4]),
			Timestamp:    binary.BigEndian.Uint32(u.receiveBuffer[4:8]),
			SSRC:         binary.BigEndian.Uint32(u.receiveBuffer[8:voice.RTPHeaderSize]),
			HasExtension: (u.receiveBuffer[0] & 0x10) != 0,
		}

		cc := int(u.receiveBuffer[0] & 0x0F)
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

		decrypted, err := u.encrypter.Decrypt(p.HeaderSize, u.receiveBuffer[:n])
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

// sizedBuffer returns buf with len(buf) == n, reusing the backing array when its
// capacity allows and allocating a fresh one otherwise. The DAVE encrypt/decrypt
// calls write into out[:len(out)], so the LENGTH is the contract, not the cap.
func sizedBuffer(buf []byte, n int) []byte {
	if cap(buf) < n {
		return make([]byte, n)
	}
	return buf[:n]
}

// Close stops the keepalive loop and closes the socket. The closed socket
// propagates net.ErrClosed into a blocked ReadPacket / the next Write — the
// exact contract disgo's audio goroutines and [Session.Close]'s reap sequence
// depend on (#579, #586).
func (u *keepaliveUDPConn) Close() error {
	u.connMu.Lock()
	defer u.connMu.Unlock()
	u.closed = true
	u.stopKeepaliveLocked()
	if u.conn == nil {
		return nil
	}
	return u.conn.Close()
}
