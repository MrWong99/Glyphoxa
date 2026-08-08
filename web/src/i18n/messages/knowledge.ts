// The Knowledge area's copy: the Campaign screen's wiki list, the relationship
// map (graph), the checkup (world health) panel and the suggestions (proposal)
// queue. `en` is the key source of truth; `de` must cover every key
// (compile-checked via the Record type below).
//
// Naming note: the code keeps the Node/Edge/Proposal domain terms; the COPY
// never shows them. A Node is an "entry", an Edge a "connection", a Knowledge
// Proposal a "suggestion", an Aspect a "fact", and gm_private is "GM-only".

export const en = {
  // ---- Entry-type labels (wire enums untouched; these are display-only) ----
  "knowledge.typeCharacter": "Character",
  "knowledge.typeNpc": "NPC",
  "knowledge.typeLocation": "Location",
  "knowledge.typeFaction": "Faction",
  "knowledge.typeItem": "Item",
  "knowledge.typePlotThread": "Plot thread",
  "knowledge.typeNote": "Note",

  // ---- Connection-type labels (plain language; wire stays snake_case) ----
  "knowledge.edgeLivesIn": "lives in",
  "knowledge.edgeMemberOf": "member of",
  "knowledge.edgeOwns": "owns",
  "knowledge.edgeKnows": "knows",
  "knowledge.edgeEnemyOf": "enemy of",
  "knowledge.edgeAllyOf": "ally of",
  "knowledge.edgeParentOf": "parent of",
  "knowledge.edgeTookPartIn": "took part in",
  "knowledge.edgeMentionedIn": "mentioned in",

  // ---- Disposition scale ----
  "knowledge.dispositionHostile": "hostile",
  "knowledge.dispositionWary": "wary",
  "knowledge.dispositionNeutral": "neutral",
  "knowledge.dispositionWarm": "warm",
  "knowledge.dispositionDevoted": "devoted",

  // ---- Wiki panel: modes, search, list ----
  "knowledge.loadEntriesError": "Couldn't load entries: {message}",
  "knowledge.viewModeAria": "View mode",
  "knowledge.viewList": "List",
  "knowledge.viewMap": "Relationship map",
  "knowledge.viewCheckup": "Checkup",
  "knowledge.filterByTagAria": "Filter by tag",
  "knowledge.loadMapError": "Couldn't load the relationship map: {message}",
  "knowledge.searchAria": "Search entries",
  "knowledge.searchPlaceholder": "Search the wiki — names and content",
  "knowledge.searchError": "Couldn't search: {message}",
  "knowledge.noMatches": "No entries match “{query}”.",
  "knowledge.emptyList": "No entries yet. Add what the world knows and your NPCs will speak to it.",

  // ---- Draft generator ----
  "knowledge.draftOpen": "Generate entries with AI…",
  "knowledge.draftTitle": "Generate entries",
  "knowledge.draftCloseAria": "Close generator",
  "knowledge.draftPromptLabel": "What should be drafted?",
  "knowledge.draftPromptPlaceholder":
    "e.g. The thieves' guild of Saltmarsh — its leader, hideout, prized relic and rival faction",
  "knowledge.draftPromptHint":
    "Your AI model drafts linked entries from this. Nothing is saved until you review and apply the draft.",
  "knowledge.draftGenerate": "Generate draft",
  "knowledge.draftGenerating": "Drafting…",
  "knowledge.draftGenerateError": "Couldn't generate: {message}",
  "knowledge.draftReviewHint": "A draft — nothing is saved yet. Remove what doesn't fit, then apply.",
  "knowledge.draftRemoveEntryAria": "Remove {name} from the draft",
  "knowledge.draftRemoveConnectionAria": "Remove this connection from the draft",
  "knowledge.draftApplyOne": "Add 1 entry",
  "knowledge.draftApplyMany": "Add {n} entries",
  "knowledge.draftApplyConnectionsOne": " + 1 connection",
  "knowledge.draftApplyConnectionsMany": " + {n} connections",
  "knowledge.draftApplying": "Adding…",
  "knowledge.draftDiscard": "Discard draft",
  "knowledge.draftApplyError": "Couldn't apply: {message}",
  "knowledge.draftAddedOne": "Added 1 entry",
  "knowledge.draftAddedMany": "Added {n} entries",
  "knowledge.draftAddedConnectionsOne": " and 1 connection",
  "knowledge.draftAddedConnectionsMany": " and {n} connections",

  // ---- Entry delete confirmation ----
  "knowledge.deleteCounting": "Counting its connections…",
  "knowledge.deleteCountFailed":
    "Its connections couldn't be counted, but any it has will be deleted too.",
  "knowledge.deleteCascadeBefore": "This also deletes its ",
  "knowledge.deleteCascadeAfter": ".",
  "knowledge.connectionCountOne": "1 connection",
  "knowledge.connectionCountMany": "{n} connections",
  "knowledge.deleteEntryTitle": "Delete “{name}”?",
  "knowledge.deleteEntryBody": "This permanently deletes this entry. It can't be undone.",
  "knowledge.deleteEntry": "Delete entry",

  // ---- Entry card ----
  "knowledge.gmOnlyBadge": "GM-only",
  "knowledge.voicedBadge": "Voiced",
  "knowledge.editEntryAria": "Edit {name}",
  "knowledge.deleteNamedEntryAria": "Delete {name}",

  // ---- Facts editor ----
  "knowledge.factsLabel": "Facts",
  "knowledge.factsHint":
    "Each fact hides on its own — an entry can be public and still keep a secret.",
  "knowledge.factKeyAria": "Fact {n} label",
  "knowledge.factKeyPlaceholder": "Role",
  "knowledge.factTextAria": "Fact {n} text",
  "knowledge.factTextPlaceholder": "Runs the Rusty Anchor",
  "knowledge.factMoveUpAria": "Move fact {n} up",
  "knowledge.factMoveDownAria": "Move fact {n} down",
  "knowledge.factMakePublicAria": "Make fact {n} public",
  "knowledge.factMakeGmOnlyAria": "Make fact {n} GM-only",
  "knowledge.factRemoveAria": "Remove fact {n}",
  "knowledge.addFact": "Add fact",

  // ---- Entry editor ----
  "knowledge.editEntryTitle": "Edit entry",
  "knowledge.addEntryTitle": "Add entry",
  "knowledge.typeLabel": "Type",
  "knowledge.nameLabel": "Name",
  "knowledge.namePlaceholder": "What is it called?",
  "knowledge.contentLabel": "Content",
  "knowledge.contentHint": "What the world knows. Your NPCs can draw on public entries.",
  "knowledge.gmOnlySwitch": "GM-only — hidden from your NPCs",
  "knowledge.gmOnlyHint": "GM-only entries stay searchable for you and never reach the table.",
  "knowledge.saveEntry": "Save entry",
  "knowledge.deleteError": "Couldn't delete: {message}",

  // ---- Connections (relations editor) ----
  "knowledge.voicedByLabel": "Voiced by",
  "knowledge.noneOption": "— None —",
  "knowledge.voicedByHint":
    "Optional. Links this entry to a cast NPC — it then knows its own entry and its connections.",
  "knowledge.linkAgentError": "Couldn't link the NPC: {message}",
  "knowledge.connectionsHeading": "Connections · {n}",
  "knowledge.addConnection": "Add connection",
  "knowledge.connectionLabel": "Connection",
  "knowledge.connectionPlaceholder": "Connection…",
  "knowledge.targetEntryLabel": "Target entry",
  "knowledge.targetEntryPlaceholder": "Which entry?",
  "knowledge.connectionTypedHint":
    "Connections are typed: “lives in” points at a location, “member of” at a faction.",
  "knowledge.add": "Add",
  "knowledge.addConnectionError": "Couldn't add: {message}",
  "knowledge.outgoingConnectionsAria": "Outgoing connections",
  "knowledge.incomingConnectionsAria": "Incoming connections",
  "knowledge.noOutgoingConnections": "No outgoing connections yet.",
  "knowledge.deleteConnectionError": "Couldn't delete the connection: {message}",
  "knowledge.oneWayHint":
    "Connections are one-way. Incoming ones are shown for context and edited from the other entry.",
  "knowledge.deleteConnectionTitle": "Delete this connection?",
  "knowledge.deleteConnectionBefore": "Removes the ",
  "knowledge.deleteConnectionAfter": " connection. This can't be undone.",
  "knowledge.deleteConnection": "Delete connection",
  "knowledge.editConnectionAria": "Edit what {relation} {target} is like",
  "knowledge.deleteConnectionAria": "Delete connection {relation} {target}",
  "knowledge.connectionNoteLabel": "What it's like",
  "knowledge.connectionNotePlaceholder": "owes you money since the siege",
  "knowledge.dispositionSelectLabel": "How you feel",
  "knowledge.incomingTag": "incoming",

  // ---- Tags ----
  "knowledge.tagsLabel": "Tags",
  "knowledge.tagsHint": "Your own labels for finding things. Your NPCs never see them.",
  "knowledge.removeTagAria": "Remove tag {tag}",
  "knowledge.addTagAria": "Add a tag",
  "knowledge.tagsPlaceholder": "seafaring, act two, needs a voice…",
  "knowledge.saveTagsError": "Couldn't save tags: {message}",

  // ---- Mentions (appearances) ----
  "knowledge.mentionedLabel": "Mentioned at the table",
  "knowledge.mentionedEmpty": "Not mentioned yet. Sessions are indexed when they end.",
  "knowledge.mentionAria": "{who} at {when}: {text}",
  "knowledge.unknownTime": "an unknown time",

  // ---- Suggestions (proposals) queue ----
  "knowledge.loadSuggestionsError": "Couldn't load suggestions: {message}",
  "knowledge.noSuggestions":
    "No pending suggestions. When your NPCs learn something new at the table, they'll suggest adding it here.",
  "knowledge.kindFact": "Fact",
  "knowledge.kindConnection": "Connection",
  "knowledge.kindNewEntry": "New entry",
  "knowledge.kindUnreadable": "Unreadable",
  "knowledge.proposalNewNode": "New {type}:",
  "knowledge.unreadableSuggestion": "Unreadable suggestion",
  "knowledge.showSimilar": "Show similar entries",
  "knowledge.similarTitle": "Similar existing entries",
  "knowledge.similarEmpty": "No similar entries.",
  "knowledge.addToWiki": "Add to wiki",
  "knowledge.dismiss": "Dismiss",
  "knowledge.dismissTitle": "Dismiss this suggestion?",
  "knowledge.dismissBody":
    "The suggestion is dropped and won't be added to your wiki. This can't be undone.",
  "knowledge.dismissConfirm": "Dismiss suggestion",
  "knowledge.addToWikiError": "Couldn't add: {message}",
  "knowledge.dismissError": "Couldn't dismiss: {message}",

  // ---- Relationship map (graph view) ----
  "knowledge.filterByTypeAria": "Filter by type",
  "knowledge.filterByConnectionAria": "Filter by connection",
  "knowledge.tableView": "Table view",
  "knowledge.suggestionsToggle": "Suggestions ({n})",
  "knowledge.zoomInAria": "Zoom in",
  "knowledge.zoomOutAria": "Zoom out",
  "knowledge.fit": "Fit",
  "knowledge.rearrange": "Re-arrange",
  "knowledge.focusedOn": "Focused on {name}",
  "knowledge.depth": "Depth {n}",
  "knowledge.clearFocus": "Clear focus",
  "knowledge.mapEmptySuggestionsHidden": "Nothing to draw here. Suggestions are hidden in table view.",
  "knowledge.mapEmptyFiltered":
    "Nothing to draw — every entry is filtered out. Turn a type chip back on.",
  "knowledge.mapAria": "Relationship map: {n} entries, {m} connections",
  "knowledge.ariaGmOnly": ", GM-only",
  "knowledge.ariaHiddenFrom": ", hidden from {agent}",
  "knowledge.ariaDropped": ", dropped from {agent}'s prompt — over budget",
  "knowledge.tooltipHiddenFrom": "Hidden from {agent} — this entry is GM-only",
  "knowledge.tooltipDropped": "Dropped from {agent}'s prompt — the facts block is full",
  "knowledge.suggestedConnectionAria": "Suggested connection from {agent} — review",
  "knowledge.suggestedFactAria": "Suggested fact from {agent} — review",
  "knowledge.suggestedFactTitle": "{agent} suggests a fact here",
  "knowledge.suggestedEntryAria": "Suggested new entry {name} ({type}) from {agent} — review",
  "knowledge.suggestedEntryTitle": "{agent} suggests creating {name}",
  "knowledge.unplacedOne": "1 suggestion can't be drawn here",
  "knowledge.unplacedMany": "{n} suggestions can't be drawn here",
  "knowledge.reviewSuggestionAria": "Review suggestion",
  "knowledge.approveWillFail": "This can't be added to the wiki yet: {reason}.",
  "knowledge.createConnection": "Create connection",
  "knowledge.linking": "Linking…",

  // ---- Suggestion-resolution reasons (proposalGhosts) ----
  "knowledge.reasonEntryGone": "the entry this was filed against is gone",
  "knowledge.reasonNoName": "the suggestion names no entry",
  "knowledge.reasonAmbiguousName": "{count} entries are called “{name}” — rename one first",
  "knowledge.reasonNoEntryYet": "no entry called “{name}” yet",
  "knowledge.reasonUnreadable": "the suggestion couldn't be read",
  "knowledge.reasonGhostOnly": "“{name}” is only a suggestion itself",
  "knowledge.reasonFilteredOut": "the entry it points at is filtered out of this view",

  // ---- NPC-knowledge lens ----
  "knowledge.lensLabel": "Show what an NPC knows",
  "knowledge.lensPending": "Checking what {name} knows…",
  "knowledge.lensError": "Couldn't read what {name} knows: {message}",
  "knowledge.lensUnlinked":
    " is not linked to a wiki entry, so nothing about your world reaches it. Link an NPC entry to it — that entry and its connections become what it knows.",
  "knowledge.lensMeter": "≈{chars} / {maxChars} chars · {facts} / {maxFacts} facts",
  "knowledge.lensTruncatedOne":
    "Truncated — 1 entry did not fit and is dropped from every prompt. Shorten entries, or narrow this NPC's connections.",
  "knowledge.lensTruncatedMany":
    "Truncated — {n} entries did not fit and are dropped from every prompt. Shorten entries, or narrow this NPC's connections.",
  "knowledge.lensClipped":
    "This entry has more connections than {name} can take in at once ({max}). The rest never reach {name} at all — prune its connections rather than its text.",
  "knowledge.lensNothing": "Nothing to say — this entry has no content and no public connections.",
  "knowledge.lensFactsSummary": "What {name} will be told",

  // ---- Checkup (world health) ----
  "knowledge.healthAria": "World checkup",
  "knowledge.healthRosterError":
    "Could not check the cast: {message} — the entry checks below are still complete.",
  "knowledge.healthClear":
    "Nothing to flag — every entry is connected, and every voiced NPC has something to say.",
  "knowledge.healthOrphansTitle": "Entries with no connections",
  "knowledge.healthOrphansWhy":
    "Nothing links to them, so your NPCs can never bring them up — they only reach the table if an NPC is linked to them directly.",
  "knowledge.healthEmptyVoicedTitle": "Voiced NPCs with nothing to say",
  "knowledge.healthEmptyVoicedWhy":
    "These have a voice, but their entry is empty — so they will improvise instead of speaking to your world.",
  "knowledge.healthEmptyVoicedDetail": "entry has no facts and no content",
  "knowledge.healthUnvoicedTitle": "NPC entries with no voice",
  "knowledge.healthUnvoicedWhy":
    "Written up but not voiced by anyone. Fine if deliberate — a reminder if not.",
  "knowledge.healthThreadsTitle": "Plot threads nobody is in",
  "knowledge.healthThreadsWhy":
    "No entry takes part in them, so they never come up through your NPCs.",
  "knowledge.healthCastTitle": "Cast with no wiki entry",
  "knowledge.healthCastWhy":
    "These NPCs have no wiki entry at all, so they know nothing about your world.",
  "knowledge.healthCastDetail": "link a wiki entry to this NPC",
  "knowledge.duplicatesTitle": "Probable duplicates",
  "knowledge.duplicatesWhy":
    "Compares every entry's text with every other. It only points — nothing is ever merged automatically.",
  "knowledge.duplicatesRun": "Check for duplicates",
  "knowledge.duplicatesRunning": "Comparing…",
  "knowledge.duplicatesError": "Couldn't check: {message}",
  "knowledge.duplicatesNone": "No entries look like duplicates of each other.",
  "knowledge.duplicatesAlike": "{pct}% alike",
  "knowledge.duplicatesUnembeddedOne":
    "1 entry is still being indexed and could not be compared yet.",
  "knowledge.duplicatesUnembeddedMany":
    "{n} entries are still being indexed and could not be compared yet.",
} as const;

