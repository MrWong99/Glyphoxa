// Package voiceprobe is a synthetic Player for live voice testing: a second
// Discord bot that joins a voice channel, speaks audio clips into it, and
// records whatever the Glyphoxa Bot (or anyone else) says back.
//
// It drives the real thing end to end — Discord voice gateway, DAVE/MLS, UDP
// transport, the Bot's VAD/STT/LLM/TTS and playback — without a human, a
// microphone or a Discord user account. It reuses [gxvoice], so it speaks the
// same transport the Bot does.
//
// A test joins once and then holds a conversation:
//
//	p, err := voiceprobe.Join(ctx, voiceprobe.Config{Token: tok, Guild: g, Channel: c, ReplyFrom: bot})
//	defer p.Close()
//	reply, err := p.Say(ctx, clip)
//	// reply.Heard, reply.Latency, reply.PCM (feed it to an STT to assert content)
//
// The probe's bot application must be a different application from the Bot
// under test, invited to the guild with Connect + Speak. Speaking needs a
// binary built with `-tags "opus dave nolibopusfile"` (libopus encoder, DAVE);
// without them [Join] and [Probe.Say] report why instead of failing obscurely.
//
// The probe waits to speak until a second member is in the channel: Discord
// only completes the DAVE group then, and the sender is gated on it. Start the
// Bot's Voice Session first.
package voiceprobe

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/disgoorg/disgo"
	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/gateway"
	"github.com/disgoorg/snowflake/v2"
	"github.com/pion/opus"

	gxvoice "github.com/MrWong99/Glyphoxa/pkg/voice"
	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
	"github.com/MrWong99/Glyphoxa/pkg/voice/wire/codec"
)

// SampleRate is the rate of every recording the probe produces (mono, 16-bit).
const SampleRate = 48000

const (
	defaultQuiet   = 4 * time.Second
	defaultMaxWait = 45 * time.Second
	// opusFrame is one 20 ms frame at SampleRate.
	opusFrame = SampleRate * 20 / 1000
)

// ErrNoDave is returned by [Join] from a binary built without `-tags dave`:
// Discord rejects non-DAVE voice (close 4017), so the join could never work.
var ErrNoDave = errors.New("voiceprobe: built without -tags dave; Discord rejects non-DAVE voice (close 4017)")

// Config configures a [Probe].
type Config struct {
	// Token is the probe bot's token. Required.
	Token string
	// Guild and Channel name the voice channel to join. Required.
	Guild, Channel snowflake.ID
	// ReplyFrom, when non-zero, is the only speaker whose audio counts as a
	// reply (normally the Bot under test's user ID). Zero counts anyone, so a
	// human talking in the channel would satisfy a test. Everyone is recorded
	// either way.
	ReplyFrom snowflake.ID
	// Quiet is how much silence ends a reply. Default 4s.
	Quiet time.Duration
	// MaxWait is how long [Probe.Say] waits for a reply to begin. Default 45s.
	// A reply that has begun is always heard to its end.
	MaxWait time.Duration
	// Logger receives disgo/voice logs. Nil discards them.
	Logger *slog.Logger
}

// Clip is audio for the probe to speak: 16-bit little-endian PCM at any sample
// rate and channel count (it is resampled and downmixed when spoken).
type Clip struct {
	Name       string
	PCM        []byte
	SampleRate int
	Channels   int
}

// Duration is the clip's playing time.
func (c Clip) Duration() time.Duration {
	if c.SampleRate <= 0 || c.Channels <= 0 {
		return 0
	}
	return time.Duration(float64(len(c.PCM)) / float64(2*c.Channels*c.SampleRate) * float64(time.Second))
}

// Reply is what the probe heard after speaking one clip.
type Reply struct {
	// Heard is false when no counted speaker said anything within MaxWait.
	Heard bool
	// Latency is the time from the end of the clip to the first reply audio.
	Latency time.Duration
	// Duration is the time from the first to the last reply audio.
	Duration time.Duration
	// PCM is the reply audio, mono at [SampleRate], with its real pauses. Feed
	// it to a speech-to-text provider to assert on what was said.
	PCM []int16
}

