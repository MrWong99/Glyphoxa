package voice

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"log/slog"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/godave"
	"github.com/disgoorg/snowflake/v2"
	"go.uber.org/goleak"
)

// fakePacketConn is the in-memory net.Conn the keepalive transport tests run
// over: Writes are recorded, Reads are fed from a channel, and Close makes
// both fail net.ErrClosed — the exact error contract disgo's reap paths and
// the keepalive loop's exit depend on.
type fakePacketConn struct {
	mu        sync.Mutex
	writes    [][]byte
	failWrite bool         // transient (non-ErrClosed) write failures while set
	readers   atomic.Int32 // goroutines currently blocked in Read
	reads     chan []byte
	closed    chan struct{}
	closeOnce sync.Once
}

// errTransient is the non-ErrClosed write failure fakePacketConn injects.
var errTransient = errors.New("transient socket error")

func (c *fakePacketConn) setFailWrite(fail bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.failWrite = fail
}

func newFakePacketConn() *fakePacketConn {
	return &fakePacketConn{reads: make(chan []byte, 16), closed: make(chan struct{})}
}

func (c *fakePacketConn) Read(p []byte) (int, error) {
	select {
	case <-c.closed:
		return 0, net.ErrClosed
	default:
	}
	c.readers.Add(1)
	defer c.readers.Add(-1)
	select {
	case b := <-c.reads:
		return copy(p, b), nil
	case <-c.closed:
		return 0, net.ErrClosed
	}
}

func (c *fakePacketConn) Write(p []byte) (int, error) {
	select {
	case <-c.closed:
		return 0, net.ErrClosed
	default:
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failWrite {
		return 0, errTransient
	}
	c.writes = append(c.writes, bytes.Clone(p))
	return len(p), nil
}

func (c *fakePacketConn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return nil
}

func (c *fakePacketConn) writeCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.writes)
}

func (c *fakePacketConn) write(i int) []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.writes[i]
}

func (c *fakePacketConn) LocalAddr() net.Addr              { return &net.UDPAddr{} }
func (c *fakePacketConn) RemoteAddr() net.Addr             { return &net.UDPAddr{} }
func (c *fakePacketConn) SetDeadline(time.Time) error      { return nil }
func (c *fakePacketConn) SetReadDeadline(time.Time) error  { return nil }
func (c *fakePacketConn) SetWriteDeadline(time.Time) error { return nil }

// discoveryResponse is a valid Discord IP-discovery reply for ssrc.
func discoveryResponse(ssrc uint32, ip string, port uint16) []byte {
	rb := make([]byte, 74)
	binary.BigEndian.PutUint16(rb[0:2], 2)
	binary.BigEndian.PutUint16(rb[2:4], 70)
	binary.BigEndian.PutUint32(rb[4:8], ssrc)
	copy(rb[8:72], ip)
	binary.BigEndian.PutUint16(rb[72:74], port)
	return rb
}

// rtpPacket is a minimal RTP-shaped packet: 12-byte header, type 0x78, raw
// payload. Only usable where the parser bails BEFORE transport decryption
// (wrong type, undecryptable garbage) — for a packet that must decrypt, use
// sealRTP. (disgo's EncryptionModeNone decrypter is broken — its Decrypt
// copies into a zero-length buffer and panics — so the tests run a real AEAD.)
func rtpPacket(seq uint16, ts, ssrc uint32, payload []byte) []byte {
	b := make([]byte, voice.RTPHeaderSize+len(payload))
	b[0] = voice.RTPVersionPadExtend
	b[1] = voice.RTPPayloadType
	binary.BigEndian.PutUint16(b[2:4], seq)
	binary.BigEndian.PutUint32(b[4:8], ts)
	binary.BigEndian.PutUint32(b[8:12], ssrc)
	copy(b[voice.RTPHeaderSize:], payload)
	return b
}

// testSecretKey is the 32-byte AEAD key the read-path tests share.
var testSecretKey = bytes.Repeat([]byte{0x42}, 32)

