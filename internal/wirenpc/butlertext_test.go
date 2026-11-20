package wirenpc

import "testing"

// TestButlerTextMessage_SuppressesMentions (S8): a Butler text answer is LLM
// output posted to a public channel, so it must never ping anyone.
func TestButlerTextMessage_SuppressesMentions(t *testing.T) {
	msg := butlerTextMessage("@everyone the dragon is here")
	if msg.Content != "@everyone the dragon is here" {
		t.Errorf("content = %q, want the part verbatim", msg.Content)
	}
	assertNoMentions(t, msg.AllowedMentions)
}
