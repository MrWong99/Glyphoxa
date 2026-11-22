// Strings for the Campaigns page (every campaign at the table, lifecycle
// actions), the campaign's Settings view and the topbar quick switcher.
//
// Registered empty with the shell skeleton (ADR-0063 amendment) so the package
// that fills it only ever edits THIS file — never i18n/index.tsx or i18n/de.ts,
// which several packages would otherwise all have to touch. Add each key to
// `en` and `de` together: the `de` annotation makes a missing German string a
// compile error, and i18n.test.tsx checks the assembled catalogs key for key.
//
// Vocabulary (CONTEXT.md): the exported file is a "Campaign Bundle" ("bundle"
// once it is clear which), never a "backup" or an "export" on its own; the
// campaign in use is the "active campaign"; "archive" means the archived state
// of a campaign and nothing else.

export const en = {
  // ── Campaigns page ───────────────────────────────────────────────────────
  "campaigns.metaSystem": "System",
  "campaigns.metaLanguage": "Language",
  "campaigns.metaCreated": "Created",
  // updated_at is when the campaign's own row last changed (a rename, a
  // setting) — NOT when it was last played. Never label it "last played".
  "campaigns.metaEdited": "Edited",
  "campaigns.metaArchived": "Archived",
  "campaigns.metaNone": "Not set",
  "campaigns.working": "Working…",
  "campaigns.archivedSection": "Archived ({count})",
  "campaigns.archivedList": "Archived campaigns",
  "campaigns.unarchiveNamed": "Unarchive {name}",
  "campaigns.searchLabel": "Search campaigns",
  "campaigns.searchPlaceholder": "Search by name or system…",
  "campaigns.sortLabel": "Sort campaigns by",
  "campaigns.sortName": "Name",
  "campaigns.sortEdited": "Recently edited",
  "campaigns.noMatchesTitle": "No campaign matches “{query}”",
  "campaigns.noMatchesDesc": "Check the spelling, or clear the search to see every campaign again.",
  "campaigns.clearSearch": "Clear search",
  "campaigns.emptyTitle": "No campaigns yet",
  "campaigns.emptyDesc":
    "A campaign holds your cast of NPCs, the world wiki, the maps and the sessions you play. Create your first one, or import a Campaign Bundle from another table.",
  "campaigns.allArchivedTitle": "Every campaign is archived",
  "campaigns.allArchivedDesc": "Unarchive one below to work in it again, or start a new campaign.",
  "campaigns.loadErrorTitle": "Couldn't load your campaigns",

  // ── Card menu ────────────────────────────────────────────────────────────
  "campaigns.menuSettings": "Settings",
  "campaigns.menuExport": "Export bundle…",
  "campaigns.menuDuplicate": "Duplicate…",
  "campaigns.menuArchiveAsk": "Archive…",

  // ── New campaign ─────────────────────────────────────────────────────────
  "campaigns.newCampaignDesc":
    "A campaign gets its own Butler, cast, world wiki and maps. Everything here can be changed later.",
  "campaigns.creating": "Creating…",
  "campaigns.created": "Created {name} — your Butler is ready",
  "campaigns.createdDeferred":
    "Created {name}. A voice session is live — it becomes the active campaign when the session ends.",

  // ── Settings (dialog and view) ───────────────────────────────────────────
  "campaigns.settingsTitle": "Settings — {name}",
  "campaigns.sectionGeneral": "General",
  "campaigns.sectionHighlights": "Session Highlights",
  "campaigns.highlightsLede":
    "Glyphoxa can clip the best moments of a session. Nothing is recorded until you switch recording on, and then only for the players who consent.",
  "campaigns.nameRequired": "A campaign needs a name.",
  "campaigns.sectionData": "Data",
  "campaigns.dataExportTitle": "Campaign Bundle",
  "campaigns.dataExportDesc":
    "One file with this campaign's NPCs, world wiki, maps, players and planning chats — and, if you want, its session history and map images. Keep it as a copy, or import it at another table.",
  "campaigns.sectionDanger": "Danger zone",
  "campaigns.archiveRowTitle": "Archive this campaign",
  "campaigns.archiveRowDesc":
    "Hides it from the campaign switcher and from Discord. Nothing is deleted, and you can unarchive it on the Campaigns page.",
  "campaigns.deleteRowTitle": "Delete this campaign",
  "campaigns.deleteRowDesc":
    "Permanently removes the campaign and everything in it. This cannot be undone.",
  "campaigns.settingsLoadErrorTitle": "Couldn't load this campaign's settings",

  // ── Export ───────────────────────────────────────────────────────────────
  "campaigns.exportTitle": "Export “{name}”",
  "campaigns.exportDesc":
    "Downloads a Campaign Bundle: one file with the campaign's NPCs, world wiki, maps, players and planning chats. API keys are never part of it.",
  "campaigns.exportHistory": "Session history",
  "campaigns.exportHistoryHint": "Every voice session with its transcript, and where each NPC appeared.",
  "campaigns.exportImages": "Map images",
  "campaigns.exportImagesHint":
    "The picture behind every map. Without them the maps keep their pins but arrive without an image.",
  "campaigns.exportSizeNote":
    "With map images and a long history a bundle can reach many megabytes, and packing it takes a moment.",
  "campaigns.exportDownload": "Download bundle",
  "campaigns.exportPacking": "Packing…",
  "campaigns.exported": "Bundle of {name} downloaded",
  "campaigns.exportFailed": "Couldn't export the campaign",
  "campaigns.bundleBusyTitle": "The server is busy with another bundle",
  "campaigns.bundleBusy":
    "Another import or export is running right now. Wait a few seconds, then try again.",

  // ── Archive / delete ─────────────────────────────────────────────────────
  "campaigns.archived": "Archived {name}",
  "campaigns.restored": "Unarchived {name}",
  "campaigns.restoredActive": "Unarchived {name} — it is your active campaign again",
  "campaigns.deleted": "Deleted {name}",
  "campaigns.archivedNotDeleted": "{name} is archived now, but couldn't be deleted: {message}",
  "campaigns.liveRefusal":
    "{name} is in a live voice session. End the session first, then try again.",
  "campaigns.archiveTitle": "Archive “{name}”?",
  "campaigns.archiveActiveDesc":
    "This is the campaign you are working in. Archiving hides it from the campaign switcher and from Discord; nothing is deleted, and you can unarchive it at any time.",
  "campaigns.nextActive": "“{name}” becomes the active campaign.",
  "campaigns.nextActiveNone":
    "It is your only campaign that is not archived, so no campaign will be active afterwards.",
  "campaigns.nextActiveUnknown": "Another campaign becomes the active one.",
  "campaigns.deleteDescActive":
    "This archives the campaign and then permanently deletes it with everything in it — NPCs, world wiki, maps, players, planning chats, transcripts and voice sessions. This cannot be undone.",
  "campaigns.backupFirst": "Download full bundle first",
  "campaigns.backupDone": "Bundle downloaded.",

  // ── Duplicate ────────────────────────────────────────────────────────────
  "campaigns.duplicate": "Duplicate",
  "campaigns.duplicateTitle": "Duplicate “{name}”?",
  "campaigns.duplicateDesc":
    "Creates “{copy}” with the same NPCs, world wiki, maps (with their images), players and planning chats. The session history is not copied.",
  "campaigns.copyName": "{name} (copy)",
  "campaigns.duplicating": "Duplicating {name}…",
  "campaigns.duplicated": "Duplicated as {name}",
  "campaigns.duplicatedNoImages":
    "Duplicated as {name} — without the map images, which were too large to copy.",
  "campaigns.duplicatedNotRenamed": "A copy of {name} was created, but couldn't be renamed: {message}",
  "campaigns.couldntDuplicate": "Couldn't duplicate {name}: {message}",

  // ── Quick switcher ───────────────────────────────────────────────────────
  "campaigns.allCampaigns": "All campaigns",
  "campaigns.next": "Next: {name}",
  "campaigns.nextBadge": "Next",
  "campaigns.switcherLive": "Switch campaign — live voice session in {name}",
  "campaigns.switcherLiveNext": "Switch campaign — live voice session in {name}, next: {next}",
  "campaigns.changedElsewhere": "Active campaign changed to {name} (another tab or Discord)",
  "campaigns.switchBack": "Switch back",
} as const;

