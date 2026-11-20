package dsp

// Buffered reports how many samples are held but not yet emitted — what the
// Reframer tests assert the carried-over remainder against.
func (r *Reframer) Buffered() int { return len(r.buf) }
