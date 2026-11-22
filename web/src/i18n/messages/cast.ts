// Strings for the Cast studio (New NPC dialog, roster readiness, aliases, voice picker).
//
// Registered empty with the shell skeleton (ADR-0063 amendment) so the package
// that fills it only ever edits THIS file — never i18n/index.tsx or i18n/de.ts,
// which several packages would otherwise all have to touch. Add each key to
// `en` and `de` together: the `de` annotation makes a missing German string a
// compile error, and i18n.test.tsx checks the assembled catalogs key for key.
//
// The agent editor's older strings (labels, tool names, the readiness checks'
// positive wording) stay in the campaign fragment under `campaign.*`.

export const en = {
  // ── Roster list ──────────────────────────────────────────────────────────
  "cast.loadErrorTitle": "Couldn't load the cast",
  "cast.rosterAria": "Cast members",
  "cast.summaryNpcs": "{n} NPCs",
  "cast.summaryNpcsOne": "1 NPC",
  "cast.summaryAttention": "{n} need attention",
  "cast.summaryAttentionOne": "1 needs attention",
  "cast.summaryAttentionHint": "Show only the NPCs that are not session-ready",
  "cast.summaryAllReady": "all session-ready",
  "cast.filterLabel": "Filter the cast",
  "cast.filterPlaceholder": "Filter by name or role…",
  "cast.filterNoMatch": "Nobody in the cast matches “{query}”.",
  "cast.filterClear": "Clear filter",
  "cast.emptyTitle": "No NPCs yet",
  "cast.emptyDesc":
    "NPCs are the characters your players talk to at the table. The Butler can run a session alone; add your first NPC when the table needs a face.",
  "cast.rowNeeds": "Needs attention: {checks}",
  "cast.rowNeedsTitle": "Not session-ready",
  "cast.rowVoice": "Voice: {name}",
  // The readiness checks worded as what is missing (the positive wording, used
  // by the Readiness view's marks, is campaign.check*).
  "cast.missingPersona": "No personality written",
  "cast.missingVoice": "No voice",
  "cast.missingLink": "No wiki entry linked",
  "cast.missingContent": "Wiki entry is empty",
  "cast.missingFacts": "No facts in reach",

  // ── New NPC dialog ───────────────────────────────────────────────────────
  "cast.newNpcTitle": "New NPC",
  "cast.newNpcDesc": "Who are they? A name is enough to start; everything can be changed later.",
  "cast.newNpcNameRequired": "Give the NPC a name.",
  "cast.newNpcNamePlaceholder": "e.g. Mira",
  "cast.newNpcConceptLabel": "Concept (optional)",
  "cast.newNpcConceptPlaceholder": "a grumpy dwarven blacksmith who secretly writes poetry",
  "cast.newNpcConceptHint":
    "One line is enough. Glyphoxa drafts a personality from it for you to review; the draft is not saved until you press Save.",
  "cast.newNpcVoiceLabel": "Voice (optional)",
  "cast.newNpcCreate": "Create NPC",
  "cast.newNpcErrorTitle": "Couldn't create the NPC",

  // ── Agent editor ─────────────────────────────────────────────────────────
  "cast.aliasesLabel": "Also answers to",
  "cast.aliasesHint":
    "Nicknames, titles and the spellings speech-to-text tends to produce, e.g. “Meera” for Mira. Press Enter after each one.",
  "cast.aliasesPlaceholder": "Add a name…",
  "cast.nameRequired": "A name is required.",
  "cast.revert": "Revert",
  "cast.saveBarAria": "Save",
  "cast.saveTooltip": "Save changes ({shortcut})",
  "cast.saveShortcut": "Ctrl+S",
  "cast.saveShortcutMac": "⌘S",
  "cast.draftFromConcept": "Drafting a personality from your concept…",
  "cast.readinessTitle": "{name} is not session-ready yet",
  "cast.readinessListAria": "What is missing",
  "cast.trimmed": "Knowledge trimmed {chars}/{maxChars}",
  "cast.trimmedHint":
    "The wiki around this NPC holds more than fits into one reply, so the facts furthest away were cut. Keep the entry itself and its closest connections short.",
  "cast.voiceBrowse": "Browse voices",
  "cast.voiceStop": "Stop preview",

  // ── Voice picker ─────────────────────────────────────────────────────────
  "cast.voicePickerTitle": "Choose a voice",
  "cast.voicePickerDesc":
    "Listen before you pick. Nothing plays by itself: every sample uses text-to-speech characters from your plan.",
  "cast.voiceSearchLabel": "Search voices",
  "cast.voiceSampleLabel": "Sample line",
  "cast.voiceSamplePlaceholder": "Leave empty for the standard line",
  "cast.voiceSampleHint": "What each voice says when you press play. Up to {max} characters.",
  "cast.voiceLangChip": "Campaign language: {language}",
  "cast.voiceListAria": "Voices",
  "cast.voicePlay": "Play {name}",
  "cast.voiceStopNamed": "Stop {name}",
  "cast.voiceCurrent": "Current",
  "cast.voiceUse": "Use this voice",
  "cast.voiceNoMatch": "No voice matches your search.",
  "cast.voiceClearSearch": "Show all voices",
  "cast.voiceCatalogErrorTitle": "Couldn't load the voice catalog",
  "cast.voiceCatalogEmptyTitle": "No voices available",
  "cast.voiceCatalogEmptyDesc":
    "The voice catalog came back empty. Check the text-to-speech key in Setup.",
  "cast.openSetup": "Open Setup",

  // ── Readiness view ───────────────────────────────────────────────────────
  "cast.prepReadyCount": "{ready} of {total} NPCs ready",
  "cast.prepReadyCountOne": "{ready} of 1 NPC ready",
  "cast.prepModeAria": "Which NPCs to list",
  "cast.prepModeAttention": "Needs attention",
  "cast.prepModeAll": "All",
  "cast.prepAttentionAria": "NPCs that need attention",
  "cast.prepAllReadyTitle": "Everyone is ready",
  "cast.prepAllReadyDesc": "Every NPC has a voice, a personality and knowledge in reach.",
  "cast.prepShowAll": "Show all",
  "cast.prepLoadErrorTitle": "Couldn't load the readiness check",
  "cast.prepEmptyTitle": "No NPCs yet",
  "cast.prepEmptyDesc": "The readiness check fills in as you add NPCs to the cast.",
} as const;

