// Command palette (Ctrl/Cmd+K): navigation layer + campaign search (#591). en
// is the source of truth; de must cover every key (compile-checked in
// i18n/index.tsx).

export const en = {
  "palette.title": "Search and jump",
  // The topbar button. {shortcut} is the chord for this platform ("Ctrl+K",
  // "⌘K"); the button's visible text is palette.searchButton.
  "palette.openSearch": "Search and jump ({shortcut})",
  "palette.searchButton": "Search or jump to…",
  // The modifier key's cap, as printed on the keyboard for this language.
  "palette.keyCtrl": "Ctrl",
  "palette.placeholder": "Search or jump to…",
  "palette.hint": "Type to search this campaign's entries, transcripts and highlights — or an NPC's name.",

  // Navigation layer (filtered in the browser).
  "palette.groupGoTo": "Go to",
  "palette.groupActions": "Actions",
  "palette.switchCampaign": "Switch campaign: {name}",
  "palette.goToLiveSession": "Go to live session",
  "palette.newCampaignDesc":
    "It becomes your active campaign, with its own Butler, cast and world wiki.",

  // Footer key hints.
  "palette.keyHintMove": "move",
  "palette.keyHintOpen": "open",
  "palette.keyHintClose": "close",

  // Campaign search (ranked by the server).
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
  "palette.title": "Suchen und springen",
  "palette.openSearch": "Suchen und springen ({shortcut})",
  "palette.searchButton": "Suchen oder springen zu…",
  "palette.keyCtrl": "Strg",
  "palette.placeholder": "Suchen oder springen zu…",
  "palette.hint":
    "Tippe, um Einträge, Mitschriften und Highlights dieser Kampagne zu durchsuchen — oder den Namen eines NPCs.",

  "palette.groupGoTo": "Gehe zu",
  "palette.groupActions": "Aktionen",
  "palette.switchCampaign": "Kampagne wechseln: {name}",
  "palette.goToLiveSession": "Zur laufenden Session",
  "palette.newCampaignDesc":
    "Sie wird deine aktive Kampagne — mit eigenem Butler, eigener Besetzung und eigenem Welt-Wiki.",

  "palette.keyHintMove": "bewegen",
  "palette.keyHintOpen": "öffnen",
  "palette.keyHintClose": "schließen",

  "palette.groupEntries": "Einträge",
  "palette.groupTranscripts": "Mitschriften",
  "palette.groupHighlights": "Highlights",
  "palette.transcriptsDegraded": "Stichwort-Treffer — semantische Suche nicht verfügbar (kein Embeddings-Anbieter).",
  "palette.noResults": "Keine Ergebnisse für „{query}“.",
  "palette.searchFailed": "{group}: Suche fehlgeschlagen ({message})",
  "palette.gmPrivateBadge": "Nur für GM",
  "palette.transcriptAt": "Session, {when}",
};