// sealRTP builds a wire packet — RTP header plus AEAD-sealed payload — that a
// conn keyed with testSecretKey decrypts back to payload.
func sealRTP(t *testing.T, seq uint16, ts, ssrc uint32, payload []byte) []byte {
	t.Helper()
	enc, err := voice.NewEncrypter(voice.EncryptionModeAEADAES256GCMRTPSize, testSecretKey)
	if err != nil {
		t.Fatalf("NewEncrypter: %v", err)
	}
	var header [voice.RTPHeaderSize]byte
	header[0] = voice.RTPVersionPadExtend
	header[1] = voice.RTPPayloadType
	binary.BigEndian.PutUint16(header[2:4], seq)
	binary.BigEndian.PutUint32(header[4:8], ts)
	binary.BigEndian.PutUint32(header[8:12], ssrc)
	return bytes.Clone(enc.Encrypt(header, payload))
}

// keepaliveMetrics counts the two keepalive recorder calls; the rest of the
// MetricsRecorder surface is inherited no-ops from discardMetrics.
type keepaliveMetrics struct {
	discardMetrics
	mu         sync.Mutex
	sent, errs int
}

func (m *keepaliveMetrics) UDPKeepaliveSent() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent++
}

func (m *keepaliveMetrics) UDPKeepaliveSendError() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.errs++
}

func (m *keepaliveMetrics) counts() (sent, errs int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sent, m.errs
}

func noopDave() godave.Session {
	return godave.NewNoopSession(slog.New(slog.DiscardHandler), "", nil)
}

// newTestUDPConn builds a keepaliveUDPConn whose dial yields the given fake
// conns in order and whose keepalive ticker is the hand-fired ticks channel.
func newTestUDPConn(t *testing.T, rec MetricsRecorder, ticks chan time.Time, conns ...*fakePacketConn) *keepaliveUDPConn {
	t.Helper()
	if rec == nil {
		rec = discardMetrics{}
	}
	u := newKeepaliveUDPConn(noopDave(), func(uint32) snowflake.ID { return 0 }, discardLogger(), rec)
	var dials int
	var mu sync.Mutex
	u.dial = func(context.Context, string, string) (net.Conn, error) {
		mu.Lock()
		defer mu.Unlock()
		c := conns[dials]
		dials++
		return c, nil
	}
	u.newTicker = func(time.Duration) (<-chan time.Time, func()) { return ticks, func() {} }
	return u
}

// open runs Open against a conn pre-loaded with a discovery response and fails
// the test on any mismatch.
func open(t *testing.T, u *keepaliveUDPConn, fc *fakePacketConn, ssrc uint32) {
	t.Helper()
	fc.reads <- discoveryResponse(ssrc, "203.0.113.7", 50000)
	ip, port, err := u.Open(context.Background(), "198.51.100.1", 443, ssrc)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if ip != "203.0.113.7" || port != 50000 {
		t.Fatalf("Open returned %s:%d, want 203.0.113.7:50000", ip, port)
	}
}

func TestKeepaliveUDPConnSendsKeepalives(t *testing.T) {
	t.Parallel()
	rec := &keepaliveMetrics{}
	ticks := make(chan time.Time)
	fc := newFakePacketConn()
	u := newTestUDPConn(t, rec, ticks, fc)
	open(t, u, fc, 42)
	defer u.Close()

	// Write 0 is the discovery request; the loop writes one keepalive
	// immediately after Open, before its first tick.
	if !eventually(func() bool { return fc.writeCount() >= 2 }) {
		t.Fatalf("no keepalive written after Open; %d writes", fc.writeCount())
	}
	if got := fc.write(0); len(got) != 74 {
		t.Fatalf("first write is not the 74-byte discovery request: %d bytes", len(got))
	}
	first := fc.write(1)
	if len(first) != keepalivePacketSize || binary.LittleEndian.Uint64(first) != 0 {
		t.Fatalf("first keepalive = %x, want 8-byte LE counter 0", first)
	}

	ticks <- time.Now()
	if !eventually(func() bool { return fc.writeCount() >= 3 }) {
		t.Fatal("no keepalive written on tick")
	}
	if second := fc.write(2); binary.LittleEndian.Uint64(second) != 1 {
		t.Fatalf("second keepalive counter = %d, want 1", binary.LittleEndian.Uint64(second))
	}
	if !eventually(func() bool { sent, _ := rec.counts(); return sent >= 2 }) {
		sent, _ := rec.counts()
		t.Fatalf("keepalive metric = %d, want >= 2", sent)
	}
}

