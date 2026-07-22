# NPC turn-end commits delivered sentences only

NPC speech is committed at sentence granularity: a sentence counts as **delivered** when its last opus frame is forwarded to Discord. On turn-end (natural OR barge-in), the Transcript utterance is committed containing only delivered sentences; mid-sentence audio at barge-in is dropped. Per-utterance fields: `was_interrupted`, `interrupted_by_user_id`.

If zero sentences were delivered (interrupted before the first sentence completes), the utterance is **not logged at all**.

**Why:** Transcripts must reflect what listeners actually heard. Word-level mid-sentence truncation would need word-timestamps from the TTS provider, which is deferred to v1.5+. Logging zero-delivered utterances would pollute Address Detection and NPC retrieval with content the room never heard.

---

**Note (2026-07-22, #437):** the LINE grain (ADR-0040) conforms to this
invariant too — never-delivered lines are reconciled out of `transcript_line`,
so "not logged at all" holds for both persisted grains.