// Track is everything recorded from one speaker over the probe's lifetime, on
// a timeline starting when the probe joined.
type Track struct {
	UserID       snowflake.ID
	PCM          []int16       // mono at SampleRate, silence where they did not speak
	Voiced       time.Duration // audio actually received
	Frames       int
	DecodeErrors int
}

// Probe is a joined synthetic Player. It is not safe for concurrent [Probe.Say]
// calls: a conversation is one clip at a time.
type Probe struct {
	cfg    Config
	client *bot.Client
	mgr    *gxvoice.Manager
	sess   *gxvoice.Session
	codec  *codec.Codec
	rec    *recorder
	joined time.Time

	closeOnce sync.Once
	closeErr  error
}

// Join connects the probe bot and joins the voice channel. Close the returned
// Probe to leave.
func Join(ctx context.Context, cfg Config) (*Probe, error) {
	if cfg.Token == "" {
		return nil, errors.New("voiceprobe: Config.Token is empty")
	}
	if cfg.Guild == 0 || cfg.Channel == 0 {
		return nil, errors.New("voiceprobe: Config.Guild and Config.Channel are required")
	}
	if !gxvoice.DaveAvailable() {
		return nil, ErrNoDave
	}
	if cfg.Quiet <= 0 {
		cfg.Quiet = defaultQuiet
	}
	if cfg.MaxWait <= 0 {
		cfg.MaxWait = defaultMaxWait
	}
	log := cfg.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	client, err := disgo.New(cfg.Token,
		bot.WithLogger(log),
		bot.WithDefaultGateway(),
		// Without GuildVoiceStates the bot never sees its own voice state and
		// disgo's voice join dereferences a nil channel (see internal/wirenpc).
		bot.WithGatewayConfigOpts(gateway.WithIntents(gateway.IntentGuilds|gateway.IntentGuildVoiceStates)),
		gxvoice.DaveOption(),
		gxvoice.TransportOption(log, nil),
	)
	if err != nil {
		return nil, fmt.Errorf("voiceprobe: build client: %w", err)
	}
	// Neither step fails on its own when the token lacks access: a join the bot
	// may not make simply never gets its voice-server update. Bound both.
	joinCtx, cancel := context.WithTimeout(ctx, joinTimeout)
	defer cancel()
	if err := client.OpenGateway(joinCtx); err != nil {
		client.Close(context.Background())
		return nil, fmt.Errorf("voiceprobe: open gateway: %w", err)
	}
	mgr := gxvoice.NewManager(client, gxvoice.WithLogger(log), gxvoice.WithDave(true),
		gxvoice.WithSelfDeaf(false), gxvoice.WithSelfMute(false), gxvoice.WithInboundBuffer(1024))
	sess, err := mgr.Open(joinCtx, cfg.Guild, cfg.Channel)
	if err != nil {
		_ = mgr.Close()
		client.Close(context.Background())
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("voiceprobe: no voice connection after %s: check the channel ID and that the probe bot is in the guild with Connect and Speak there: %w", joinTimeout, err)
		}
		return nil, fmt.Errorf("voiceprobe: join voice: %w", err)
	}

	p := &Probe{cfg: cfg, client: client, mgr: mgr, sess: sess, codec: codec.New(), joined: time.Now()}
	p.rec = newRecorder(p.joined, cfg.ReplyFrom)
	go p.rec.consume(sess.Inbound())
	return p, nil
}

// Say speaks the clip and waits for the reply to finish. A missing reply is not
// an error: it is reported as Reply.Heard == false, for the caller to assert on.
func (p *Probe) Say(ctx context.Context, clip Clip) (Reply, error) {
	if err := p.speak(ctx, clip); err != nil {
		return Reply{}, err
	}
	return p.rec.waitReply(ctx, time.Now(), p.cfg.Quiet, p.cfg.MaxWait)
}

