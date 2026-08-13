# Transcript chunks with async embeddings (pgvector)

The storage unit for Transcripts is the chunk (3–6 utterances), embedded with pgvector + HNSW from day one. Chunking closes on whichever-first: 5 utterances OR 60s elapsed OR session ending. Single-utterance chunks are only flushed at session end.

The buffer is an in-process ring per active Voice Session held in the Voice Instance — no WAL. Crash-loss bound is <60s.

The embedding pipeline is async and eventually consistent: insert chunk with `embedding=NULL`, a background worker embeds and `UPDATE`s. Retrieval queries filter `WHERE embedding IS NOT NULL`; the HNSW index is partial on non-null embeddings.

Default embedding model: Ollama `nomic-embed-text` (768-dim, local) → `vector(768)`. Switching models (or Matryoshka dimensions) requires a backfill.

User-facing transcript search in v1.0 is tsvector-only; embedding-augmented overlay is possible later. NPC retrieval (Hot Context assembly) uses ANN similarity with hard filters on `participated_agent_ids` (NPC-knowledge) vs `campaign_id` only (topical/world context, marked "may not personally know"). Mentioned-entity extraction is case-insensitive name matching against the Campaign's Agents and KG Nodes at chunk-finalize; NER is deferred.

Audio extracts are deferred to v1.5+; the schema accommodates with future nullable columns.

## Amendment: user-facing search moves to the Transcript Line grain (2026-07-04, #120)

v1.0 user-facing search (web search box + `/glyphoxa search`) queries a **generated tsvector column on `transcript_line`**, not the chunk fts column. Two reasons: line hits carry an exact speaker/timestamp and can deep-link straight to the rendered line in the Session screen (a chunk hit cannot), and it decouples the search slice from the chunk writer, which now ships inside the streaming-STT-enlarged memory epic (ADR-0042). The chunk `fts` column stays in place, reserved for the later embedding-augmented search overlay — it is retrieval infrastructure, not the user-facing search path. Search stays tsvector-only in v1.0 as decided above.

## Amendment: the embedding-augmented overlay arrives as campaign search (2026-08-13, #591)

The "later embedding-augmented search overlay" reserved above is now built, as the Ctrl+K campaign palette's transcript tier (`internal/search`): the query is embedded with the SAME resolved embeddings provider the backfill worker and recall share, and the campaign-scoped chunk ANN serves semantic hits. The chunk-hit deep-link problem the 2026-07-04 amendment named is solved by **anchoring**: a chunk hit resolves to the first `transcript_line` of its session at/after the chunk's `started_at` (`FirstLineIDAtOrAfter`), so it deep-links like a line hit. With no embeddings provider (or any embed failure) the palette degrades to the tsvector line search and **says so on the wire** (`semantic=false`) — the Line grain remains the keyword truth; the Session screen's own search box is unchanged. The query embedding is priced and logged (Ollama = $0; the off-session ledger flush stays open for the #592 ADR).
