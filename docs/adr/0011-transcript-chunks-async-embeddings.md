# Transcript chunks with async embeddings (pgvector)

The storage unit for Transcripts is the chunk (3–6 utterances), embedded with pgvector + HNSW from day one. Chunking closes on whichever-first: 5 utterances OR 60s elapsed OR session ending. Single-utterance chunks are only flushed at session end.

The buffer is an in-process ring per active Voice Session held in the Voice Instance — no WAL. Crash-loss bound is <60s.

The embedding pipeline is async and eventually consistent: insert chunk with `embedding=NULL`, a background worker embeds and `UPDATE`s. Retrieval queries filter `WHERE embedding IS NOT NULL`; the HNSW index is partial on non-null embeddings.

Default embedding model: Ollama `nomic-embed-text` (1024-dim, local). Switching models requires a backfill.

User-facing transcript search in v1.0 is tsvector-only; embedding-augmented overlay is possible later. NPC retrieval (Hot Context assembly) uses ANN similarity with hard filters on `participated_agent_ids` (NPC-knowledge) vs `campaign_id` only (topical/world context, marked "may not personally know"). Mentioned-entity extraction is case-insensitive name matching against the Campaign's Agents and KG Nodes at chunk-finalize; NER is deferred.

Audio extracts are deferred to v1.5+; the schema accommodates with future nullable columns.
