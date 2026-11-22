// Shared UI strings used across screens. Every fragment under i18n/messages/
// exports an `en` object (the key source of truth) and a `de` object typed
// Record<keyof typeof en, string>, so a missing German string is a compile
// error in THIS file, not a silent English fallback at runtime.

export const en = {
  "common.save": "Save",
  "common.saving": "Saving…",
  "common.saveChanges": "Save changes",
  "common.cancel": "Cancel",
  "common.close": "Close",
  "common.delete": "Delete",
  "common.loading": "Loading…",
  "common.couldntSave": "Couldn't save: {message}",
  "common.advancedTitle": "Advanced settings",
  "common.advancedHint": "Extra options most groups never need — open only if you're looking for something specific.",
  "common.language": "Language",
  "common.displayLanguage": "Display language",
  "common.discardChangesTitle": "Discard unsaved changes?",
  "common.discardChangesDesc": "You have edits that haven't been saved yet. If you continue, they are lost.",
  "common.discardChangesConfirm": "Discard changes",
  // The shared primitive layer (components/ui): state, overlay and form
  // primitives speak through these so every screen loads, fails, empties and
  // confirms in the same words.
  "common.retry": "Try again",
  "common.loadFailedTitle": "Couldn't load this",
  "common.loadFailedDesc": "Something went wrong while loading. Try again in a moment.",
  "common.errorDetails": "Technical details",
  "common.undo": "Undo",
  "common.dismiss": "Dismiss",
  "common.removeItem": "Remove {item}",
  "common.required": "required",
  // The inline status beside a Save button in an editor that stays open
  // (ADR-0017 feedback rule): one wording for every such editor.
  "common.unsavedChanges": "Unsaved changes",
  "common.saved": "Saved",
  // The plan-allowance refusal (ADR-0055), shared by every AI feature that
  // spends provider money: drafting, maps, portraits, recaps, previews, sound.
  // The gate is per PLAN over the UTC calendar month, so the only true remedy
  // is the reset: a saved key does not lift it and there is no self-service
  // plan change. chat.errAllowance is the planning chat's wording of the same
  // refusal; keep the two saying the same things (i18n.test.tsx checks).
  "common.errAllowance":
    "This month's included usage is used up. AI generation is paused until it resets at the start of next month.",
} as const;

export const de: Record<keyof typeof en, string> = {
  "common.save": "Speichern",
  "common.saving": "Wird gespeichert…",
  "common.saveChanges": "Änderungen speichern",
  "common.cancel": "Abbrechen",
  "common.close": "Schließen",
  "common.delete": "Löschen",
  "common.loading": "Wird geladen…",
  "common.couldntSave": "Speichern fehlgeschlagen: {message}",
  "common.advancedTitle": "Erweiterte Einstellungen",
  "common.advancedHint": "Zusätzliche Optionen, die die meisten Gruppen nie brauchen — nur öffnen, wenn du etwas Bestimmtes suchst.",
  "common.language": "Sprache",
  "common.displayLanguage": "Anzeigesprache",
  "common.discardChangesTitle": "Ungespeicherte Änderungen verwerfen?",
  "common.discardChangesDesc": "Du hast Änderungen, die noch nicht gespeichert sind. Wenn du fortfährst, gehen sie verloren.",
  "common.discardChangesConfirm": "Änderungen verwerfen",
  "common.retry": "Erneut versuchen",
  "common.loadFailedTitle": "Das konnte nicht geladen werden",
  "common.loadFailedDesc": "Beim Laden ist etwas schiefgegangen. Versuch es gleich noch einmal.",
  "common.errorDetails": "Technische Details",
  "common.undo": "Rückgängig",
  "common.dismiss": "Ausblenden",
  "common.removeItem": "{item} entfernen",
  "common.required": "erforderlich",
  "common.unsavedChanges": "Ungespeicherte Änderungen",
  "common.saved": "Gespeichert",
  "common.errAllowance":
    "Das inklusive Kontingent für diesen Monat ist aufgebraucht. KI-Generierung pausiert, bis es zu Anfang des nächsten Monats zurückgesetzt wird.",
};
