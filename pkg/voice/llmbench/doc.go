// Package llmbench is the text-level voice-LLM A/B bench (#702): it plays
// scripted German tavern conversations against a table of candidate models and
// reports, per candidate, how fast the first speakable sentence arrives, how
// reliably Tools are used, what leaks into the spoken text, and what it costs.
//
// It drives the production reply path without audio. A bench turn enters where
// a live turn enters after Address Detection — [agent.Replier]'s streaming
// reply — and ends where a sentence would be handed to TTS. In between runs
// exactly what production runs: the prompt assembly, the tool loop with the
// dice gate, the invented-roll guard and the stream scrubber
// ([agenttool.Engine]), the sentence splitter and spoken-text scrub, and the
// real provider adapter. Only the knowledge Tools' backing store is fake (a
// small in-memory campaign), so the Tools are callable without a database.
//
// Everything here is plain Go and unit-tested offline; the one live entry
// point, TestLLMBench_Live, is behind the build tags "bench live". See the
// README for how to run it and how to read the output.
//
// [agent.Replier]: github.com/MrWong99/Glyphoxa/pkg/voice/agent
// [agenttool.Engine]: github.com/MrWong99/Glyphoxa/pkg/voice/agenttool
package llmbench