func TestKeepaliveUDPConnCloseStopsKeepaliveLoop(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	ticks := make(chan time.Time)
	fc := newFakePacketConn()
	u := newTestUDPConn(t, nil, ticks, fc)
	open(t, u, fc, 42)
	if !eventually(func() bool { return fc.writeCount() >= 2 }) {
		t.Fatal("keepalive loop never started")
	}
	if err := u.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// goleak (deferred) proves the loop goroutine exited; the closed socket
	// also fails any straggler write, so the count settles.
}

func TestKeepaliveUDPConnReopenRetiresOldSocket(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	ticks := make(chan time.Time)
	fc1, fc2 := newFakePacketConn(), newFakePacketConn()
	u := newTestUDPConn(t, nil, ticks, fc1, fc2)
	open(t, u, fc1, 42)
	if err := u.SetSecretKey(voice.EncryptionModeAEADAES256GCMRTPSize, testSecretKey); err != nil {
		t.Fatalf("SetSecretKey: %v", err)
	}

	// Land one voice packet so the counter is non-zero before the re-Open.
	fc1.reads <- sealRTP(t, 1, 960, 7, []byte{0x01, 0x02, 0x03, 0x04})
	if _, err := u.ReadPacket(); err != nil {
		t.Fatalf("ReadPacket: %v", err)
	}
	if got := u.packets.Load(); got != 1 {
		t.Fatalf("packets = %d, want 1", got)
	}

	// A reader parked in a deadline-less Read on the old socket — the exact
	// goroutine disgo's stock transport strands forever on a re-Open. Wait for
	// it to actually park on fc1 before re-opening, or it would snapshot the
	// NEW conn and the test would race itself.
	readErr := make(chan error, 1)
	go func() {
		_, err := u.ReadPacket()
		readErr <- err
	}()
	if !eventually(func() bool { return fc1.readers.Load() == 1 }) {
		t.Fatal("reader never parked on the old socket")
	}

	open(t, u, fc2, 42)
	defer u.terminate()
	if got := u.packets.Load(); got != 0 {
		t.Fatalf("packets after re-Open = %d, want 0 (fresh path, fresh count)", got)
	}
	if err := u.SetSecretKey(voice.EncryptionModeAEADAES256GCMRTPSize, testSecretKey); err != nil {
		t.Fatalf("SetSecretKey after re-Open: %v", err)
	}

	// The parked reader must neither stay stranded on the old socket nor
	// surface its close as an error (disgo's receiver reaps itself for good on
	// net.ErrClosed): once the old socket is retired it carries on with the
	// new one and returns the next packet from there.
	fc2.reads <- sealRTP(t, 2, 1920, 7, []byte{0x05, 0x06, 0x07, 0x08})
	select {
	case err := <-readErr:
		if err != nil {
			t.Fatalf("parked read returned %v, want the packet from the new socket", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("re-Open left the old socket's reader parked (the disgo zombie this transport exists to fix)")
	}
	if !eventually(func() bool { return fc2.writeCount() >= 2 }) {
		t.Fatal("no keepalive on the fresh socket after re-Open")
	}
}

func TestKeepaliveUDPConnCountsOnlyParsedRTP(t *testing.T) {
	t.Parallel()
	ticks := make(chan time.Time)
	fc := newFakePacketConn()
	u := newTestUDPConn(t, nil, ticks, fc)
	open(t, u, fc, 42)
	defer u.Close()
	if err := u.SetSecretKey(voice.EncryptionModeAEADAES256GCMRTPSize, testSecretKey); err != nil {
		t.Fatalf("SetSecretKey: %v", err)
	}

	payload := []byte{0x01, 0x02, 0x03, 0x04}
	fc.reads <- make([]byte, keepalivePacketSize) // keepalive echo: shorter than an RTP header
	fc.reads <- func() []byte {                   // right length, wrong payload type
		b := rtpPacket(9, 960, 7, payload)
		b[1] = 0xC8
		return b
	}()
	fc.reads <- sealRTP(t, 9, 960, 7, payload)

	p, err := u.ReadPacket()
	if err != nil {
		t.Fatalf("ReadPacket: %v", err)
	}
	if !bytes.Equal(p.Opus, payload) {
		t.Fatalf("Opus = %x, want %x", p.Opus, payload)
	}
	if p.SSRC != 7 || p.Sequence != 9 {
		t.Fatalf("parsed SSRC/seq = %d/%d, want 7/9", p.SSRC, p.Sequence)
	}
	if got := u.packets.Load(); got != 1 {
		t.Fatalf("packets = %d, want 1 (echo and non-voice packets must not count)", got)
	}
	if got := u.echoes.Load(); got != 1 {
		t.Fatalf("echoes = %d, want 1 (the 8-byte datagram is a keepalive echo)", got)
	}
}

func TestKeepaliveUDPConnRefusesOpenAfterTerminate(t *testing.T) {
	t.Parallel()
	ticks := make(chan time.Time)
	fc := newFakePacketConn()
	u := newTestUDPConn(t, nil, ticks, fc)
	open(t, u, fc, 42)
	u.terminate()
	// A straggler voice-gateway Ready dispatched during teardown must not
	// revive the transport with a socket and keepalive loop nothing will stop.
	if _, _, err := u.Open(context.Background(), "198.51.100.1", 443, 42); err == nil {
		t.Fatal("Open succeeded on a terminated transport")
	}
}

// rtpWrites counts the RTP voice packets a fake socket has been sent, ignoring
// the 74-byte discovery request and the 8-byte keepalives.
func rtpWrites(c *fakePacketConn) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	var n int
	for _, w := range c.writes {
		if len(w) > voice.RTPHeaderSize && len(w) != 74 && w[1] == voice.RTPPayloadType {
			n++
		}
	}
	return n
}

// TestKeepaliveUDPConnCloseIsAPause pins the lifecycle disgo v0.20.0's in-place
// recovery needs: its conn Closes the transport and re-Opens it for a fresh
// voice session without leaving the channel. Neither audio direction may see
// an error across that gap — disgo's sender and receiver both reap themselves
// permanently on net.ErrClosed — and nothing may be sent with the previous
// session's key.
func TestKeepaliveUDPConnCloseIsAPause(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	ticks := make(chan time.Time)
	fc1, fc2 := newFakePacketConn(), newFakePacketConn()
	u := newTestUDPConn(t, nil, ticks, fc1, fc2)
	open(t, u, fc1, 42)
	defer u.terminate()
	if err := u.SetSecretKey(voice.EncryptionModeAEADAES256GCMRTPSize, testSecretKey); err != nil {
		t.Fatalf("SetSecretKey: %v", err)
	}
	frame := []byte{0x01, 0x02, 0x03, 0x04}
	if _, err := u.Write(frame); err != nil {
		t.Fatalf("Write on the open transport: %v", err)
	}
	if got := rtpWrites(fc1); got != 1 {
		t.Fatalf("RTP packets on the first socket = %d, want 1", got)
	}

	type read struct {
		p   *voice.Packet
		err error
	}
	reads := make(chan read, 1)
	go func() {
		p, err := u.ReadPacket()
		reads <- read{p, err}
	}()
	if !eventually(func() bool { return fc1.readers.Load() == 1 }) {
		t.Fatal("reader never parked on the first socket")
	}

	if err := u.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case r := <-reads:
		t.Fatalf("ReadPacket returned (%v, %v) on a paused transport; it must wait for the re-Open", r.p, r.err)
	case <-time.After(50 * time.Millisecond):
	}
	if n, err := u.Write(frame); err != nil || n != len(frame) {
		t.Fatalf("Write on a paused transport = (%d, %v), want the frame dropped as written", n, err)
	}
	if err := u.SetReadDeadline(time.Now()); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("SetReadDeadline on a paused transport = %v, want net.ErrClosed", err)
	}
	if u.LocalAddr() != nil || u.RemoteAddr() != nil {
		t.Fatal("a paused transport reported a socket address")
	}

	open(t, u, fc2, 43)
	// The new session's key has not arrived: sealing with the old one would
	// put undecryptable packets on the new path.
	if n, err := u.Write(frame); err != nil || n != len(frame) {
		t.Fatalf("Write before the new session key = (%d, %v), want the frame dropped as written", n, err)
	}
	if got := rtpWrites(fc2); got != 0 {
		t.Fatalf("RTP packets on the new socket before its key = %d, want 0", got)
	}
	if err := u.SetSecretKey(voice.EncryptionModeAEADAES256GCMRTPSize, testSecretKey); err != nil {
		t.Fatalf("SetSecretKey after re-Open: %v", err)
	}

	if _, err := u.Write(frame); err != nil {
		t.Fatalf("Write after the re-Open: %v", err)
	}
	if got := rtpWrites(fc2); got != 1 {
		t.Fatalf("RTP packets on the new socket = %d, want 1", got)
	}
	sent := fc2.write(fc2.writeCount() - 1)
	if ssrc := binary.BigEndian.Uint32(sent[8:12]); ssrc != 43 {
		t.Fatalf("outbound SSRC after the re-Open = %d, want the new session's 43", ssrc)
	}
	// Three frames were dropped across the gap; the sequence still advanced for
	// each, so the far side sees loss, not a rewound stream.
	if seq := binary.BigEndian.Uint16(sent[2:4]); seq != 3 {
		t.Fatalf("outbound sequence after the gap = %d, want 3", seq)
	}

	payload := []byte{0x09, 0x0a, 0x0b, 0x0c}
	fc2.reads <- sealRTP(t, 7, 960, 9, payload)
	select {
	case r := <-reads:
		if r.err != nil {
			t.Fatalf("ReadPacket across the pause: %v", r.err)
		}
		if !bytes.Equal(r.p.Opus, payload) {
			t.Fatalf("Opus = %x, want %x from the new socket", r.p.Opus, payload)
		}
	case <-time.After(time.Second):
		t.Fatal("the reader never resumed on the re-opened transport")
	}
}

