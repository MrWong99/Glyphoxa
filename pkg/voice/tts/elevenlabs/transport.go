package elevenlabs

import (
	"fmt"
	"strings"
)

// Transport names how [Client.Synthesize] reaches the API.
type Transport string

const (
	// TransportHTTP is one POST /v1/text-to-speech/{voice}/stream per sentence
	// on a kept-warm connection. The default.
	TransportHTTP Transport = "http"

	// TransportWS routes sentences of the v3/v4 model families over pooled
	// Text-to-Dialogue websockets (see [wsPool]); every other model stays on
	// HTTP, because the dialogue socket refuses it (and the plain TTS socket
	// refuses v3/v4). Opt-in, for in-pipeline comparison against HTTP (#702).
	TransportWS Transport = "ws"
)

// ParseTransport reads a transport name as an operator writes it
// (GLYPHOXA_TTS_TRANSPORT): blank or "http" is [TransportHTTP], "ws" is
// [TransportWS], case and surrounding space ignored. Anything else is an error
// and reports [TransportHTTP], so a typo can never switch the transport.
func ParseTransport(s string) (Transport, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", string(TransportHTTP):
		return TransportHTTP, nil
	case string(TransportWS):
		return TransportWS, nil
	}
	return TransportHTTP, fmt.Errorf("elevenlabs: unknown TTS transport %q (want %q or %q)", s, TransportHTTP, TransportWS)
}

// WithTransport selects the synthesis transport. [TransportHTTP] (and any
// unknown value) leaves the client exactly as it is without the option; this is
// the ONE place the websocket transport is switched on. A client built with
// [TransportWS] holds sockets and must be released with [Client.Close].
func WithTransport(t Transport) Option {
	return func(c *Client) {
		if t == TransportWS {
			c.ws = newWSPool(c)
			return
		}
		c.ws = nil
	}
}

// Transport reports the transport the client was built with.
func (c *Client) Transport() Transport {
	if c.ws != nil {
		return TransportWS
	}
	return TransportHTTP
}

// dialogueFamily reports whether model belongs to the eleven_v3 / eleven_v4
// families (eleven_v3, eleven_v3_conversational, eleven_v4, eleven_v4_turbo):
// the models that render inline audio tags and the only ones the
// Text-to-Dialogue websocket accepts.
func dialogueFamily(model string) bool {
	return strings.HasPrefix(model, "eleven_v3") || strings.HasPrefix(model, "eleven_v4")
}
