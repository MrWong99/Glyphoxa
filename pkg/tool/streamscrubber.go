package tool

import "strings"

// pseudoMarker is the opening literal a streaming pseudo-call begins with. Once
// the stream produces it in full, a pseudo-call has started and the scrubber
// swallows the rest of the round (extraction happens off the accumulated batch
// text, so nothing spoken is lost that the batch scrub would keep).
const pseudoMarker = "<function="

// scrubHoldbackCap bounds how many bytes the scrubber will withhold while a run
// still looks like it could become [pseudoMarker]. A run this long that has not
// completed the marker cannot be one (the marker is short), so it is flushed as
// prose rather than buffered unbounded.
const scrubHoldbackCap = 4096

// streamScrubber suppresses pseudo-XML tool-call syntax from a token stream on
// the way to TTS (issue #410). It is the streaming counterpart of
// [ExtractPseudoCalls]: as deltas arrive it forwards prose to out but withholds
// any tail that is still a prefix of [pseudoMarker]; once the full marker
// appears it swallows the remainder of the round. It NEVER parses or executes —
// recovery is driven off the batch-scrubbed accumulated text so there is a
// single source of truth and no dual-parse drift.
type streamScrubber struct {
	// out receives prose the scrubber has cleared for delivery. Never nil when
	// the scrubber is used.
	out func(string) error

	// held is the current trailing run that is still a proper prefix of
	// pseudoMarker and therefore withheld pending more input.
	held string

	// swallowing is set once the full marker has been seen; all further input in
	// the round is dropped.
	swallowing bool
}

// Write feeds one stream delta through the scrubber, forwarding cleared prose to
// out. It returns the first error out returns (a downstream barge-in cancel).
func (sc *streamScrubber) Write(delta string) error {
	if sc.swallowing {
		return nil
	}
	s := sc.held + delta
	sc.held = ""

	for len(s) > 0 {
		idx := strings.IndexByte(s, '<')
		if idx < 0 {
			return sc.emit(s)
		}
		if idx > 0 {
			if err := sc.emit(s[:idx]); err != nil {
				return err
			}
			s = s[idx:]
		}

		// s now starts with '<'. How much of it matches the marker?
		n := commonPrefixLen(s, pseudoMarker)
		switch {
		case n == len(pseudoMarker):
			// Full marker: a pseudo-call has started — swallow the rest.
			sc.swallowing = true
			return nil
		case n == len(s):
			// s is a proper prefix of the marker and we consumed all input.
			if len(s) >= scrubHoldbackCap {
				return sc.emit(s) // too long to ever be the marker — flush.
			}
			sc.held = s
			return nil
		default:
			// Diverges at s[n]: this '<' did not begin a marker. Emit the '<'
			// and rescan from the next byte.
			if err := sc.emit(s[:1]); err != nil {
				return err
			}
			s = s[1:]
		}
	}
	return nil
}

// Flush releases any withheld partial marker as prose (it never completed, so it
// was ordinary text) and resets. Call it at the end of a round.
func (sc *streamScrubber) Flush() error {
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
