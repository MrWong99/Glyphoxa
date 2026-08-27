// Command palette (Ctrl+K campaign search, #591). en is the source of truth;
// de must cover every key (compile-checked in i18n/index.tsx).

export const en = {
  "palette.title": "Campaign search",
  "palette.openSearch": "Search the campaign (Ctrl+K)",
  "palette.placeholder": "Search entries, transcripts, highlights…",
  "palette.hint": "Type to search this campaign. Esc closes.",
  "palette.groupEntries": "Entries",
  "palette.groupTranscripts": "Transcripts",
  "palette.groupHighlights": "Highlights",
  // Shown under the Transcripts heading when the server answered in keyword
  // mode (semantic=false): no embeddings provider, or the embed failed. Honest
  // degraded copy, never silent (#591 AC).
  "palette.transcriptsDegraded": "Keyword matches — semantic search is unavailable (no embeddings provider).",
  "palette.noResults": "No results for “{query}”.",
  "palette.searchFailed": "{group}: search failed ({message})",
  "palette.gmPrivateBadge": "GM-only",
  // A semantic chunk hit has no single speaker; the row leads with the session
  // date instead. {when} is the localized timestamp.
  "palette.transcriptAt": "Session, {when}",
} as const;

export const de: Record<keyof typeof en, string> = {
  "palette.title": "Kampagnen-Suche",
  "palette.openSearch": "Kampagne durchsuchen (Strg+K)",
  "palette.placeholder": "Einträge, Mitschriften, Highlights durchsuchen…",
  "palette.hint": "Tippen, um diese Kampagne zu durchsuchen. Esc schließt.",
  "palette.groupEntries": "Einträge",
  "palette.groupTranscripts": "Mitschriften",
  "palette.groupHighlights": "Highlights",
  "palette.transcriptsDegraded": "Stichwort-Treffer — semantische Suche nicht verfügbar (kein Embeddings-Anbieter).",
  "palette.noResults": "Keine Ergebnisse für „{query}“.",
  "palette.searchFailed": "{group}: Suche fehlgeschlagen ({message})",
  "palette.gmPrivateBadge": "Nur für GM",
  "palette.transcriptAt": "Session, {when}",
};
