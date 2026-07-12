package tool

import "strings"

// streamScrubber suppresses pseudo-XML tool-call syntax from a token stream on
// the way to TTS (issue #410). It is the streaming counterpart of
// [ExtractPseudoCalls]: as deltas arrive it forwards prose to out but withholds
// any tail that is still a prefix of [pseudoMarker]; once the full marker
// appears it swallows through the matching [pseudoClose] and then RESUMES
// forwarding, so trailing prose after the call is still spoken (batch parity). It
// NEVER parses or executes — recovery is driven off the batch-scrubbed
// accumulated text so there is a single source of truth and no dual-parse drift.
type streamScrubber struct {
	// out receives prose the scrubber has cleared for delivery. Never nil when
	// the scrubber is used.
	out func(string) error

	// held is the current trailing run that is still a proper prefix of
	// pseudoMarker and therefore withheld pending more input.
	held string

	// swallowing is set between a completed pseudoMarker and its pseudoClose; all
	// input in that span is dropped.
	swallowing bool

	// closeHeld is, while swallowing, the trailing run that is still a prefix of
	// pseudoClose (so the closer can be detected across delta boundaries).
	closeHeld string
}

// Write feeds one stream delta through the scrubber, forwarding cleared prose to
// out. It returns the first error out returns (a downstream barge-in cancel).
func (sc *streamScrubber) Write(delta string) error {
	s := delta
	for {
		if sc.swallowing {
			s = sc.closeHeld + s
			sc.closeHeld = ""
			k := strings.Index(s, pseudoClose)
			if k < 0 {
				// Closer not seen yet: keep only the tail that could still begin
				// it, drop the rest.
				sc.closeHeld = tailPrefixOf(s, pseudoClose)
				return nil
			}
			// Drop through the closer and resume forwarding the remainder.
			s = s[k+len(pseudoClose):]
			sc.swallowing = false
			continue
		}

		done, rest, err := sc.writeProse(sc.held + s)
		if err != nil {
			return err
		}
		if done {
			return nil
		}
		// writeProse hit a complete opener: reprocess the remainder in swallow mode.
		s = rest
	}
}

// writeProse forwards prose from s, withholding a trailing partial opener. It
// returns done=true when s is fully consumed (sc.held may hold a partial opener);
// done=false when a complete [pseudoMarker] was seen, with rest = the text after
// the marker to reprocess in swallow mode (sc.swallowing is then set).
func (sc *streamScrubber) writeProse(s string) (done bool, rest string, err error) {
	sc.held = ""
	for len(s) > 0 {
		idx := strings.IndexByte(s, '<')
		if idx < 0 {
			return true, "", sc.emit(s)
		}
		if idx > 0 {
			if err := sc.emit(s[:idx]); err != nil {
				return true, "", err
			}
			s = s[idx:]
		}

		// s now starts with '<'. How much of it matches the marker?
		n := commonPrefixLen(s, pseudoMarker)
		switch {
		case n == len(pseudoMarker):
			sc.swallowing = true
			return false, s[len(pseudoMarker):], nil
		case n == len(s):
			// Proper prefix of the marker (≤ 9 bytes) — hold pending more input.
			sc.held = s
			return true, "", nil
		default:
			// Diverges at s[n]: this '<' did not begin a marker. Emit the '<'
			// and rescan from the next byte.
			if err := sc.emit(s[:1]); err != nil {
				return true, "", err
			}
			s = s[1:]
		}
	}
	return true, "", nil
}

// Flush releases any withheld partial opener as prose (it never completed, so it
// was ordinary text) and resets. A still-open swallow (a pseudo-call the stream
// cut off before its closer) drops its residue. Call it at the end of a round.
func (sc *streamScrubber) Flush() error {
	sc.closeHeld = ""
	sc.swallowing = false
	if sc.held == "" {
		return nil
	}
	rem := sc.held
	sc.held = ""
	return sc.emit(rem)
}

func (sc *streamScrubber) emit(s string) error {
	if s == "" {
		return nil
	}
	return sc.out(s)
}

// commonPrefixLen returns the length of the longest common prefix of a and b.
func commonPrefixLen(a, b string) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}

// tailPrefixOf returns the longest suffix of s that is a prefix of marker (the
// bytes worth keeping to detect marker across a delta boundary). "" if none.
func tailPrefixOf(s, marker string) string {
	start := len(s) - len(marker)
	if start < 0 {
		start = 0
	}
	for i := start; i < len(s); i++ {
		if strings.HasPrefix(marker, s[i:]) {
			return s[i:]
		}
	}
	return ""
}