// Barge is what the probe heard when it talked over a reply (a barge-in).
type Barge struct {
	// Heard is false when the first clip got no reply to interrupt; nothing
	// else is set and the second clip was not spoken.
	Heard bool
	// Latency is the time from the end of the first clip to its reply.
	Latency time.Duration
	// TalkedOver is how long the Bot kept talking after the probe started to
	// speak over it: small when barge-in works, the rest of the reply when the
	// Bot ignored the interruption.
	TalkedOver time.Duration
	// Reply is what came back for the interrupting clip, if anything.
	Reply Reply
}

// bargeGap is the silence that separates the interrupted reply from the reply
// to the interruption. Shorter than any turn (STT + LLM + TTS), longer than a
// pause between two sentences of one reply.
const bargeGap = 700 * time.Millisecond

// joinTimeout bounds [Join]; playMargin is how long past its own length a clip
// may take to go out before [Probe.Say] gives up on it.
const (
	joinTimeout = 30 * time.Second
	playMargin  = 20 * time.Second
)

// pauseRebase is how far behind its timestamp a frame must arrive to count as
// the start of a new utterance after a pause (see [recorder.record]).
const pauseRebase = 200 * time.Millisecond

// Interrupt speaks clip, lets the reply run for after, then speaks over on top
// of it and reports how quickly the Bot yielded and what it answered. Use a
// clip that draws a reply longer than after plus the length of over.
func (p *Probe) Interrupt(ctx context.Context, clip, over Clip, after time.Duration) (Barge, error) {
	if err := p.speak(ctx, clip); err != nil {
		return Barge{}, err
	}
	asked := time.Now()
	first, err := p.rec.waitStart(ctx, asked, p.cfg.MaxWait)
	if err != nil || first.IsZero() {
		return Barge{}, err
	}
	select {
	case <-time.After(after):
	case <-ctx.Done():
		return Barge{}, ctx.Err()
	}
	barged := time.Now()
	if err := p.speak(ctx, over); err != nil {
		return Barge{}, err
	}
	out := Barge{Heard: true, Latency: first.Sub(asked)}
	out.TalkedOver, out.Reply, err = p.rec.waitBarge(ctx, barged, time.Now(), p.cfg.Quiet, p.cfg.MaxWait)
	return out, err
}

// speak plays one clip to its end.
func (p *Probe) speak(ctx context.Context, clip Clip) error {
	if len(clip.PCM) == 0 {
		return fmt.Errorf("voiceprobe: clip %q is empty", clip.Name)
	}
	ch := make(chan tts.AudioChunk, 1)
	ch <- tts.AudioChunk{PCM: clip.PCM, SampleRate: clip.SampleRate, Channels: clip.Channels}
	close(ch)
	src, err := p.codec.PlaybackSource(ch)
	if err != nil {
		return fmt.Errorf("voiceprobe: encoder (build with -tags \"opus nolibopusfile\"): %w", err)
	}
	pb, err := p.sess.Play(ctx, src)
	if err != nil {
		return fmt.Errorf("voiceprobe: play %q: %w", clip.Name, err)
	}
	stuck := time.NewTimer(clip.Duration() + playMargin)
	defer stuck.Stop()
	select {
	case <-pb.Done():
	case <-stuck.C:
		pb.Stop()
		return fmt.Errorf("voiceprobe: %q (%s) was still not sent after %s: alone in a channel the probe cannot speak (Discord completes the DAVE group only with a second member) — start the Voice Session first",
			clip.Name, clip.Duration().Round(100*time.Millisecond), clip.Duration()+playMargin)
	case <-ctx.Done():
		pb.Stop()
		return ctx.Err()
	}
	if err := pb.Err(); err != nil {
		return fmt.Errorf("voiceprobe: playback %q: %w", clip.Name, err)
	}
	return nil
}

// Listen keeps recording for d without speaking (a settle period after joining,
// or a soak for unprompted speech).
func (p *Probe) Listen(ctx context.Context, d time.Duration) {
	select {
	case <-time.After(d):
	case <-ctx.Done():
	}
}

