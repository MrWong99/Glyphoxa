// Strings for Maps, Players and Prep boards (maps.* / players.* / boards.*).
//
// Registered with the shell skeleton (ADR-0063 amendment) so the package that
// fills it only ever edits THIS file — never i18n/index.tsx or i18n/de.ts,
// which several packages would otherwise all have to touch. Add each key to
// `en` and `de` together: the `de` annotation makes a missing German string a
// compile error, and i18n.test.tsx checks the assembled catalogs key for key.
//
// The three views' older strings still live in campaign.ts / components.ts
// (campaign.* / components.*); what is here is what the maps-players-boards
// pass added.

export const en = {
  // ── Maps: list, empty and load states ────────────────────────────────────
  "maps.loadingMaps": "Loading maps…",
  "maps.loadingMap": "Loading the map…",
  "maps.loadErrorTitle": "Couldn't load your maps",
  "maps.openErrorTitle": "Couldn't open this map",
  "maps.stripAria": "Maps",
  "maps.emptyTitle": "No maps yet",
  "maps.emptyDesc":
    "A map gives your world a place for everything in it. Pin wiki entries where they belong, and your NPCs can say where things are.",
  "maps.emptyUpload": "Upload a map",
  "maps.emptyGenerate": "Generate one",

  // ── Maps: add dialog ─────────────────────────────────────────────────────
  "maps.addTitle": "Add a map",
  "maps.addDesc": "Upload a picture you already have, or let the image model draw one.",
  "maps.imageHint": "PNG, JPEG, WebP or GIF, up to {mb} MB.",
  "maps.imageWrongType": "That file type isn't supported. Use a PNG, JPEG, WebP or GIF image.",
  "maps.imageTooLarge": "That image is larger than {mb} MB. Export it smaller and try again.",
  "maps.depictsLabel": "Depicts",
  "maps.depictsNone": "— No entry —",
  "maps.depictsCurrent": "Current entry",
  "maps.depictsHint":
    "The Location entry this map shows. Linking one lets “Suggest placements” read it and propose what belongs on the map.",
  "maps.depictsHintGenerate":
    "The Location entry this map shows. The picture is drawn from its public description, and “Suggest placements” reads it later.",
  "maps.depictsNoLocations":
    "You have no Location entries yet. Add one in the World wiki to link it here; you can also do that later in Map settings.",
  "maps.parentHint": "The larger map this one is a part of, such as the region a town lies in.",
  "maps.parentCurrent": "Current map",

  // ── Maps: settings and image ─────────────────────────────────────────────
  "maps.settingsButton": "Map settings",
  "maps.settingsTitle": "Map settings",
  "maps.settingsDesc": "Rename the map, move it inside another one, link the entry it shows, or hide it from players.",
  "maps.settingsSaved": "Map settings saved",
  "maps.moreActions": "More map actions",
  "maps.replaceImage": "Replace image…",
  "maps.replaceImageFileAria": "New image file",
  "maps.uploadImage": "Upload image",
  "maps.imageReplaced": "Image replaced. Every pin kept its place.",

  // ── Maps: zoom ───────────────────────────────────────────────────────────
  "maps.zoomGroupAria": "Zoom",
  "maps.zoomReadoutAria": "Zoom {pct}%",

  // ── Maps: pins ───────────────────────────────────────────────────────────
  "maps.pinGestureHint":
    "Click a pin for its details. Drag it to move it, or focus it and use the arrow keys (Shift for bigger steps), then press Enter. Hold Ctrl (⌘ on a Mac) and scroll to zoom.",
  "maps.pinCardAria": "Pin: {name}",
  "maps.pinNoBody": "This entry has no description yet.",
  "maps.pinLabelLabel": "Label on this map",
  "maps.pinLabelHint": "Leave empty to show the entry's name.",
  "maps.pinLabelSave": "Save label",
  "maps.pinHideLabel": "Hide from players",
  "maps.pinHiddenByEntry": "The entry itself is GM-only, so this pin is always hidden.",
  "maps.pinOpenEntry": "Open entry",
  "maps.pinRemove": "Remove from map",
  "maps.pinRemoved": "Removed “{name}” from the map",
  "maps.pinRestoreError": "Couldn't put the pin back: {message}",
  "maps.pinPlaced": "Placed “{name}” on the map.",
  "maps.pinMoved": "Moved “{name}”.",
  "maps.pinMoving": "Moving “{name}”: {x}% across, {y}% down. Enter to place, Escape to cancel.",
  "maps.pinMoveCancelled": "“{name}” is back where it was.",
  "maps.nudgeHint": "Moving “{name}” with the arrow keys. Press Enter to place it here, or Escape to put it back.",
  "maps.placeAtCentre": "Place at centre",

  // ── Maps: tray ───────────────────────────────────────────────────────────
  "maps.traySearchAria": "Search entries not on this map",
  "maps.traySearchPlaceholder": "Search entries…",
  "maps.trayTypeFilterAria": "Filter by type",
  "maps.trayTypeAll": "All",
  "maps.trayNoMatches": "No entries match.",
  "maps.trayClearFilters": "Clear filters",
  "maps.suggestNeedsEntry":
    "Link the Location entry this map depicts and “Suggest placements” can propose what belongs here.",
  "maps.suggestLinkEntry": "Open Map settings",

  // ── Players ──────────────────────────────────────────────────────────────
  "players.loading": "Loading players…",
  "players.loadErrorTitle": "Couldn't load your players",
  "players.emptyTitle": "No players yet",
  "players.emptyDesc":
    "Add each player's character and link it to their Discord account. The transcript then names who is speaking, and your NPCs know who they are talking to.",
  "players.inVoice": "in voice",
  "players.notInVoice": "not in voice",
  "players.maskedUser": "Discord user {tail}",
  "players.unlinkedTitle": "In voice, not yet linked",
  "players.unlinkedHint":
    "They are in the voice channel but have no character, so the transcript can't name them.",
  "players.createCharacter": "Create character",
  "players.createCharacterFor": "Create character for {name}",
  "players.newPlayerFor": "A character for {name}",
  "players.badgeLinkedTip": "This player has signed in to Glyphoxa with their Discord account.",
  "players.badgeDiscordIdTip":
    "Known only by their Discord user ID. Their Discord name shows here while they are in the voice channel.",
  "players.memberTaken": "plays {name}",
  "players.idTaken": "This Discord user already plays {name}. One Discord user can have one character per campaign.",
  "players.bindHintNoVoice":
    "Paste the player's Discord user ID. Once they are in the voice channel you can pick them from a list instead.",
  "players.idHelpSummary": "Where do I find a Discord user ID?",
  "players.idHelpStep1": "In Discord, open User Settings, go to Advanced and turn on Developer Mode.",
  "players.idHelpStep2": "Right-click the player (on a phone, press and hold) and choose Copy User ID.",
  "players.idHelpStep3": "Paste it here. It is a long number, like 123456789012345678.",

  // ── Prep boards ──────────────────────────────────────────────────────────
  "boards.loading": "Loading boards…",
  "boards.loadErrorTitle": "Couldn't load the boards",
  "boards.emptyTitle": "No boards yet",
  "boards.emptyDesc": "Make one for tonight and the entries you need are one click away during play.",
  "boards.boardEmpty": "Nothing on this board yet. Add entries below.",
  "boards.actionsAria": "Actions for board {name}",
  "boards.rename": "Rename",
  "boards.renameTitle": "Click to rename",
  "boards.renameAria": "New name for board {name}",
  "boards.renameSave": "Save name",
  "boards.renameCancel": "Cancel renaming",
  "boards.duplicate": "Duplicate",
  "boards.duplicated": "Duplicated “{name}”",
  "boards.copyName": "{name} (copy)",
  "boards.moveUpAria": "Move {name} up",
  "boards.moveDownAria": "Move {name} down",
  "boards.reorderHint": "Alt + ↑ / ↓ moves an entry.",
  "boards.addEntry": "Add entry…",
  "boards.addEntryAria": "Add an entry to {name}",
  "boards.addEntrySearch": "Search entries…",
  "boards.addEntryAllOn": "Every entry is already on this board.",
  "boards.addEntryNoEntries": "No wiki entries yet",
  "boards.newBoard": "New board",
  "boards.nodeEmptyHint":
    "A board is a shortlist for one session. Name one and this entry goes on it. Your NPCs never see boards.",
} as const;

