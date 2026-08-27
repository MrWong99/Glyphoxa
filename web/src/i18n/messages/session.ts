// Session-screen strings (live voice session, transcript, highlights, voice
// panel). `en` is the key source of truth; `de` must cover every key
// (compile-checked via Record<keyof typeof en, string>).

export const en = {
  // --- Header + session control card ---------------------------------------
  "session.title": "Voice session",
  "session.statusLive": "Live",
  "session.statusIdle": "Idle",
  "session.statusFailed": "Failed",
  "session.connecting": "Connecting…",
  "session.connected": "Connected",
  "session.startSession": "Start session",
  "session.stopSession": "Stop session",
  "session.voiceChannel": "Voice channel",
  "session.voiceChannelPlaceholder": "Voice channel…",
  "session.setAsDefault": "Set as default",
  "session.noVoiceChannels": "The linked Discord server has no voice channels.",
  // Localized doubles of the channel lister's two known preconditions
  // (internal/rpc/session_channels.go) — matched on the raw server message,
  // any other message still renders verbatim.
  "session.hintLinkServerFirst": "Link a Discord server first.",
  "session.hintSaveBotTokenFirst": "Save a Discord Bot token first.",
  "session.couldntStart": "Couldn't start the session: {message}",
  "session.couldntStop": "Couldn't stop the session: {message}",
  "session.couldntSaveDefaultChannel": "Couldn't save the default channel: {message}",
  "session.couldntSetMute": "Couldn't change the mute: {message}",
  "session.connectionFailedReason": "Voice connection failed: {reason}",
  "session.connectionFailed": "Voice connection failed.",

  // --- Spending limits (spend caps, #130) ----------------------------------
  "session.spendCapHard": "Hard spending limit reached — the session is ending.",
  "session.spendCapSoft":
    "Soft spending limit reached — no new AI replies will start (replies in progress will finish).",
  "session.spendEstimate": "Estimated spend {usd}.",

  // --- Last-session summary + recap ----------------------------------------
  "session.lastSummary": "Last session ended {when} · {duration} · {n} lines transcribed.",
  "session.lastSummaryOne": "Last session ended {when} · {duration} · 1 line transcribed.",
  "session.durationHm": "{h}h {m}m",
  "session.recap": "Recap",
  "session.generatingRecap": "Generating recap…",
  "session.recapLabel": "Recap · session of {stamp}",
  "session.couldntGenerateRecap": "Couldn't generate the recap: {message}",

  // --- Transcript card + past-session picker -------------------------------
  "session.liveTranscript": "Live transcript",
  "session.sessionTranscript": "Session transcript",
  "session.transcriptLoadFailedPastToast":
    "Couldn't load that session's transcript. It may be unavailable — try again.",
  "session.transcriptLoadFailedPastInline":
    "Couldn't load this session's transcript. It may be unavailable — pick another session or try again.",
  "session.transcriptLoadFailedCurrent":
    "Couldn't load the session transcript. It may be unavailable — reload to try again.",
  "session.listening": "Listening… transcript lines will appear here.",
  "session.noTranscriptLines": "This session has no transcript lines.",
  "session.startToCapture": "Start a session to capture the table's voice transcript.",
  "session.pickerLabel": "Sessions",
  "session.pickerLive": "live",
  "session.pickerLines": "{n} lines",
  "session.pickerLinesOne": "1 line",
  "session.backToCurrent": "Back to current session",

  // --- Transcript search -----------------------------------------------------
  "session.searchTranscript": "Search the transcript",
  "session.searchPlaceholder": "Search the transcript — speakers and text",
  "session.couldntSearch": "Couldn't search the transcript: {message}",
  "session.noLinesMatch": "No lines match “{query}”.",

  // --- Section titles --------------------------------------------------------
  "session.highlightsTitle": "Highlights",
  "session.boardTitle": "Tonight's board",

  // --- Voice control panel ---------------------------------------------------
  "session.voiceControl": "Voice control",
  "session.npcVoices": "NPC voices",
  "session.voicingCount": "{voicing} of {total} speaking",
  "session.voicingCountOne": "{voicing} of {total} speaking",
  "session.muteAll": "Mute all",
  "session.unmuteAll": "Unmute all",
  "session.butlerState": "Butler · answers when called by name",
  "session.stateMuted": "Muted",
  "session.stateSpeaking": "Speaking",
  "session.stateIdle": "Ready",
  "session.muteAgent": "Mute {name}",
  "session.unmuteAgent": "Unmute {name}",
  "session.mutedHint":
    "Muted NPCs stay in the scene but won't speak aloud. Unmute any voice mid-session.",

  // --- Highlights strip ------------------------------------------------------
  "session.couldntLoadHighlights": "Couldn't load the highlights: {message}",
  "session.couldntRefreshHighlights":
    "Couldn't refresh the highlights — showing the last loaded set.",
  "session.noHighlights":
    "No highlights yet — epic moments appear here once highlight recording is turned on and everyone has consented. To turn it on, open the campaign menu in the top bar, choose Campaign settings, and enable “Highlight recording”.",
  "session.candidateBadge": "Candidate — auto-deletes in 7 days",
  "session.promotedBadge": "Promoted",
  "session.promote": "Promote",
  "session.clipLabel": "Clip: {excerpt}",
  "session.deleteHighlightAria": "Delete highlight {range}",
  "session.deleteHighlightTitle": "Delete this highlight?",
  "session.deleteHighlightDescription":
    "This permanently deletes the highlight and its clip. This can't be undone.",
  "session.deleteHighlightConfirm": "Delete highlight",
  "session.couldntPromote": "Couldn't promote the highlight: {message}",
  "session.couldntDelete": "Couldn't delete the highlight: {message}",

  // --- Highlight sound (#312) ------------------------------------------------
  "session.addSound": "Add sound",
  "session.soundChoice": "Sound: {kind}",
  "session.soundMenuLabel": "Highlight sound",
  "session.soundHint":
    "Generate a matching sound for this highlight — a short sting to layer under the clip, or an instrumental music theme.",
  "session.soundSting": "Sting",
  "session.soundMusic": "Music",
  "session.removeSound": "Remove sound",
  "session.soundClipLabel": "Generated sound",
  "session.soundPending": "Generating a matching sound…",
  "session.soundRequested": "Generating a matching sound — it appears here shortly.",
  "session.soundRemoved": "Sound removed.",
  "session.couldntSetSound": "Couldn't update the highlight sound: {message}",

  // --- Share-highlight dialog ------------------------------------------------
  "session.share": "Share",
  "session.shareGroupLabel": "Share this highlight",
  "session.discordChannel": "Discord channel",
  "session.loadingChannels": "Loading channels…",
  "session.pickChannel": "Pick a channel…",
  "session.postToChannel": "Post to channel",
  "session.replayInVoice": "Replay in voice",
  "session.replayHint": "Start a voice session to replay.",
  "session.download": "Download",
  "session.couldntShare": "Couldn't share the highlight: {message}",
  "session.postedToDiscord": "Posted the highlight to Discord.",
  "session.replayingInVoice": "Replaying the highlight in voice.",

  // --- In-flight player bind affordance --------------------------------------
  "session.bindPlayer": "Bind a player",
  "session.character": "Character",
  "session.newCharacter": "New Character",
  "session.reassign": "Reassign",
  "session.createAndBind": "Create & bind",
  "session.boundPlayer": "Bound the player — their next line is named.",
  "session.reassignedCharacter": "Reassigned the Character — their next line is named.",
  "session.couldntBindPlayer": "Couldn't bind the player: {message}",
  "session.couldntReassignCharacter": "Couldn't reassign the Character: {message}",
  "session.couldntBind": "Couldn't bind: {message}",
  "session.couldntReassign": "Couldn't reassign: {message}",
} as const;