export const de: Record<keyof typeof en, string> = {
  // ── Roster list ──────────────────────────────────────────────────────────
  "cast.loadErrorTitle": "Besetzung konnte nicht geladen werden",
  "cast.rosterAria": "Ensemblemitglieder",
  "cast.summaryNpcs": "{n} NPCs",
  "cast.summaryNpcsOne": "1 NPC",
  "cast.summaryAttention": "{n} brauchen Aufmerksamkeit",
  "cast.summaryAttentionOne": "1 braucht Aufmerksamkeit",
  "cast.summaryAttentionHint": "Nur die NPCs zeigen, die noch nicht bereit für die Session sind",
  "cast.summaryAllReady": "alle bereit für die Session",
  "cast.filterLabel": "Besetzung filtern",
  "cast.filterPlaceholder": "Nach Name oder Rolle filtern…",
  "cast.filterNoMatch": "Niemand in der Besetzung passt zu „{query}“.",
  "cast.filterClear": "Filter zurücksetzen",
  "cast.emptyTitle": "Noch keine NPCs",
  "cast.emptyDesc":
    "NPCs sind die Figuren, mit denen deine Spieler am Tisch sprechen. Der Butler kann eine Session allein stemmen; füge deinen ersten NPC hinzu, sobald der Tisch ein Gesicht braucht.",
  "cast.rowNeeds": "Braucht Aufmerksamkeit: {checks}",
  "cast.rowNeedsTitle": "Noch nicht bereit für die Session",
  "cast.rowVoice": "Stimme: {name}",
  "cast.missingPersona": "Keine Persönlichkeit geschrieben",
  "cast.missingVoice": "Keine Stimme",
  "cast.missingLink": "Kein Wiki-Eintrag verknüpft",
  "cast.missingContent": "Wiki-Eintrag ist leer",
  "cast.missingFacts": "Keine Fakten in Reichweite",

  // ── New NPC dialog ───────────────────────────────────────────────────────
  "cast.newNpcTitle": "Neuer NPC",
  "cast.newNpcDesc": "Wer ist das? Ein Name reicht für den Anfang; alles lässt sich später ändern.",
  "cast.newNpcNameRequired": "Gib dem NPC einen Namen.",
  "cast.newNpcNamePlaceholder": "z. B. Mira",
  "cast.newNpcConceptLabel": "Konzept (optional)",
  "cast.newNpcConceptPlaceholder": "ein grantiger Zwergenschmied, der heimlich Gedichte schreibt",
  "cast.newNpcConceptHint":
    "Eine Zeile genügt. Glyphoxa entwirft daraus eine Persönlichkeit, die du prüfen kannst; gespeichert wird der Entwurf erst, wenn du auf Speichern klickst.",
  "cast.newNpcVoiceLabel": "Stimme (optional)",
  "cast.newNpcCreate": "NPC erstellen",
  "cast.newNpcErrorTitle": "NPC konnte nicht erstellt werden",

  // ── Agent editor ─────────────────────────────────────────────────────────
  "cast.aliasesLabel": "Hört auch auf",
  "cast.aliasesHint":
    "Spitznamen, Titel und die Schreibweisen, die die Spracherkennung gern liefert, z. B. „Meera“ für Mira. Nach jedem Namen Enter drücken.",
  "cast.aliasesPlaceholder": "Namen hinzufügen…",
  "cast.nameRequired": "Ein Name ist erforderlich.",
  "cast.revert": "Zurücksetzen",
  "cast.saveBarAria": "Speichern",
  "cast.saveTooltip": "Änderungen speichern ({shortcut})",
  "cast.saveShortcut": "Strg+S",
  "cast.saveShortcutMac": "⌘S",
  "cast.draftFromConcept": "Aus deinem Konzept wird eine Persönlichkeit entworfen…",
  "cast.readinessTitle": "{name} ist noch nicht bereit für die Session",
  "cast.readinessListAria": "Was noch fehlt",
  "cast.trimmed": "Wissen gekürzt {chars}/{maxChars}",
  "cast.trimmedHint":
    "Das Wiki rund um diesen NPC enthält mehr, als in eine Antwort passt; die am weitesten entfernten Fakten wurden weggelassen. Halte den Eintrag selbst und seine engsten Verbindungen kurz.",
  "cast.voiceBrowse": "Stimmen durchstöbern",
  "cast.voiceStop": "Vorschau stoppen",

  // ── Voice picker ─────────────────────────────────────────────────────────
  "cast.voicePickerTitle": "Stimme wählen",
  "cast.voicePickerDesc":
    "Erst anhören, dann wählen. Nichts spielt von selbst: Jede Hörprobe verbraucht Text-to-Speech-Zeichen aus deinem Tarif.",
  "cast.voiceSearchLabel": "Stimmen durchsuchen",
  "cast.voiceSampleLabel": "Beispielsatz",
  "cast.voiceSamplePlaceholder": "Leer lassen für den Standardsatz",
  "cast.voiceSampleHint": "Was jede Stimme sagt, wenn du auf Abspielen drückst. Bis zu {max} Zeichen.",
  "cast.voiceLangChip": "Kampagnensprache: {language}",
  "cast.voiceListAria": "Stimmen",
  "cast.voicePlay": "{name} abspielen",
  "cast.voiceStopNamed": "{name} stoppen",
  "cast.voiceCurrent": "Aktuell",
  "cast.voiceUse": "Diese Stimme verwenden",
  "cast.voiceNoMatch": "Keine Stimme passt zu deiner Suche.",
  "cast.voiceClearSearch": "Alle Stimmen zeigen",
  "cast.voiceCatalogErrorTitle": "Stimmenkatalog konnte nicht geladen werden",
  "cast.voiceCatalogEmptyTitle": "Keine Stimmen verfügbar",
  "cast.voiceCatalogEmptyDesc":
    "Der Stimmenkatalog ist leer. Prüfe den Text-to-Speech-Schlüssel in der Einrichtung.",
  "cast.openSetup": "Einrichtung öffnen",

  // ── Readiness view ───────────────────────────────────────────────────────
  "cast.prepReadyCount": "{ready} von {total} NPCs bereit",
  "cast.prepReadyCountOne": "{ready} von 1 NPC bereit",
  "cast.prepModeAria": "Welche NPCs aufgelistet werden",
  "cast.prepModeAttention": "Braucht Aufmerksamkeit",
  "cast.prepModeAll": "Alle",
  "cast.prepAttentionAria": "NPCs, die Aufmerksamkeit brauchen",
  "cast.prepAllReadyTitle": "Alle sind bereit",
  "cast.prepAllReadyDesc": "Jeder NPC hat eine Stimme, eine Persönlichkeit und Wissen in Reichweite.",
  "cast.prepShowAll": "Alle zeigen",
  "cast.prepLoadErrorTitle": "Bereitschafts-Check konnte nicht geladen werden",
  "cast.prepEmptyTitle": "Noch keine NPCs",
  "cast.prepEmptyDesc": "Der Bereitschafts-Check füllt sich, sobald du NPCs zur Besetzung hinzufügst.",
};