// TestKeepaliveUDPConnTerminateEndsBothDirections pins the other half of the
// lifecycle: once the conn is discarded, the blocked reader and the next write
// get net.ErrClosed — the signal disgo's audio goroutines reap on, and what
// Session.Close's sender poke depends on (#579).
func TestKeepaliveUDPConnTerminateEndsBothDirections(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	ticks := make(chan time.Time)
	fc := newFakePacketConn()
	u := newTestUDPConn(t, nil, ticks, fc)
	open(t, u, fc, 42)
	if err := u.SetSecretKey(voice.EncryptionModeAEADAES256GCMRTPSize, testSecretKey); err != nil {
		t.Fatalf("SetSecretKey: %v", err)
	}

	// A reader waiting out a pause is the one a plain socket close cannot wake.
	if err := u.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	readErr := make(chan error, 1)
	go func() {
		_, err := u.ReadPacket()
		readErr <- err
	}()

	u.terminate()
	u.terminate() // idempotent

	select {
	case err := <-readErr:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("ReadPacket after terminate = %v, want net.ErrClosed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("terminate left a paused reader blocked")
	}
	if _, err := u.Write([]byte{0x01, 0x02, 0x03, 0x04}); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("Write after terminate = %v, want net.ErrClosed", err)
	}
}

func TestKeepaliveUDPConnCountsUndecryptableRTP(t *testing.T) {
	t.Parallel()
	ticks := make(chan time.Time)
	fc := newFakePacketConn()
	u := newTestUDPConn(t, nil, ticks, fc)
	open(t, u, fc, 42)
	defer u.Close()
	// A real AEAD encrypter, so garbage ciphertext fails transport decryption.
	if err := u.SetSecretKey(voice.EncryptionModeAEADAES256GCMRTPSize, make([]byte, 32)); err != nil {
		t.Fatalf("SetSecretKey: %v", err)
	}

	fc.reads <- rtpPacket(9, 960, 7, []byte("definitely not valid AES-GCM"))
	_, err := u.ReadPacket()
	if err == nil || !strings.Contains(err.Error(), "failed to decrypt packet") {
		t.Fatalf("ReadPacket error = %v, want transport decrypt failure", err)
	}
	// The count stamps BEFORE decryption: a broken key-roll still proves the
	// media path is alive, which is what the watchdog needs to know.
	if got := u.packets.Load(); got != 1 {
		t.Fatalf("packets = %d, want 1 (undecryptable RTP still proves liveness)", got)
	}
}

