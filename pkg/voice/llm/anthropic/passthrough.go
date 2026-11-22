package anthropic

import (
	"encoding/json"
	"sync"
)

// This file is the thinking-block continuity of a tool turn (#702).
//
// With adaptive thinking the model emits a `thinking` block (its text omitted,
// a signature only) in front of a tool_use block. The Messages API wants that
// block passed back unchanged on the request that carries the tool result;
// dropping it is accepted, but the model was measured to think again after the
// tool result — seconds of latency and hundreds of output tokens on the turn a
// player is waiting on.
//
// The block is opaque provider state with no place in the provider-neutral
// vocabulary: [llm.Message] rebuilds an assistant turn from text and
// [llm.ToolCall]s only, and adding a field for it would have to be threaded
// through llm.Message, tool.Message, tool.AssistantMessage, the tool loop and
// both agenttool converters, for a value only this adapter can read. So the
// adapter keeps it itself: the blocks of a streamed assistant turn are stored
// under that turn's tool_use ids, and put back when a request replays an
// assistant turn carrying one of those ids. The ids are what the caller must
// echo anyway, so nothing above the adapter changes.
//
// The store only ever serves the tool rounds of a turn in flight — the voice
// history keeps spoken text, never tool calls — so it is small and bounded.

// maxThinkingTurns bounds the store: the number of tool_use ids whose thinking
// blocks are remembered. A turn's tool loop runs at most a handful of rounds,
// and a session a handful of turns at once; anything older is never replayed.
const maxThinkingTurns = 256

// thinkingStore remembers the thinking blocks of recent assistant tool turns,
// keyed by tool_use id, oldest evicted first. Safe for concurrent use: one
// client serves every NPC of a Voice Session.
type thinkingStore struct {
	mu     sync.Mutex
	blocks map[string][]json.RawMessage
	order  []string // insertion order of the keys, for eviction
}

func newThinkingStore() *thinkingStore {
	return &thinkingStore{blocks: map[string][]json.RawMessage{}}
}

// put records blocks as the thinking that preceded the tool_use toolUseID.
func (s *thinkingStore) put(toolUseID string, blocks []json.RawMessage) {
	if s == nil || toolUseID == "" || len(blocks) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.blocks[toolUseID]; !ok {
		s.order = append(s.order, toolUseID)
		for len(s.order) > maxThinkingTurns {
			delete(s.blocks, s.order[0])
			s.order = s.order[1:]
		}
	}
	s.blocks[toolUseID] = blocks
}

// get returns the thinking blocks stored for toolUseID, nil if none.
func (s *thinkingStore) get(toolUseID string) []json.RawMessage {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.blocks[toolUseID]
}

// forget drops what is stored for the given ids — the blocks the API has just
// refused, so no later request replays them either.
func (s *thinkingStore) forget(toolUseIDs []string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range toolUseIDs {
		delete(s.blocks, id)
	}
}

// thinkingBlock renders one streamed thinking block as the content block to
// replay. The `thinking` text is written even when empty: the API omits it by
// default and still requires the field on the way back.
func thinkingBlock(text, signature string) json.RawMessage {
	raw, _ := json.Marshal(struct {
		Type      string `json:"type"`
		Thinking  string `json:"thinking"`
		Signature string `json:"signature"`
	}{"thinking", text, signature})
	return raw
}

// redactedThinkingBlock renders a redacted_thinking block to replay.
func redactedThinkingBlock(data string) json.RawMessage {
	raw, _ := json.Marshal(struct {
		Type string `json:"type"`
		Data string `json:"data"`
	}{"redacted_thinking", data})
	return raw
}