// Elapsed is the time since the probe joined — the origin of every [Track].
func (p *Probe) Elapsed() time.Duration { return time.Since(p.joined) }

// Recording returns a snapshot of every speaker heard so far, ordered by user ID.
func (p *Probe) Recording() []Track { return p.rec.tracksSnapshot() }

// Close leaves the channel and disconnects. The recording stays readable.
func (p *Probe) Close() error {
	p.closeOnce.Do(func() {
		p.closeErr = p.sess.Close()
		_ = p.mgr.Close()
		p.client.Close(context.Background())
	})
	return p.closeErr
}

// recorder decodes every inbound speaker onto its own wall-clock-aligned mono
// track, so the recording keeps real pauses and reply timing.
type recorder struct {
	start     time.Time
	replyFrom snowflake.ID

	mu     sync.Mutex
	tracks map[snowflake.ID]*track
	// Reply detection, over counted speakers only: armed is when the current
	// wait began, first the first frame after it, last the latest frame.
	armed time.Time
	first time.Time
	last  time.Time
	// arrivals are the counted frames' arrival times since armed: the timeline
	// [Probe.Interrupt] reads to tell the interrupted reply from the next one.
	arrivals []time.Time
}

type track struct {
	dec     opus.Decoder
	buf     []int16
	pcm     []int16
	base    int // sample offset (from recorder start) of this stream's PTS zero
	lastPTS time.Duration
	voiced  int
	frames  int
	errs    int
}

func newRecorder(start time.Time, replyFrom snowflake.ID) *recorder {
	return &recorder{start: start, replyFrom: replyFrom, tracks: make(map[snowflake.ID]*track)}
}

func (r *recorder) counts(user snowflake.ID) bool {
	return r.replyFrom == 0 || user == r.replyFrom
}

func (r *recorder) consume(in <-chan gxvoice.Frame) {
	for f := range in {
		if f.Silence || len(f.Opus) == 0 {
			continue
		}
		r.record(f, time.Now())
	}
}

func (r *recorder) record(f gxvoice.Frame, now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t := r.tracks[f.UserID]
	rebase := t != nil && f.PTS < t.lastPTS // SSRC change: PTS restarted
	if t == nil {
		dec, err := opus.NewDecoderWithOutput(SampleRate, 1)
		if err != nil {
			return
		}
		t = &track{dec: dec, buf: make([]int16, SampleRate*120/1000)}
		r.tracks[f.UserID] = t
		rebase = true
	}
	// A speaker's PTS only advances while they send: after a pause their next
	// frame carries the timestamp right behind the last one. Left alone it would
	// be written directly after the previous utterance — a track with no pauses,
	// and a reply window ([recorder.mix] reads wall-clock time) full of silence.
	// So a frame that arrives well after its PTS says it should re-anchors the
	// stream to the wall clock. Jitter is far below the threshold.
	if !rebase && samplesIn(now.Sub(r.start))-(t.base+samplesIn(f.PTS)) > samplesIn(pauseRebase) {
		rebase = true
	}
	if rebase {
		t.base = samplesIn(now.Sub(r.start)) - samplesIn(f.PTS)
	}
	t.lastPTS = f.PTS
	t.frames++
	n, err := t.dec.DecodeToInt16(f.Opus, t.buf)
	if err != nil {
		t.errs++
		return
	}
	at := max(t.base+samplesIn(f.PTS), 0)
	if need := at + n; need > len(t.pcm) {
		t.pcm = append(t.pcm, make([]int16, need-len(t.pcm))...)
	}
	copy(t.pcm[at:], t.buf[:n])
	t.voiced += n
	if r.counts(f.UserID) {
		if !r.armed.IsZero() && now.After(r.armed) {
			if r.first.IsZero() {
				r.first = now
			}
			r.arrivals = append(r.arrivals, now)
		}
		r.last = now
	}
}