func TestKeepaliveUDPConnKeepaliveErrorCountsAndContinues(t *testing.T) {
	t.Parallel()
	rec := &keepaliveMetrics{}
	ticks := make(chan time.Time)
	fc := newFakePacketConn()
	u := newTestUDPConn(t, rec, ticks, fc)
	open(t, u, fc, 42)
	defer u.Close()
	if !eventually(func() bool { return fc.writeCount() >= 2 }) {
		t.Fatal("keepalive loop never started")
	}

	// A transient (non-ErrClosed) write failure must count the error metric and
	// keep the loop alive for the next tick.
	fc.setFailWrite(true)
	ticks <- time.Now()
	if !eventually(func() bool { _, errs := rec.counts(); return errs == 1 }) {
		_, errs := rec.counts()
		t.Fatalf("keepalive error metric = %d, want 1", errs)
	}

	fc.setFailWrite(false)
	before := fc.writeCount()
	ticks <- time.Now()
	if !eventually(func() bool { return fc.writeCount() > before }) {
		t.Fatal("keepalive loop died on a transient write error instead of continuing")
	}
}

// TestKeepaliveUDPConnReadsPayloadsLargerThanInitialBuffer pins the buffer-length
// contract: the DAVE session copies into out[:len(out)], so the 512-byte initial
// buffer must be re-sized by LENGTH for larger Opus payloads (high-bitrate
// channels), not merely grown in capacity as the upstream disgo code does — that
// silently truncated every payload over 512 bytes.
func TestKeepaliveUDPConnReadsPayloadsLargerThanInitialBuffer(t *testing.T) {
	t.Parallel()
	ticks := make(chan time.Time)
	fc := newFakePacketConn()
	u := newTestUDPConn(t, nil, ticks, fc)
	open(t, u, fc, 42)
	defer u.Close()
	if err := u.SetSecretKey(voice.EncryptionModeAEADAES256GCMRTPSize, testSecretKey); err != nil {
		t.Fatalf("SetSecretKey: %v", err)
	}

	for i, size := range []int{513, 700, 960} {
		payload := make([]byte, size)
		for j := range payload {
			payload[j] = byte(j)
		}
		fc.reads <- sealRTP(t, uint16(9+i), 960, 7, payload)
		p, err := u.ReadPacket()
		if err != nil {
			t.Fatalf("ReadPacket(%d bytes): %v", size, err)
		}
		if !bytes.Equal(p.Opus, payload) {
			t.Fatalf("payload of %d bytes: got %d bytes back, want the whole payload", size, len(p.Opus))
		}
	}
}