export const de: Record<keyof typeof en, string> = {
  "knowledge.typeCharacter": "Charakter",
  "knowledge.typeNpc": "NPC",
  "knowledge.typeLocation": "Ort",
  "knowledge.typeFaction": "Fraktion",
  "knowledge.typeItem": "Gegenstand",
  "knowledge.typePlotThread": "Handlungsstrang",
  "knowledge.typeNote": "Notiz",

  "knowledge.edgeLivesIn": "wohnt in",
  "knowledge.edgeMemberOf": "Mitglied von",
  "knowledge.edgeOwns": "besitzt",
  "knowledge.edgeKnows": "kennt",
  "knowledge.edgeEnemyOf": "Feind von",
  "knowledge.edgeAllyOf": "Verbündeter von",
  "knowledge.edgeParentOf": "Elternteil von",
  "knowledge.edgeTookPartIn": "beteiligt an",
  "knowledge.edgeMentionedIn": "erwähnt in",

  "knowledge.dispositionHostile": "feindselig",
  "knowledge.dispositionWary": "misstrauisch",
  "knowledge.dispositionNeutral": "neutral",
  "knowledge.dispositionWarm": "wohlwollend",
  "knowledge.dispositionDevoted": "ergeben",

  "knowledge.loadEntriesError": "Einträge konnten nicht geladen werden: {message}",
  "knowledge.viewModeAria": "Ansicht",
  "knowledge.viewList": "Liste",
  "knowledge.viewMap": "Beziehungskarte",
  "knowledge.viewCheckup": "Checkup",
  "knowledge.filterByTagAria": "Nach Tag filtern",
  "knowledge.loadMapError": "Die Beziehungskarte konnte nicht geladen werden: {message}",
  "knowledge.searchAria": "Einträge durchsuchen",
  "knowledge.searchPlaceholder": "Das Welt-Wiki durchsuchen — Namen und Inhalte",
  "knowledge.searchError": "Suche fehlgeschlagen: {message}",
  "knowledge.noMatches": "Keine Einträge passen zu „{query}“.",
  "knowledge.emptyList":
    "Noch keine Einträge. Halte fest, was deine Welt weiß — deine NPCs erzählen dann davon.",

  "knowledge.draftOpen": "Einträge mit KI entwerfen…",
  "knowledge.draftTitle": "Einträge entwerfen",
  "knowledge.draftCloseAria": "Generator schließen",
  "knowledge.draftPromptLabel": "Was soll entworfen werden?",
  "knowledge.draftPromptPlaceholder":
    "z. B. Die Diebesgilde von Saltmarsh — ihre Anführerin, ihr Versteck, ihr Schatz und ihre rivalisierende Fraktion",
  "knowledge.draftPromptHint":
    "Dein KI-Modell entwirft daraus verknüpfte Einträge. Gespeichert wird erst, wenn du den Entwurf geprüft und übernommen hast.",
  "knowledge.draftGenerate": "Entwurf erstellen",
  "knowledge.draftGenerating": "Entwerfe…",
  "knowledge.draftGenerateError": "Entwurf fehlgeschlagen: {message}",
  "knowledge.draftReviewHint":
    "Ein Entwurf — noch ist nichts gespeichert. Entferne, was nicht passt, und übernimm den Rest.",
  "knowledge.draftRemoveEntryAria": "{name} aus dem Entwurf entfernen",
  "knowledge.draftRemoveConnectionAria": "Diese Verbindung aus dem Entwurf entfernen",
  "knowledge.draftApplyOne": "1 Eintrag hinzufügen",
  "knowledge.draftApplyMany": "{n} Einträge hinzufügen",
  "knowledge.draftApplyConnectionsOne": " + 1 Verbindung",
  "knowledge.draftApplyConnectionsMany": " + {n} Verbindungen",
  "knowledge.draftApplying": "Füge hinzu…",
  "knowledge.draftDiscard": "Entwurf verwerfen",
  "knowledge.draftApplyError": "Übernehmen fehlgeschlagen: {message}",
  "knowledge.draftAddedOne": "1 Eintrag hinzugefügt",
  "knowledge.draftAddedMany": "{n} Einträge hinzugefügt",
  "knowledge.draftAddedConnectionsOne": " und 1 Verbindung",
  "knowledge.draftAddedConnectionsMany": " und {n} Verbindungen",

  "knowledge.deleteCounting": "Zähle seine Verbindungen…",
  "knowledge.deleteCountFailed":
    "Seine Verbindungen konnten nicht gezählt werden — falls es welche hat, werden sie mit gelöscht.",
  "knowledge.deleteCascadeBefore": "Dabei werden auch seine ",
  "knowledge.deleteCascadeAfter": " gelöscht.",
  "knowledge.connectionCountOne": "1 Verbindung",
  "knowledge.connectionCountMany": "{n} Verbindungen",
  "knowledge.deleteEntryTitle": "„{name}“ löschen?",
  "knowledge.deleteEntryBody":
    "Dieser Eintrag wird dauerhaft gelöscht. Das lässt sich nicht rückgängig machen.",
  "knowledge.deleteEntry": "Eintrag löschen",

  "knowledge.gmOnlyBadge": "Nur für GM",
  "knowledge.voicedBadge": "Mit Stimme",
  "knowledge.editEntryAria": "{name} bearbeiten",
  "knowledge.deleteNamedEntryAria": "{name} löschen",

  "knowledge.factsLabel": "Fakten",
  "knowledge.factsHint":
    "Jeder Fakt lässt sich einzeln verbergen — ein Eintrag kann öffentlich sein und trotzdem ein Geheimnis behalten.",
  "knowledge.factKeyAria": "Bezeichnung von Fakt {n}",
  "knowledge.factKeyPlaceholder": "Rolle",
  "knowledge.factTextAria": "Text von Fakt {n}",
  "knowledge.factTextPlaceholder": "Führt den Rostigen Anker",
  "knowledge.factMoveUpAria": "Fakt {n} nach oben schieben",
  "knowledge.factMoveDownAria": "Fakt {n} nach unten schieben",
  "knowledge.factMakePublicAria": "Fakt {n} öffentlich machen",
  "knowledge.factMakeGmOnlyAria": "Fakt {n} nur für GM machen",
  "knowledge.factRemoveAria": "Fakt {n} entfernen",
  "knowledge.addFact": "Fakt hinzufügen",

  "knowledge.editEntryTitle": "Eintrag bearbeiten",
  "knowledge.addEntryTitle": "Eintrag hinzufügen",
  "knowledge.typeLabel": "Typ",
  "knowledge.nameLabel": "Name",
  "knowledge.namePlaceholder": "Wie heißt es?",
  "knowledge.contentLabel": "Inhalt",
  "knowledge.contentHint":
    "Was die Welt weiß. Deine NPCs greifen auf öffentliche Einträge zurück.",
  "knowledge.gmOnlySwitch": "Nur für GM — für deine NPCs unsichtbar",
  "knowledge.gmOnlyHint":
    "Nur-für-GM-Einträge bleiben für dich durchsuchbar und erreichen nie den Spieltisch.",
  "knowledge.saveEntry": "Eintrag speichern",
  "knowledge.deleteError": "Löschen fehlgeschlagen: {message}",

  "knowledge.voicedByLabel": "Gesprochen von",
  "knowledge.noneOption": "— Niemand —",
  "knowledge.voicedByHint":
    "Optional. Verknüpft diesen Eintrag mit einem NPC aus dem Ensemble — er kennt dann seinen eigenen Eintrag und dessen Verbindungen.",
  "knowledge.linkAgentError": "NPC konnte nicht verknüpft werden: {message}",
  "knowledge.connectionsHeading": "Verbindungen · {n}",
  "knowledge.addConnection": "Verbindung hinzufügen",
  "knowledge.connectionLabel": "Verbindung",
  "knowledge.connectionPlaceholder": "Verbindung…",
  "knowledge.targetEntryLabel": "Ziel-Eintrag",
  "knowledge.targetEntryPlaceholder": "Welcher Eintrag?",
  "knowledge.connectionTypedHint":
    "Verbindungen sind typisiert: „wohnt in“ zeigt auf einen Ort, „Mitglied von“ auf eine Fraktion.",
  "knowledge.add": "Hinzufügen",
  "knowledge.addConnectionError": "Hinzufügen fehlgeschlagen: {message}",
  "knowledge.outgoingConnectionsAria": "Ausgehende Verbindungen",
  "knowledge.incomingConnectionsAria": "Eingehende Verbindungen",
  "knowledge.noOutgoingConnections": "Noch keine ausgehenden Verbindungen.",
  "knowledge.deleteConnectionError": "Verbindung konnte nicht gelöscht werden: {message}",
  "knowledge.oneWayHint":
    "Verbindungen sind einseitig. Eingehende dienen nur der Orientierung und werden am anderen Eintrag bearbeitet.",
  "knowledge.deleteConnectionTitle": "Diese Verbindung löschen?",
  "knowledge.deleteConnectionBefore": "Entfernt die Verbindung ",
  "knowledge.deleteConnectionAfter": ". Das lässt sich nicht rückgängig machen.",
  "knowledge.deleteConnection": "Verbindung löschen",
  "knowledge.editConnectionAria": "Beschreiben, was hinter {relation} {target} steckt",
  "knowledge.deleteConnectionAria": "Verbindung {relation} {target} löschen",
  "knowledge.connectionNoteLabel": "Was dahintersteckt",
  "knowledge.connectionNotePlaceholder": "schuldet dir seit der Belagerung Geld",
  "knowledge.dispositionSelectLabel": "Haltung",
  "knowledge.incomingTag": "eingehend",

  "knowledge.tagsLabel": "Tags",
  "knowledge.tagsHint":
    "Deine eigenen Labels zum Wiederfinden. Deine NPCs bekommen sie nie zu sehen.",
  "knowledge.removeTagAria": "Tag {tag} entfernen",
  "knowledge.addTagAria": "Tag hinzufügen",
  "knowledge.tagsPlaceholder": "Seefahrt, zweiter Akt, braucht eine Stimme…",
  "knowledge.saveTagsError": "Tags konnten nicht gespeichert werden: {message}",

  "knowledge.mentionedLabel": "Am Spieltisch erwähnt",
  "knowledge.mentionedEmpty": "Noch nicht erwähnt. Sessions werden ausgewertet, sobald sie enden.",
  "knowledge.mentionAria": "{who} ({when}): {text}",
  "knowledge.unknownTime": "Zeitpunkt unbekannt",

  "knowledge.loadSuggestionsError": "Vorschläge konnten nicht geladen werden: {message}",
  "knowledge.noSuggestions":
    "Keine offenen Vorschläge. Wenn deine NPCs am Spieltisch etwas Neues erfahren, schlagen sie es hier vor.",
  "knowledge.kindFact": "Fakt",
  "knowledge.kindConnection": "Verbindung",
  "knowledge.kindNewEntry": "Neuer Eintrag",
  "knowledge.kindUnreadable": "Nicht lesbar",
  "knowledge.proposalNewNode": "Neuer Eintrag ({type}):",
  "knowledge.unreadableSuggestion": "Vorschlag nicht lesbar",
  "knowledge.showSimilar": "Ähnliche Einträge anzeigen",
  "knowledge.similarTitle": "Ähnliche vorhandene Einträge",
  "knowledge.similarEmpty": "Keine ähnlichen Einträge.",
  "knowledge.addToWiki": "Ins Wiki übernehmen",
  "knowledge.dismiss": "Verwerfen",
  "knowledge.dismissTitle": "Diesen Vorschlag verwerfen?",
  "knowledge.dismissBody":
    "Der Vorschlag wird verworfen und landet nicht in deinem Wiki. Das lässt sich nicht rückgängig machen.",
  "knowledge.dismissConfirm": "Vorschlag verwerfen",
  "knowledge.addToWikiError": "Übernehmen fehlgeschlagen: {message}",
  "knowledge.dismissError": "Verwerfen fehlgeschlagen: {message}",

  "knowledge.filterByTypeAria": "Nach Typ filtern",
  "knowledge.filterByConnectionAria": "Nach Verbindung filtern",
  "knowledge.tableView": "Spieler-Ansicht",
  "knowledge.suggestionsToggle": "Vorschläge ({n})",
  "knowledge.zoomInAria": "Hineinzoomen",
  "knowledge.zoomOutAria": "Herauszoomen",
  "knowledge.fit": "Einpassen",
  "knowledge.rearrange": "Neu anordnen",
  "knowledge.focusedOn": "Fokus auf {name}",
  "knowledge.depth": "Tiefe {n}",
  "knowledge.clearFocus": "Fokus aufheben",
  "knowledge.mapEmptySuggestionsHidden":
    "Hier gibt es nichts zu zeichnen. In der Spieler-Ansicht sind Vorschläge ausgeblendet.",
  "knowledge.mapEmptyFiltered":
    "Nichts zu zeichnen — alle Einträge sind ausgefiltert. Schalte einen Typ-Filter wieder ein.",
  "knowledge.mapAria": "Beziehungskarte: {n} Einträge, {m} Verbindungen",
  "knowledge.ariaGmOnly": ", nur für GM",
  "knowledge.ariaHiddenFrom": ", vor {agent} verborgen",
  "knowledge.ariaDropped": ", fehlt im Prompt von {agent} — über dem Limit",
  "knowledge.tooltipHiddenFrom": "Vor {agent} verborgen — dieser Eintrag ist nur für GM",
  "knowledge.tooltipDropped": "Fehlt im Prompt von {agent} — der Faktenblock ist voll",
  "knowledge.suggestedConnectionAria": "Vorgeschlagene Verbindung von {agent} — prüfen",
  "knowledge.suggestedFactAria": "Vorgeschlagener Fakt von {agent} — prüfen",
  "knowledge.suggestedFactTitle": "{agent} schlägt hier einen Fakt vor",
  "knowledge.suggestedEntryAria":
    "Vorgeschlagener neuer Eintrag {name} ({type}) von {agent} — prüfen",
  "knowledge.suggestedEntryTitle": "{agent} schlägt vor, {name} anzulegen",
  "knowledge.unplacedOne": "1 Vorschlag lässt sich hier nicht einzeichnen",
  "knowledge.unplacedMany": "{n} Vorschläge lassen sich hier nicht einzeichnen",
  "knowledge.reviewSuggestionAria": "Vorschlag prüfen",
  "knowledge.approveWillFail": "Das kann noch nicht ins Wiki übernommen werden: {reason}.",
  "knowledge.createConnection": "Verbindung erstellen",
  "knowledge.linking": "Verbinde…",

  "knowledge.reasonEntryGone": "der Eintrag, zu dem das eingereicht wurde, existiert nicht mehr",
  "knowledge.reasonNoName": "der Vorschlag nennt keinen Eintrag",
  "knowledge.reasonAmbiguousName": "{count} Einträge heißen „{name}“ — benenne zuerst einen um",
  "knowledge.reasonNoEntryYet": "es gibt noch keinen Eintrag namens „{name}“",
  "knowledge.reasonUnreadable": "der Vorschlag ließ sich nicht lesen",
  "knowledge.reasonGhostOnly": "„{name}“ ist selbst nur ein Vorschlag",
  "knowledge.reasonFilteredOut":
    "der Eintrag, auf den es zeigt, ist in dieser Ansicht ausgefiltert",

  "knowledge.lensLabel": "Zeigen, was ein NPC weiß",
  "knowledge.lensPending": "Prüfe, was {name} weiß…",
  "knowledge.lensError": "Konnte nicht lesen, was {name} weiß: {message}",
  "knowledge.lensUnlinked":
    " ist mit keinem Wiki-Eintrag verknüpft — er weiß deshalb nichts über deine Welt. Verknüpfe einen NPC-Eintrag mit ihm: Dieser Eintrag und seine Verbindungen werden dann sein Wissen.",
  "knowledge.lensMeter": "≈{chars} / {maxChars} Zeichen · {facts} / {maxFacts} Fakten",
  "knowledge.lensTruncatedOne":
    "Gekürzt — 1 Eintrag hat nicht mehr hineingepasst und fehlt in jedem Prompt. Kürze Einträge oder reduziere die Verbindungen dieses NPCs.",
  "knowledge.lensTruncatedMany":
    "Gekürzt — {n} Einträge haben nicht mehr hineingepasst und fehlen in jedem Prompt. Kürze Einträge oder reduziere die Verbindungen dieses NPCs.",
  "knowledge.lensClipped":
    "Dieser Eintrag hat mehr Verbindungen, als {name} auf einmal aufnehmen kann ({max}). Der Rest erreicht {name} gar nicht — reduziere die Verbindungen statt des Textes.",
  "knowledge.lensNothing":
    "Nichts zu erzählen — dieser Eintrag hat keinen Inhalt und keine öffentlichen Verbindungen.",
  "knowledge.lensFactsSummary": "Was {name} gesagt bekommt",

  "knowledge.healthAria": "Welt-Check",
  "knowledge.healthRosterError":
    "Das Ensemble konnte nicht geprüft werden: {message} — die Eintrags-Checks darunter sind trotzdem vollständig.",
  "knowledge.healthClear":
    "Nichts zu beanstanden — jeder Eintrag ist verbunden, und jeder NPC mit Stimme hat etwas zu erzählen.",
  "knowledge.healthOrphansTitle": "Einträge ohne Verbindungen",
  "knowledge.healthOrphansWhy":
    "Nichts verweist auf sie, deshalb können deine NPCs sie nie zur Sprache bringen — an den Spieltisch kommen sie nur, wenn ein NPC direkt mit ihnen verknüpft ist.",
  "knowledge.healthEmptyVoicedTitle": "NPCs mit Stimme und ohne Stoff",
  "knowledge.healthEmptyVoicedWhy":
    "Sie haben eine Stimme, aber ihr Eintrag ist leer — also improvisieren sie, statt aus deiner Welt zu erzählen.",
  "knowledge.healthEmptyVoicedDetail": "Eintrag hat weder Fakten noch Inhalt",
  "knowledge.healthUnvoicedTitle": "NPC-Einträge ohne Stimme",
  "knowledge.healthUnvoicedWhy":
    "Ausgearbeitet, aber niemand spricht sie. In Ordnung, wenn es Absicht ist — sonst eine Erinnerung.",
  "knowledge.healthThreadsTitle": "Handlungsstränge ohne Beteiligte",
  "knowledge.healthThreadsWhy":
    "Kein Eintrag ist an ihnen beteiligt, deshalb kommen sie über deine NPCs nie ins Spiel.",
  "knowledge.healthCastTitle": "Ensemble ohne Wiki-Eintrag",
  "knowledge.healthCastWhy":
    "Diese NPCs haben gar keinen Wiki-Eintrag und wissen deshalb nichts über deine Welt.",
  "knowledge.healthCastDetail": "verknüpfe einen Wiki-Eintrag mit diesem NPC",
  "knowledge.duplicatesTitle": "Wahrscheinliche Duplikate",
  "knowledge.duplicatesWhy":
    "Vergleicht den Text jedes Eintrags mit jedem anderen. Nur ein Hinweis — zusammengeführt wird nie automatisch.",
  "knowledge.duplicatesRun": "Auf Duplikate prüfen",
  "knowledge.duplicatesRunning": "Vergleiche…",
  "knowledge.duplicatesError": "Prüfung fehlgeschlagen: {message}",
  "knowledge.duplicatesNone": "Keine Einträge sehen nach Duplikaten aus.",
  "knowledge.duplicatesAlike": "{pct} % ähnlich",
  "knowledge.duplicatesUnembeddedOne":
    "1 Eintrag wird noch ausgewertet und konnte noch nicht verglichen werden.",
  "knowledge.duplicatesUnembeddedMany":
    "{n} Einträge werden noch ausgewertet und konnten noch nicht verglichen werden.",
};