func samplesIn(d time.Duration) int { return int(d.Seconds() * SampleRate) }

// arm starts a new reply wait at since. Callers hold r.mu.
func (r *recorder) arm(since time.Time) {
	r.armed, r.first, r.arrivals = since, time.Time{}, r.arrivals[:0]
}

// waitStart blocks until the first counted audio after since and returns when
// it arrived, or the zero time when maxWait passes with nothing heard. The
// wait stays armed, so the arrivals keep accumulating for [recorder.waitBarge].
func (r *recorder) waitStart(ctx context.Context, since time.Time, maxWait time.Duration) (time.Time, error) {
	r.mu.Lock()
	r.arm(since)
	r.mu.Unlock()

	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return time.Time{}, ctx.Err()
		case now := <-tick.C:
			r.mu.Lock()
			first := r.first
			r.mu.Unlock()
			if !first.IsZero() || now.Sub(since) >= maxWait {
				return first, nil
			}
		}
	}
}

// waitBarge follows an interruption that began at barged and whose clip ended
// at spoken: it returns how long the interrupted reply ran on past barged, and
// the reply to the interruption once it has been followed by quiet (or the
// zero Reply when maxWait passes after spoken without one).
func (r *recorder) waitBarge(ctx context.Context, barged, spoken time.Time, quiet, maxWait time.Duration) (time.Duration, Reply, error) {
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return 0, Reply{}, ctx.Err()
		case now := <-tick.C:
			r.mu.Lock()
			cut, resumed, last := splitBarge(r.arrivals, barged, now, bargeGap)
			r.mu.Unlock()
			switch {
			case cut.IsZero(): // the interrupted reply is still running
			case !resumed.IsZero() && now.Sub(last) >= quiet:
				return talkedOver(cut, barged), Reply{
					Heard:    true,
					Latency:  resumed.Sub(spoken),
					Duration: last.Sub(resumed),
					PCM:      r.mix(resumed, last),
				}, nil
			case resumed.IsZero() && now.Sub(spoken) >= maxWait:
				return talkedOver(cut, barged), Reply{}, nil
			}
		}
	}
}

func talkedOver(cut, barged time.Time) time.Duration { return max(cut.Sub(barged), 0) }

// splitBarge reads a frame-arrival timeline around an interruption at barged:
// cut is the last frame of the reply that was running then (zero while it has
// not yet fallen silent for gap, judged at now), resumed the first frame after
// that silence (zero if none yet), last the latest frame.
func splitBarge(arrivals []time.Time, barged, now time.Time, gap time.Duration) (cut, resumed, last time.Time) {
	if len(arrivals) == 0 {
		return barged, time.Time{}, time.Time{}
	}
	last = arrivals[len(arrivals)-1]
	prev := barged
	for _, at := range arrivals {
		if at.Before(barged) {
			prev = at
			continue
		}
		if at.Sub(prev) >= gap {
			return prev, at, last
		}
		prev = at
	}
	if now.Sub(prev) >= gap {
		return prev, time.Time{}, last
	}
	return time.Time{}, time.Time{}, last
}

// waitReply blocks until audio that began after since has been followed by
// quiet of silence, or until maxWait passes with nothing heard.
func (r *recorder) waitReply(ctx context.Context, since time.Time, quiet, maxWait time.Duration) (Reply, error) {
	r.mu.Lock()
	r.arm(since)
	r.mu.Unlock()

	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return Reply{}, ctx.Err()
		case now := <-tick.C:
			r.mu.Lock()
			first, last := r.first, r.last
			r.mu.Unlock()
			switch {
			case !first.IsZero() && now.Sub(last) >= quiet:
				return Reply{
					Heard:    true,
					Latency:  first.Sub(since),
					Duration: last.Sub(first),
					PCM:      r.mix(first, last),
				}, nil
			case first.IsZero() && now.Sub(since) >= maxWait:
				return Reply{}, nil
			}
		}
	}
}

