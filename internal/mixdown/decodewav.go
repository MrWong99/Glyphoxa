package mixdown

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
)

// replayChunkMillis is the target chunk duration DecodeWAV slices the clip into,
// mirroring a synthesizer's provider-native window: ~100 ms of audio per
// [tts.AudioChunk] so the outbound playback path (#310 Highlight voice replay) is
// fed at the same granularity as live TTS. The last chunk may be shorter.
const replayChunkMillis = 100

// ErrNotPCM16WAV is returned when the bytes are not a mono 16-bit PCM WAV — the
// exact shape [WAVClip] (and so a stored Highlight clip) produces. DecodeWAV is
// deliberately strict: it is the inverse of [encodeWAV], not a general WAV reader.
var ErrNotPCM16WAV = errors.New("mixdown: not a mono 16-bit PCM WAV clip")

// DecodeWAV parses a mono 16-bit PCM WAV clip (the exact output of [encodeWAV],
// i.e. a stored Session Highlight clip) into ~100 ms [tts.AudioChunk] windows for
// replay through the outbound playback path (#310, ADR-0005: the clip bytes arrive
// via the blob seam, this turns them back into playable chunks). It reads the
// clip's own sample rate from the header and stamps each chunk with it and
// Channels:1. A clip that is not mono PCM16, or is truncated, is [ErrNotPCM16WAV].
func DecodeWAV(wav []byte) ([]tts.AudioChunk, error) {
	// Canonical 44-byte RIFF/WAVE header (the encodeWAV layout).
	if len(wav) < 44 {
		return nil, fmt.Errorf("%w: header truncated (%d bytes)", ErrNotPCM16WAV, len(wav))
	}
	if string(wav[0:4]) != "RIFF" || string(wav[8:12]) != "WAVE" || string(wav[12:16]) != "fmt " {
		return nil, fmt.Errorf("%w: bad RIFF/WAVE/fmt magic", ErrNotPCM16WAV)
	}
	audioFormat := binary.LittleEndian.Uint16(wav[20:22])
	numChannels := binary.LittleEndian.Uint16(wav[22:24])
	sampleRate := binary.LittleEndian.Uint32(wav[24:28])
	bitsPerSample := binary.LittleEndian.Uint16(wav[34:36])
	if audioFormat != 1 || numChannels != 1 || bitsPerSample != 16 {
		return nil, fmt.Errorf("%w: format=%d channels=%d bits=%d", ErrNotPCM16WAV, audioFormat, numChannels, bitsPerSample)
	}
	if string(wav[36:40]) != "data" {
		return nil, fmt.Errorf("%w: missing data chunk", ErrNotPCM16WAV)
	}
	dataSize := int(binary.LittleEndian.Uint32(wav[40:44]))
	if 44+dataSize > len(wav) {
		return nil, fmt.Errorf("%w: data truncated (declared %d, have %d)", ErrNotPCM16WAV, dataSize, len(wav)-44)
	}
	if dataSize%2 != 0 {
		return nil, fmt.Errorf("%w: odd data length %d for 16-bit samples", ErrNotPCM16WAV, dataSize)
	}
	pcm := wav[44 : 44+dataSize]

	// ~100 ms per chunk: bytesPerChunk = rate * 0.1 s * 2 bytes/sample, rounded to a
	// whole sample (even byte count).
	bytesPerChunk := int(sampleRate) * replayChunkMillis / 1000 * 2
	if bytesPerChunk <= 0 {
		bytesPerChunk = 2
	}

	var chunks []tts.AudioChunk
	for off := 0; off < len(pcm); off += bytesPerChunk {
		end := off + bytesPerChunk
		if end > len(pcm) {
			end = len(pcm)
		}
		// Copy so a chunk never aliases the caller's WAV buffer.
		buf := make([]byte, end-off)
		copy(buf, pcm[off:end])
		chunks = append(chunks, tts.AudioChunk{
			PCM:        buf,
			SampleRate: int(sampleRate),
			Channels:   1,
		})
	}
	return chunks, nil
}
