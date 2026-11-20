# NPC turn-end commits delivered sentences only

NPC speech is committed at sentence granularity: a sentence counts as **delivered** when its last opus frame is forwarded to Discord. On turn-end (natural OR barge-in), the Transcript utterance is committed containing only delivered sentences; mid-sentence audio at barge-in is dropped. Per-utterance fields: `was_interrupted`, `interrupted_by_user_id`.

If zero sentences were delivered (interrupted before the first sentence completes), the utterance is **not logged at all**.

**Why:** Transcripts must reflect what listeners actually heard. Word-level mid-sentence truncation would need word-timestamps from the TTS provider, which is deferred to v1.5+. Logging zero-delivered utterances would pollute Address Detection and NPC retrieval with content the room never heard.

---

**Note (2026-07-22, #437):** the LINE grain (ADR-0040) conforms to this
invariant too — never-delivered lines are reconciled out of `transcript_line`,
so "not logged at all" holds for both persisted grains.

**Amendment (2026-11-20): generation runs ahead of delivery; a commit is
anchored to its own turn.** The streaming Agent turn no longer consumes the
provider stream in lockstep with playback: generation is drained into a bounded
sentence queue and a dispatcher delivers the queue at speaking pace
(`agent.Replier.streamTurn`). The invariant is unchanged and is now stated
against the queue — *generated is not delivered*: a sentence still queued when
the turn is cut (barge, mute, supersede) was never dispatched and is neither
spoken nor committed. The per-turn deadline (`agent.Config.TurnTimeout`) bounds
generation only; before, it also bounded playback and truncated any reply
longer than a minute of audio. The deliberate bound that replaces that accident
is a cap on the text one turn may queue for speech (`agent.MaxSpokenRunes`,
6000 runes): at the cap generation is stopped and what is queued is spoken. Separately, the assistant commit is placed
directly behind its own turn's user message instead of appended to the end of
the Agent's history, so a superseded turn's delivered prefix that commits after
the superseding turn recorded its user line cannot leave the history reading
user, user, assistant. A commit whose user message has meanwhile been trimmed
out of the window is dropped.
