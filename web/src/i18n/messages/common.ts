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
  "common.edit": "Edit",
  "common.create": "Create",
  "common.loading": "Loading…",
  "common.couldntSave": "Couldn't save: {message}",
  "common.advancedTitle": "Advanced settings",
  "common.advancedHint": "Extra options most groups never need — open only if you're looking for something specific.",
  "common.showAdvanced": "Show advanced settings",
  "common.hideAdvanced": "Hide advanced settings",
  "common.language": "Language",
  "common.displayLanguage": "Display language",
} as const;

export const de: Record<keyof typeof en, string> = {
  "common.save": "Speichern",
  "common.saving": "Wird gespeichert…",
  "common.saveChanges": "Änderungen speichern",
  "common.cancel": "Abbrechen",
  "common.close": "Schließen",
  "common.delete": "Löschen",
  "common.edit": "Bearbeiten",
  "common.create": "Erstellen",
  "common.loading": "Wird geladen…",
  "common.couldntSave": "Speichern fehlgeschlagen: {message}",
  "common.advancedTitle": "Erweiterte Einstellungen",
  "common.advancedHint": "Zusätzliche Optionen, die die meisten Gruppen nie brauchen — nur öffnen, wenn du etwas Bestimmtes suchst.",
  "common.showAdvanced": "Erweiterte Einstellungen anzeigen",
  "common.hideAdvanced": "Erweiterte Einstellungen ausblenden",
  "common.language": "Sprache",
  "common.displayLanguage": "Anzeigesprache",
};