// mix sums the counted speakers' audio between two wall-clock instants, with a
// frame of margin either side for the arrival jitter the timeline was built on.
func (r *recorder) mix(from, to time.Time) []int16 {
	lo := max(samplesIn(from.Sub(r.start))-opusFrame, 0)
	hi := samplesIn(to.Sub(r.start)) + 2*opusFrame
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]int16, hi-lo)
	for id, t := range r.tracks {
		if !r.counts(id) {
			continue
		}
		for i := lo; i < hi && i < len(t.pcm); i++ {
			out[i-lo] = clip16(int32(out[i-lo]) + int32(t.pcm[i]))
		}
	}
	return out
}

func clip16(v int32) int16 {
	return int16(min(max(v, -32768), 32767))
}

func (r *recorder) tracksSnapshot() []Track {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Track, 0, len(r.tracks))
	for id, t := range r.tracks {
		out = append(out, Track{
			UserID: id, PCM: append([]int16(nil), t.pcm...), Frames: t.frames, DecodeErrors: t.errs,
			Voiced: time.Duration(float64(t.voiced) / SampleRate * float64(time.Second)),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UserID < out[j].UserID })
	return out
}

// LoadWAV reads a 16-bit PCM WAV of any sample rate and channel count.
func LoadWAV(path string) (Clip, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Clip{}, err
	}
	if len(b) < 12 || string(b[:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return Clip{}, fmt.Errorf("voiceprobe: %s is not a RIFF/WAVE file", path)
	}
	var rate, channels, bits int
	var data []byte
	for off := 12; off+8 <= len(b); {
		id := string(b[off : off+4])
		size := int(binary.LittleEndian.Uint32(b[off+4 : off+8]))
		body := off + 8
		end := min(body+size, len(b))
		switch id {
		case "fmt ":
			if end-body < 16 {
				return Clip{}, fmt.Errorf("voiceprobe: %s has a short fmt chunk", path)
			}
			if format := binary.LittleEndian.Uint16(b[body:]); format != 1 && format != 0xFFFE {
				return Clip{}, fmt.Errorf("voiceprobe: %s is WAV format %d, not PCM", path, format)
			}
			channels = int(binary.LittleEndian.Uint16(b[body+2:]))
			rate = int(binary.LittleEndian.Uint32(b[body+4:]))
			bits = int(binary.LittleEndian.Uint16(b[body+14:]))
		case "data":
			data = b[body:end]
		}
		off = body + size + size%2
	}
	if data == nil || rate == 0 || channels == 0 {
		return Clip{}, fmt.Errorf("voiceprobe: %s has no fmt or data chunk", path)
	}
	if bits != 16 {
		return Clip{}, fmt.Errorf("voiceprobe: %s is %d-bit; need 16-bit PCM (ffmpeg -i in -c:a pcm_s16le out.wav)", path, bits)
	}
	return Clip{Name: path, PCM: data, SampleRate: rate, Channels: channels}, nil
}

// WriteWAV writes mono 16-bit PCM as a WAV file.
func WriteWAV(path string, pcm []int16, rate int) error {
	dataLen := len(pcm) * 2
	b := make([]byte, 44+dataLen)
	copy(b[0:], "RIFF")
	binary.LittleEndian.PutUint32(b[4:], uint32(36+dataLen))
	copy(b[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(b[16:], 16)
	binary.LittleEndian.PutUint16(b[20:], 1)
	binary.LittleEndian.PutUint16(b[22:], 1)
	binary.LittleEndian.PutUint32(b[24:], uint32(rate))
	binary.LittleEndian.PutUint32(b[28:], uint32(rate*2))
	binary.LittleEndian.PutUint16(b[32:], 2)
	binary.LittleEndian.PutUint16(b[34:], 16)
	copy(b[36:], "data")
	binary.LittleEndian.PutUint32(b[40:], uint32(dataLen))
	for i, s := range pcm {
		binary.LittleEndian.PutUint16(b[44+2*i:], uint16(s))
	}
	return os.WriteFile(path, b, 0o644)
}