// sealPaddedRTP is sealRTP with the RTP padding bit set and pad octets of
// padding appended to the payload INSIDE the encrypted body, the final octet
// carrying the count — the layout RFC 3550 §5.1 describes and Discord uses.
func sealPaddedRTP(t *testing.T, seq uint16, ts, ssrc uint32, payload []byte, pad int) []byte {
	t.Helper()
	enc, err := voice.NewEncrypter(voice.EncryptionModeAEADAES256GCMRTPSize, testSecretKey)
	if err != nil {
		t.Fatalf("NewEncrypter: %v", err)
	}
	var header [voice.RTPHeaderSize]byte
	header[0] = voice.RTPVersionPadExtend | rtpPaddingBit
	header[1] = voice.RTPPayloadType
	binary.BigEndian.PutUint16(header[2:4], seq)
	binary.BigEndian.PutUint32(header[4:8], ts)
	binary.BigEndian.PutUint32(header[8:12], ssrc)
	body := bytes.Clone(payload)
	if pad > 0 {
		body = append(body, make([]byte, pad)...)
		body[len(body)-1] = byte(pad)
	}
	return bytes.Clone(enc.Encrypt(header, body))
}

// TestKeepaliveUDPConnStripsRTPPadding pins the upstream disgo v0.20.0 fix this
// reimplementation carries: the padding flag is bit 0x20 of the first header
// byte (the pinned code tested 0x04, a CSRC-count bit), and the padding sits at
// the end of the ENCRYPTED payload, so it can only be measured after transport
// decryption — the pinned code read the count from the last wire byte, which
// under the rtpsize AEAD modes is the nonce.
func TestKeepaliveUDPConnStripsRTPPadding(t *testing.T) {
	t.Parallel()
	ticks := make(chan time.Time)
	fc := newFakePacketConn()
	u := newTestUDPConn(t, nil, ticks, fc)
	open(t, u, fc, 42)
	defer u.Close()
	if err := u.SetSecretKey(voice.EncryptionModeAEADAES256GCMRTPSize, testSecretKey); err != nil {
		t.Fatalf("SetSecretKey: %v", err)
	}

	payload := []byte{0x01, 0x02, 0x03, 0x04}
	fc.reads <- sealPaddedRTP(t, 9, 960, 7, payload, 6)
	p, err := u.ReadPacket()
	if err != nil {
		t.Fatalf("ReadPacket: %v", err)
	}
	if !bytes.Equal(p.Opus, payload) {
		t.Fatalf("Opus = %x, want %x (padding must be stripped)", p.Opus, payload)
	}

	// A padding-only packet carries no media: it is skipped, not delivered as
	// an empty frame, and the next real packet is what ReadPacket returns.
	fc.reads <- sealPaddedRTP(t, 10, 960, 7, nil, 8)
	fc.reads <- sealRTP(t, 11, 1920, 7, payload)
	p, err = u.ReadPacket()
	if err != nil {
		t.Fatalf("ReadPacket after padding-only packet: %v", err)
	}
	if p.Sequence != 11 || !bytes.Equal(p.Opus, payload) {
		t.Fatalf("got seq %d payload %x, want seq 11 payload %x", p.Sequence, p.Opus, payload)
	}
	if got := u.packets.Load(); got != 3 {
		t.Fatalf("packets = %d, want 3 (a padding-only packet still proves liveness)", got)
	}
}

func TestStripRTPPadding(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		in, want []byte
	}{
		{"empty", nil, nil},
		{"count covers the tail", []byte{1, 2, 3, 0, 0, 3}, []byte{1, 2, 3}},
		{"count is the whole payload", []byte{0, 0, 3}, []byte{}},
		{"zero count is malformed: unchanged", []byte{1, 2, 0}, []byte{1, 2, 0}},
		{"count past the payload is malformed: unchanged", []byte{1, 2, 9}, []byte{1, 2, 9}},
	} {
		if got := stripRTPPadding(tc.in); !bytes.Equal(got, tc.want) {
			t.Errorf("%s: stripRTPPadding(%v) = %v, want %v", tc.name, tc.in, got, tc.want)
		}
	}
}
