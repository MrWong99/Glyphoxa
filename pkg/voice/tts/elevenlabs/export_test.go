package elevenlabs

import "time"

// WithWSTuning overrides the websocket pool's timing and caps so tests need not
// wait out the production values; a zero leaves that value as it is. It must
// come after WithTransport(TransportWS).
func WithWSTuning(keepAlive, idleTTL, firstAudio time.Duration, perKey, total int) Option {
	return func(c *Client) {
		if c.ws == nil {
			return
		}
		t := &c.ws.tune
		if keepAlive > 0 {
			t.keepAlive = keepAlive
		}
		if idleTTL > 0 {
			t.idleTTL = idleTTL
		}
		if firstAudio > 0 {
			t.firstAudio = firstAudio
		}
		if perKey > 0 {
			t.perKey = perKey
		}
		if total > 0 {
			t.total = total
		}
	}
}

// WithWSCapBackoff overrides how long the pool stops dialling after the API
// refused a socket for the account's dialogue-session cap. It must come after
// WithTransport(TransportWS).
func WithWSCapBackoff(d time.Duration) Option {
	return func(c *Client) {
		if c.ws != nil {
			c.ws.tune.capBackoff = d
		}
	}
}

// WithWSWarmFor overrides how long after its last sentence Warm keeps a voice's
// socket lined up. It must come after WithTransport(TransportWS).
func WithWSWarmFor(d time.Duration) Option {
	return func(c *Client) {
		if c.ws != nil {
			c.ws.tune.warmFor = d
		}
	}
}

// WaitWSBackground returns once the websocket pool's goroutines are done. Only
// meaningful while no socket is open (a socket's goroutines run until it
// closes): then what is left are background pre-dials.
func WaitWSBackground(c *Client) {
	if c.ws != nil {
		c.ws.wg.Wait()
	}
}