export const de: Record<keyof typeof en, string> = {
  "maps.loadingMaps": "Karten werden geladen…",
  "maps.loadingMap": "Karte wird geladen…",
  "maps.loadErrorTitle": "Deine Karten konnten nicht geladen werden",
  "maps.openErrorTitle": "Diese Karte konnte nicht geöffnet werden",
  "maps.stripAria": "Karten",
  "maps.emptyTitle": "Noch keine Karten",
  "maps.emptyDesc":
    "Eine Karte gibt allem in deiner Welt einen Platz. Setze Wiki-Einträge dort als Pin, wo sie hingehören — dann können deine NPCs sagen, wo etwas liegt.",
  "maps.emptyUpload": "Karte hochladen",
  "maps.emptyGenerate": "Eine generieren",

  "maps.addTitle": "Karte hinzufügen",
  "maps.addDesc": "Lade ein Bild hoch, das du schon hast, oder lass das Bildmodell eins zeichnen.",
  "maps.imageHint": "PNG, JPEG, WebP oder GIF, bis {mb} MB.",
  "maps.imageWrongType": "Dieser Dateityp wird nicht unterstützt. Nimm ein PNG-, JPEG-, WebP- oder GIF-Bild.",
  "maps.imageTooLarge": "Dieses Bild ist größer als {mb} MB. Exportiere es kleiner und versuch es noch einmal.",
  "maps.depictsLabel": "Zeigt",
  "maps.depictsNone": "— Kein Eintrag —",
  "maps.depictsCurrent": "Aktueller Eintrag",
  "maps.depictsHint":
    "Der Ort-Eintrag, den diese Karte zeigt. Ist einer verknüpft, kann „Platzierungen vorschlagen“ ihn lesen und vorschlagen, was auf die Karte gehört.",
  "maps.depictsHintGenerate":
    "Der Ort-Eintrag, den diese Karte zeigt. Das Bild entsteht aus seiner öffentlichen Beschreibung, und „Platzierungen vorschlagen“ liest ihn später.",
  "maps.depictsNoLocations":
    "Du hast noch keine Ort-Einträge. Lege im Welt-Wiki einen an, um ihn hier zu verknüpfen — das geht auch später in den Karteneinstellungen.",
  "maps.parentHint": "Die größere Karte, zu der diese gehört — etwa die Region, in der eine Stadt liegt.",
  "maps.parentCurrent": "Aktuelle Karte",

  "maps.settingsButton": "Karteneinstellungen",
  "maps.settingsTitle": "Karteneinstellungen",
  "maps.settingsDesc":
    "Benenne die Karte um, lege sie in eine andere, verknüpfe den Eintrag, den sie zeigt, oder verbirg sie vor den Spielern.",
  "maps.settingsSaved": "Karteneinstellungen gespeichert",
  "maps.moreActions": "Weitere Kartenaktionen",
  "maps.replaceImage": "Bild ersetzen…",
  "maps.replaceImageFileAria": "Neue Bilddatei",
  "maps.uploadImage": "Bild hochladen",
  "maps.imageReplaced": "Bild ersetzt. Jeder Pin ist an seinem Platz geblieben.",

  "maps.zoomGroupAria": "Zoom",
  "maps.zoomReadoutAria": "Zoom {pct} %",

  "maps.pinGestureHint":
    "Klicke einen Pin an, um seine Details zu sehen. Zieh ihn, um ihn zu verschieben — oder fokussiere ihn, nutze die Pfeiltasten (Umschalt für größere Schritte) und drück Enter. Mit Strg (⌘ am Mac) und Mausrad zoomst du.",
  "maps.pinCardAria": "Pin: {name}",
  "maps.pinNoBody": "Dieser Eintrag hat noch keine Beschreibung.",
  "maps.pinLabelLabel": "Beschriftung auf dieser Karte",
  "maps.pinLabelHint": "Leer lassen, um den Namen des Eintrags zu zeigen.",
  "maps.pinLabelSave": "Beschriftung speichern",
  "maps.pinHideLabel": "Vor Spielern verbergen",
  "maps.pinHiddenByEntry": "Der Eintrag selbst ist nur für den GM, deshalb ist dieser Pin immer verborgen.",
  "maps.pinOpenEntry": "Eintrag öffnen",
  "maps.pinRemove": "Von der Karte entfernen",
  "maps.pinRemoved": "„{name}“ von der Karte entfernt",
  "maps.pinRestoreError": "Der Pin konnte nicht zurückgesetzt werden: {message}",
  "maps.pinPlaced": "„{name}“ auf der Karte platziert.",
  "maps.pinMoved": "„{name}“ verschoben.",
  "maps.pinMoving":
    "„{name}“ wird verschoben: {x} % von links, {y} % von oben. Enter zum Platzieren, Escape zum Abbrechen.",
  "maps.pinMoveCancelled": "„{name}“ ist wieder an seinem alten Platz.",
  "maps.nudgeHint":
    "„{name}“ wird mit den Pfeiltasten verschoben. Drück Enter, um den Pin hier zu platzieren, oder Escape, um ihn zurückzusetzen.",
  "maps.placeAtCentre": "In der Mitte platzieren",

  "maps.traySearchAria": "Einträge suchen, die nicht auf dieser Karte sind",
  "maps.traySearchPlaceholder": "Einträge suchen…",
  "maps.trayTypeFilterAria": "Nach Typ filtern",
  "maps.trayTypeAll": "Alle",
  "maps.trayNoMatches": "Keine passenden Einträge.",
  "maps.trayClearFilters": "Filter zurücksetzen",
  "maps.suggestNeedsEntry":
    "Verknüpfe den Ort-Eintrag, den diese Karte zeigt — dann kann „Platzierungen vorschlagen“ vorschlagen, was hierher gehört.",
  "maps.suggestLinkEntry": "Karteneinstellungen öffnen",

  "players.loading": "Spieler werden geladen…",
  "players.loadErrorTitle": "Deine Spieler konnten nicht geladen werden",
  "players.emptyTitle": "Noch keine Spieler",
  "players.emptyDesc":
    "Lege für jeden Spieler den Charakter an und verknüpfe ihn mit dem Discord-Konto. Dann zeigt die Mitschrift, wer spricht, und deine NPCs wissen, mit wem sie reden.",
  "players.inVoice": "im Sprachkanal",
  "players.notInVoice": "nicht im Sprachkanal",
  "players.maskedUser": "Discord-Nutzer {tail}",
  "players.unlinkedTitle": "Im Sprachkanal, noch nicht verknüpft",
  "players.unlinkedHint":
    "Sie sind im Sprachkanal, haben aber keinen Charakter — die Mitschrift kann sie deshalb nicht benennen.",
  "players.createCharacter": "Charakter erstellen",
  "players.createCharacterFor": "Charakter erstellen für {name}",
  "players.newPlayerFor": "Ein Charakter für {name}",
  "players.badgeLinkedTip": "Dieser Spieler hat sich mit seinem Discord-Konto bei Glyphoxa angemeldet.",
  "players.badgeDiscordIdTip":
    "Nur über die Discord-Nutzer-ID bekannt. Der Discord-Name erscheint hier, solange der Spieler im Sprachkanal ist.",
  "players.memberTaken": "spielt {name}",
  "players.idTaken":
    "Dieser Discord-Nutzer spielt bereits {name}. Ein Discord-Nutzer kann pro Kampagne einen Charakter haben.",
  "players.bindHintNoVoice":
    "Füge die Discord-Nutzer-ID des Spielers ein. Sobald er im Sprachkanal ist, kannst du ihn stattdessen aus einer Liste wählen.",
  "players.idHelpSummary": "Wo finde ich eine Discord-Nutzer-ID?",
  "players.idHelpStep1":
    "Öffne in Discord die Benutzereinstellungen, geh zu „Erweitert“ und schalte den Entwicklermodus ein.",
  "players.idHelpStep2":
    "Klicke mit der rechten Maustaste auf den Spieler (am Handy: gedrückt halten) und wähle „Nutzer-ID kopieren“.",
  "players.idHelpStep3": "Füge sie hier ein. Es ist eine lange Zahl, etwa 123456789012345678.",

  "boards.loading": "Boards werden geladen…",
  "boards.loadErrorTitle": "Die Boards konnten nicht geladen werden",
  "boards.emptyTitle": "Noch keine Boards",
  "boards.emptyDesc": "Lege eins für heute Abend an und die Einträge, die du brauchst, sind im Spiel nur einen Klick entfernt.",
  "boards.boardEmpty": "Noch nichts auf diesem Board. Füge unten Einträge hinzu.",
  "boards.actionsAria": "Aktionen für Board {name}",
  "boards.rename": "Umbenennen",
  "boards.renameTitle": "Zum Umbenennen klicken",
  "boards.renameAria": "Neuer Name für Board {name}",
  "boards.renameSave": "Namen speichern",
  "boards.renameCancel": "Umbenennen abbrechen",
  "boards.duplicate": "Duplizieren",
  "boards.duplicated": "„{name}“ dupliziert",
  "boards.copyName": "{name} (Kopie)",
  "boards.moveUpAria": "{name} nach oben verschieben",
  "boards.moveDownAria": "{name} nach unten verschieben",
  "boards.reorderHint": "Alt + ↑ / ↓ verschiebt einen Eintrag.",
  "boards.addEntry": "Eintrag hinzufügen…",
  "boards.addEntryAria": "Eintrag zu {name} hinzufügen",
  "boards.addEntrySearch": "Einträge suchen…",
  "boards.addEntryAllOn": "Alle Einträge sind schon auf diesem Board.",
  "boards.addEntryNoEntries": "Noch keine Wiki-Einträge",
  "boards.newBoard": "Neues Board",
  "boards.nodeEmptyHint":
    "Ein Board ist eine Merkliste für eine Session. Gib einem einen Namen, und dieser Eintrag kommt darauf. Deine NPCs sehen Boards nie.",
};