export const de: Record<keyof typeof en, string> = {
  "session.title": "Spielrunde",
  "session.statusLive": "Live",
  "session.statusIdle": "Inaktiv",
  "session.statusFailed": "Fehlgeschlagen",
  "session.connecting": "Verbinde…",
  "session.connected": "Verbunden",
  "session.startSession": "Session starten",
  "session.stopSession": "Session beenden",
  "session.voiceChannel": "Sprachkanal",
  "session.voiceChannelPlaceholder": "Sprachkanal…",
  "session.setAsDefault": "Als Standard festlegen",
  "session.noVoiceChannels": "Der verbundene Discord-Server hat keine Sprachkanäle.",
  "session.hintLinkServerFirst": "Verknüpfe zuerst einen Discord-Server.",
  "session.hintSaveBotTokenFirst": "Speichere zuerst einen Discord-Bot-Token.",
  "session.couldntStart": "Session konnte nicht gestartet werden: {message}",
  "session.couldntStop": "Session konnte nicht beendet werden: {message}",
  "session.couldntSaveDefaultChannel": "Standardkanal konnte nicht gespeichert werden: {message}",
  "session.couldntSetMute": "Stummschalten fehlgeschlagen: {message}",
  "session.connectionFailedReason": "Sprachverbindung fehlgeschlagen: {reason}",
  "session.connectionFailed": "Sprachverbindung fehlgeschlagen.",

  "session.spendCapHard": "Hartes Ausgabenlimit erreicht — die Session wird beendet.",
  "session.spendCapSoft":
    "Weiches Ausgabenlimit erreicht — es starten keine neuen KI-Antworten (laufende Antworten werden noch beendet).",
  "session.spendEstimate": "Geschätzte Ausgaben {usd}.",

  "session.lastSummary": "Letzte Session endete {when} · {duration} · {n} Zeilen mitgeschrieben.",
  "session.lastSummaryOne": "Letzte Session endete {when} · {duration} · 1 Zeile mitgeschrieben.",
  "session.durationHm": "{h} Std. {m} Min.",
  "session.recap": "Recap",
  "session.generatingRecap": "Recap wird erstellt…",
  "session.recapLabel": "Recap · Session vom {stamp}",
  "session.couldntGenerateRecap": "Recap konnte nicht erstellt werden: {message}",

  "session.liveTranscript": "Live-Mitschrift",
  "session.sessionTranscript": "Mitschrift der Session",
  "session.transcriptLoadFailedPastToast":
    "Die Mitschrift dieser Session konnte nicht geladen werden. Sie ist eventuell nicht verfügbar — versuch es noch einmal.",
  "session.transcriptLoadFailedPastInline":
    "Die Mitschrift dieser Session konnte nicht geladen werden. Sie ist eventuell nicht verfügbar — wähle eine andere Session oder versuch es noch einmal.",
  "session.transcriptLoadFailedCurrent":
    "Die Mitschrift der Session konnte nicht geladen werden. Sie ist eventuell nicht verfügbar — lade die Seite neu, um es noch einmal zu versuchen.",
  "session.listening": "Zuhören läuft… Zeilen der Mitschrift erscheinen hier.",
  "session.noTranscriptLines": "Diese Session hat keine Mitschrift-Zeilen.",
  "session.startToCapture": "Starte eine Session, um eine Mitschrift eurer Runde aufzuzeichnen.",
  "session.pickerLabel": "Sessions",
  "session.pickerLive": "live",
  "session.pickerLines": "{n} Zeilen",
  "session.pickerLinesOne": "1 Zeile",
  "session.backToCurrent": "Zurück zur aktuellen Session",

  "session.searchTranscript": "Mitschrift durchsuchen",
  "session.searchPlaceholder": "Mitschrift durchsuchen — Sprecher und Text",
  "session.couldntSearch": "Mitschrift konnte nicht durchsucht werden: {message}",
  "session.noLinesMatch": "Keine Zeilen passen zu „{query}“.",

  "session.highlightsTitle": "Highlights",
  "session.boardTitle": "Heutiges Board",

  "session.voiceControl": "Stimmen-Steuerung",
  "session.npcVoices": "NPC-Stimmen",
  "session.voicingCount": "{voicing} von {total} sprechen",
  "session.voicingCountOne": "{voicing} von {total} spricht",
  "session.muteAll": "Alle stummschalten",
  "session.unmuteAll": "Alle laut stellen",
  "session.butlerState": "Butler · antwortet nur auf direkte Ansprache",
  "session.stateMuted": "Stumm",
  "session.stateSpeaking": "Spricht",
  "session.stateIdle": "Bereit",
  "session.muteAgent": "{name} stummschalten",
  "session.unmuteAgent": "{name} laut stellen",
  "session.mutedHint":
    "Stummgeschaltete NPCs bleiben in der Szene, sprechen aber nicht laut. Du kannst jede Stimme mitten in der Session wieder laut stellen.",

  "session.couldntLoadHighlights": "Highlights konnten nicht geladen werden: {message}",
  "session.couldntRefreshHighlights":
    "Highlights konnten nicht aktualisiert werden — der letzte geladene Stand wird angezeigt.",
  "session.noHighlights":
    "Noch keine Highlights — epische Momente erscheinen hier, sobald die Highlight-Aufnahme eingeschaltet ist und alle zugestimmt haben. Zum Einschalten öffne das Kampagnen-Menü in der oberen Leiste, wähle Kampagnen-Einstellungen und aktiviere „Highlight-Aufnahme“.",
  "session.candidateBadge": "Kandidat — wird in 7 Tagen automatisch gelöscht",
  "session.promotedBadge": "Übernommen",
  "session.promote": "Übernehmen",
  "session.clipLabel": "Clip: {excerpt}",
  "session.deleteHighlightAria": "Highlight {range} löschen",
  "session.deleteHighlightTitle": "Dieses Highlight löschen?",
  "session.deleteHighlightDescription":
    "Das löscht das Highlight und seinen Clip dauerhaft. Das lässt sich nicht rückgängig machen.",
  "session.deleteHighlightConfirm": "Highlight löschen",
  "session.couldntPromote": "Highlight konnte nicht übernommen werden: {message}",
  "session.couldntDelete": "Highlight konnte nicht gelöscht werden: {message}",

  "session.addSound": "Sound hinzufügen",
  "session.soundChoice": "Sound: {kind}",
  "session.soundMenuLabel": "Highlight-Sound",
  "session.soundHint":
    "Erzeuge einen passenden Sound für dieses Highlight — einen kurzen Sting, der unter den Clip gelegt wird, oder ein instrumentales Musik-Thema.",
  "session.soundSting": "Sting",
  "session.soundMusic": "Musik",
  "session.removeSound": "Sound entfernen",
  "session.soundClipLabel": "Erzeugter Sound",
  "session.soundPending": "Passender Sound wird erzeugt…",
  "session.soundRequested": "Passender Sound wird erzeugt — er erscheint hier in Kürze.",
  "session.soundRemoved": "Sound entfernt.",
  "session.couldntSetSound": "Highlight-Sound konnte nicht geändert werden: {message}",

  "session.share": "Teilen",
  "session.shareGroupLabel": "Dieses Highlight teilen",
  "session.discordChannel": "Discord-Kanal",
  "session.loadingChannels": "Kanäle werden geladen…",
  "session.pickChannel": "Kanal wählen…",
  "session.postToChannel": "In Kanal posten",
  "session.replayInVoice": "Im Sprachkanal abspielen",
  "session.replayHint": "Starte eine Session, um den Clip abzuspielen.",
  "session.download": "Herunterladen",
  "session.couldntShare": "Highlight konnte nicht geteilt werden: {message}",
  "session.postedToDiscord": "Highlight auf Discord gepostet.",
  "session.replayingInVoice": "Highlight wird im Sprachkanal abgespielt.",

  "session.bindPlayer": "Spieler zuordnen",
  "session.character": "Charakter",
  "session.newCharacter": "Neuer Charakter",
  "session.reassign": "Neu zuordnen",
  "session.createAndBind": "Erstellen & zuordnen",
  "session.boundPlayer": "Spieler zugeordnet — die nächste Zeile trägt den Namen.",
  "session.reassignedCharacter": "Charakter neu zugeordnet — die nächste Zeile trägt den Namen.",
  "session.couldntBindPlayer": "Spieler konnte nicht zugeordnet werden: {message}",
  "session.couldntReassignCharacter": "Charakter konnte nicht neu zugeordnet werden: {message}",
  "session.couldntBind": "Zuordnen fehlgeschlagen: {message}",
  "session.couldntReassign": "Neu zuordnen fehlgeschlagen: {message}",
};
