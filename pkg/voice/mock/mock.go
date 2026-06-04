// Package mock provides hand-written fakes for the unexported disgo seam
// interfaces in pkg/voice, so the wrapper's unit tests run without a live
// Discord token. The fakes live in their own package and are wired into voice's
// internal constructors via test-only adapters declared in voice's _test files.
//
// They implement the same method sets as voice's voiceManager/voiceConn seam
// (over disgo's voice.OpusFrameProvider / voice.OpusFrameReceiver), which is the
// only contract a test needs: install a provider/receiver, then drive frames
// through them as disgo's audio goroutines would.
package mock

import (
	"context"
	"sync"

	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/snowflake/v2"
)

// Manager is a fake disgo voice manager. It hands out a [Conn] per Guild,
// recording every connection it creates so tests can drive them.
type Manager struct {
	// NewConn, if set, builds the Conn for a guild; defaults to a ready Conn.
	NewConn func(guild snowflake.ID) *Conn
	// OpenErr, if set, is returned by every Conn.Open this manager creates.
	OpenErr error

	mu      sync.Mutex
	conns   map[snowflake.ID]*Conn
	removed []snowflake.ID
}

// NewManager returns a fake manager with ready connections.
func NewManager() *Manager {
	return &Manager{conns: make(map[snowflake.ID]*Conn)}
}

// CreateConn returns the existing Conn for guild or builds a fresh one.
func (m *Manager) CreateConn(guild snowflake.ID) *Conn {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c, ok := m.conns[guild]; ok {
		return c
	}
	var c *Conn
	if m.NewConn != nil {
		c = m.NewConn(guild)
	} else {
		c = NewConn()
	}
	if m.OpenErr != nil {
		c.OpenErr = m.OpenErr
	}
	m.conns[guild] = c
	return c
}

// RemoveConn records the removal and forgets the Conn.
func (m *Manager) RemoveConn(guild snowflake.ID) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.conns, guild)
	m.removed = append(m.removed, guild)
}

// Conn returns the fake Conn created for guild, if any.
func (m *Manager) Conn(guild snowflake.ID) (*Conn, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.conns[guild]
	return c, ok
}

// Removed returns the guilds passed to RemoveConn, in order.
func (m *Manager) Removed() []snowflake.ID {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]snowflake.ID(nil), m.removed...)
}

// Conn is a fake disgo voice connection. It captures the provider/receiver the
// Session installs and counts lifecycle calls; tests drive playback and inbound
// audio through [Conn.PullFrame] and [Conn.PushPacket].
type Conn struct {
	// OpenErr, if set, makes Open fail (no handlers are then installed).
	OpenErr error

	mu       sync.Mutex
	opened   bool
	closed   bool
	provider voice.OpusFrameProvider
	receiver voice.OpusFrameReceiver
	channel  snowflake.ID
}

// NewConn returns a fake connection that opens successfully.
func NewConn() *Conn { return &Conn{} }

// Open records the join and (when no OpenErr is set) marks the conn ready.
func (c *Conn) Open(_ context.Context, channel snowflake.ID, _, _ bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.OpenErr != nil {
		return c.OpenErr
	}
	c.opened = true
	c.channel = channel
	return nil
}

// SetOpusFrameProvider captures the outbound provider the Session installs.
func (c *Conn) SetOpusFrameProvider(p voice.OpusFrameProvider) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.provider = p
}

// SetOpusFrameReceiver captures the inbound receiver the Session installs.
func (c *Conn) SetOpusFrameReceiver(r voice.OpusFrameReceiver) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.receiver = r
}

// Close marks the conn closed; like disgo's Conn.Close it does NOT call the
// provider/receiver Close — the Session is responsible for that.
func (c *Conn) Close(context.Context) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
}

// Closed reports whether Close has been called.
func (c *Conn) Closed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

// PullFrame asks the installed provider for one outbound frame, standing in for
// disgo's 20ms sender tick. It returns the frame and provider error, or a nil
// frame when no provider is installed.
func (c *Conn) PullFrame() ([]byte, error) {
	c.mu.Lock()
	p := c.provider
	c.mu.Unlock()
	if p == nil {
		return nil, nil
	}
	return p.ProvideOpusFrame()
}

// PushPacket delivers one inbound packet to the installed receiver, standing in
// for disgo's receiver goroutine (which has already resolved SSRC→userID).
func (c *Conn) PushPacket(userID snowflake.ID, packet *voice.Packet) error {
	c.mu.Lock()
	r := c.receiver
	c.mu.Unlock()
	if r == nil {
		return nil
	}
	return r.ReceiveOpusFrame(userID, packet)
}