export const de: Record<keyof typeof en, string> = {
  "campaigns.metaSystem": "System",
  "campaigns.metaLanguage": "Sprache",
  "campaigns.metaCreated": "Erstellt",
  "campaigns.metaEdited": "Geändert",
  "campaigns.metaArchived": "Archiviert",
  "campaigns.metaNone": "Nicht festgelegt",
  "campaigns.working": "Wird bearbeitet…",
  "campaigns.archivedSection": "Archiviert ({count})",
  "campaigns.archivedList": "Archivierte Kampagnen",
  "campaigns.unarchiveNamed": "{name} wiederherstellen",
  "campaigns.searchLabel": "Kampagnen durchsuchen",
  "campaigns.searchPlaceholder": "Nach Name oder System suchen…",
  "campaigns.sortLabel": "Kampagnen sortieren nach",
  "campaigns.sortName": "Name",
  "campaigns.sortEdited": "Zuletzt geändert",
  "campaigns.noMatchesTitle": "Keine Kampagne passt zu „{query}“",
  "campaigns.noMatchesDesc":
    "Prüfe die Schreibweise oder leere die Suche, um wieder alle Kampagnen zu sehen.",
  "campaigns.clearSearch": "Suche leeren",
  "campaigns.emptyTitle": "Noch keine Kampagnen",
  "campaigns.emptyDesc":
    "Eine Kampagne enthält deine NPCs, das Welt-Wiki, die Karten und die Sessions, die ihr spielt. Erstelle deine erste oder importiere ein Kampagnen-Bundle von einem anderen Tisch.",
  "campaigns.allArchivedTitle": "Alle Kampagnen sind archiviert",
  "campaigns.allArchivedDesc":
    "Stelle unten eine wieder her, um darin weiterzuarbeiten, oder beginne eine neue Kampagne.",
  "campaigns.loadErrorTitle": "Deine Kampagnen konnten nicht geladen werden",

  "campaigns.menuSettings": "Einstellungen",
  "campaigns.menuExport": "Bundle exportieren…",
  "campaigns.menuDuplicate": "Duplizieren…",
  "campaigns.menuArchiveAsk": "Archivieren…",

  "campaigns.newCampaignDesc":
    "Eine Kampagne bekommt ihren eigenen Butler, ihre NPCs, ihr Welt-Wiki und ihre Karten. Alles hier lässt sich später ändern.",
  "campaigns.creating": "Wird erstellt…",
  "campaigns.created": "{name} erstellt — dein Butler ist bereit",
  "campaigns.createdDeferred":
    "{name} erstellt. Gerade läuft eine Session — sie wird zur aktiven Kampagne, sobald die Session endet.",

  "campaigns.settingsTitle": "Einstellungen — {name}",
  "campaigns.sectionGeneral": "Allgemein",
  "campaigns.sectionHighlights": "Session-Highlights",
  "campaigns.highlightsLede":
    "Glyphoxa kann die besten Momente einer Session als Clip festhalten. Aufgenommen wird erst, wenn du die Aufnahme einschaltest — und dann nur, wer zustimmt.",
  "campaigns.nameRequired": "Eine Kampagne braucht einen Namen.",
  "campaigns.sectionData": "Daten",
  "campaigns.dataExportTitle": "Kampagnen-Bundle",
  "campaigns.dataExportDesc":
    "Eine Datei mit den NPCs, dem Welt-Wiki, den Karten, Spielern und Planungs-Chats dieser Kampagne — und, wenn du willst, mit dem Session-Verlauf und den Kartenbildern. Behalte sie als Kopie oder importiere sie an einem anderen Tisch.",
  "campaigns.sectionDanger": "Gefahrenzone",
  "campaigns.archiveRowTitle": "Diese Kampagne archivieren",
  "campaigns.archiveRowDesc":
    "Blendet sie in der Kampagnen-Auswahl und in Discord aus. Nichts wird gelöscht, und du kannst sie auf der Kampagnen-Seite wiederherstellen.",
  "campaigns.deleteRowTitle": "Diese Kampagne löschen",
  "campaigns.deleteRowDesc":
    "Entfernt die Kampagne und alles darin endgültig. Das kann nicht rückgängig gemacht werden.",
  "campaigns.settingsLoadErrorTitle":
    "Die Einstellungen dieser Kampagne konnten nicht geladen werden",

  "campaigns.exportTitle": "„{name}“ exportieren",
  "campaigns.exportDesc":
    "Lädt ein Kampagnen-Bundle herunter: eine Datei mit den NPCs, dem Welt-Wiki, den Karten, Spielern und Planungs-Chats der Kampagne. API-Schlüssel sind nie enthalten.",
  "campaigns.exportHistory": "Session-Verlauf",
  "campaigns.exportHistoryHint":
    "Jede Session mit ihrer Mitschrift und den Auftritten der einzelnen NPCs.",
  "campaigns.exportImages": "Kartenbilder",
  "campaigns.exportImagesHint":
    "Das Bild hinter jeder Karte. Ohne sie behalten die Karten ihre Pins, kommen aber ohne Bild an.",
  "campaigns.exportSizeNote":
    "Mit Kartenbildern und langem Verlauf kann ein Bundle viele Megabyte groß werden, und das Packen dauert einen Moment.",
  "campaigns.exportDownload": "Bundle herunterladen",
  "campaigns.exportPacking": "Wird gepackt…",
  "campaigns.exported": "Bundle von {name} heruntergeladen",
  "campaigns.exportFailed": "Die Kampagne konnte nicht exportiert werden",
  "campaigns.bundleBusyTitle": "Der Server ist mit einem anderen Bundle beschäftigt",
  "campaigns.bundleBusy":
    "Gerade läuft ein anderer Import oder Export. Warte ein paar Sekunden und versuch es dann noch einmal.",

  "campaigns.archived": "{name} archiviert",
  "campaigns.restored": "{name} wiederhergestellt",
  "campaigns.restoredActive": "{name} wiederhergestellt — sie ist wieder deine aktive Kampagne",
  "campaigns.deleted": "{name} gelöscht",
  "campaigns.archivedNotDeleted":
    "{name} ist jetzt archiviert, konnte aber nicht gelöscht werden: {message}",
  "campaigns.liveRefusal":
    "In {name} läuft gerade eine Session. Beende zuerst die Session und versuch es dann noch einmal.",
  "campaigns.archiveTitle": "„{name}“ archivieren?",
  "campaigns.archiveActiveDesc":
    "Das ist die Kampagne, in der du gerade arbeitest. Archivieren blendet sie in der Kampagnen-Auswahl und in Discord aus; nichts wird gelöscht, und du kannst sie jederzeit wiederherstellen.",
  "campaigns.nextActive": "„{name}“ wird zur aktiven Kampagne.",
  "campaigns.nextActiveNone":
    "Sie ist deine einzige nicht archivierte Kampagne — danach ist keine Kampagne aktiv.",
  "campaigns.nextActiveUnknown": "Eine andere Kampagne wird zur aktiven.",
  "campaigns.deleteDescActive":
    "Das archiviert die Kampagne und löscht sie dann endgültig — mit allen NPCs, dem Welt-Wiki, Karten, Spielern, Planungs-Chats, Mitschriften und Sessions. Das kann nicht rückgängig gemacht werden.",
  "campaigns.backupFirst": "Zuerst vollständiges Bundle herunterladen",
  "campaigns.backupDone": "Bundle heruntergeladen.",

  "campaigns.duplicate": "Duplizieren",
  "campaigns.duplicateTitle": "„{name}“ duplizieren?",
  "campaigns.duplicateDesc":
    "Erstellt „{copy}“ mit denselben NPCs, demselben Welt-Wiki, den Karten (samt Bildern), Spielern und Planungs-Chats. Der Session-Verlauf wird nicht kopiert.",
  "campaigns.copyName": "{name} (Kopie)",
  "campaigns.duplicating": "{name} wird dupliziert…",
  "campaigns.duplicated": "Dupliziert als {name}",
  "campaigns.duplicatedNoImages":
    "Dupliziert als {name} — ohne die Kartenbilder, die zum Kopieren zu groß waren.",
  "campaigns.duplicatedNotRenamed":
    "Eine Kopie von {name} wurde erstellt, konnte aber nicht umbenannt werden: {message}",
  "campaigns.couldntDuplicate": "{name} konnte nicht dupliziert werden: {message}",

  "campaigns.allCampaigns": "Alle Kampagnen",
  "campaigns.next": "Als Nächstes: {name}",
  "campaigns.nextBadge": "Als Nächstes",
  "campaigns.switcherLive": "Kampagne wechseln — Session läuft in {name}",
  "campaigns.switcherLiveNext": "Kampagne wechseln — Session läuft in {name}, als Nächstes: {next}",
  "campaigns.changedElsewhere":
    "Aktive Kampagne wurde zu {name} gewechselt (anderer Tab oder Discord)",
  "campaigns.switchBack": "Zurückwechseln",
};
