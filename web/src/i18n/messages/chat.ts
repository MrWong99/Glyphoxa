// Butler planning chat strings (#592, ADR-0062): the Campaign screen's Planning
// tab (threads, message pane, composer, exchange errors) plus the Setup screen's
// planning chat model choice. `en` is the key source of truth; `de` must cover every key
// (compile-checked in i18n/index.tsx). "GM" and product names (Butler, Groq)
// stay untranslated; tool names are wire values and ride in as {tool}.

export const en = {
  // ── Campaign screen: tab + lede ──────────────────────────────────────────
  "chat.tabPlanning": "Planning",
  "chat.ledePlanning":
    "Plan with your Butler. It can search transcripts and your world wiki — conversations stay saved with the campaign.",

  // ── Thread list ──────────────────────────────────────────────────────────
  "chat.threadsAria": "Planning conversations",
  "chat.newThread": "New conversation",
  "chat.untitledThread": "Untitled conversation",
  "chat.threadsEmpty": "No conversations yet. Ask the Butler something and one starts.",
  "chat.threadsLoadError": "Couldn't load conversations",
  "chat.threadLoadError": "Couldn't load this conversation",
  "chat.createError": "Couldn't start a conversation: {message}",
  // Under 720px the rail is a sheet behind this button.
  "chat.threadsButton": "Conversations ({count})",
  "chat.threadsSheetTitle": "Conversations",
  "chat.threadSearchLabel": "Search conversations",
  "chat.threadSearchPlaceholder": "Search by title",
  "chat.threadSearchEmpty": "No conversation title matches “{query}”.",
  "chat.groupToday": "Today",
  "chat.groupWeek": "Last 7 days",
  "chat.groupEarlier": "Earlier",
  "chat.threadActionsAria": "Actions for “{name}”",
  "chat.renameAction": "Rename",
  "chat.deleteAction": "Delete…",
  "chat.renameInputAria": "Conversation name",
  "chat.renameSaveAria": "Save name",
  "chat.renameCancelAria": "Cancel renaming",
  "chat.renameError": "Couldn't rename: {message}",
  "chat.deleteTitle": "Delete “{name}”?",
  "chat.deleteBody": "This permanently deletes the conversation and all its messages. This can't be undone.",
  "chat.deleteConfirm": "Delete conversation",
  "chat.deleteError": "Couldn't delete: {message}",
  "chat.threadGone": "That conversation no longer exists.",

  // ── Starters: what the Butler can do, for a conversation with no messages ─
  "chat.startersTitle": "What can the Butler do?",
  "chat.startersLede":
    "It looks things up in your session transcripts and World wiki before it answers, and files wiki suggestions for you to approve. Start with one of these, or ask anything.",
  "chat.startersAria": "Starter questions",
  "chat.starterRecap": "Recap the last session",
  "chat.starterRecapTool": "Session recap",
  "chat.starterLooseEnds": "What loose ends are still open?",
  "chat.starterLooseEndsTool": "Transcript search",
  // A label ending in … fills the composer with its prefill for the GM to finish.
  "chat.starterKnow": "What do the players know about …?",
  "chat.starterKnowPrefill": "What do the players know about ",
  "chat.starterKnowTool": "Wiki lookup",
  "chat.starterLastSeen": "When did we last see …?",
  "chat.starterLastSeenPrefill": "When did we last see ",
  "chat.starterLastSeenTool": "Appearances index",
  "chat.starterHooks": "Draft three hooks for next session",
  "chat.starterHooksTool": "Your campaign so far",
  "chat.starterNote": "Note this down for the wiki: …",
  "chat.starterNotePrefill": "Note this down for the wiki: ",
  "chat.starterNoteTool": "Wiki suggestion",
  "chat.starterFills": "Fills in the message for you to finish",

  // ── Message pane ─────────────────────────────────────────────────────────
  "chat.messagesAria": "Messages",
  "chat.roleUser": "You",
  "chat.roleAssistant": "Butler",
  "chat.working": "The Butler is writing…",
  "chat.replyDone": "The Butler has replied.",
  "chat.toolsAria": "What the Butler looked up",
  "chat.toolChip": "Used {tool}",
  "chat.toolChipError": "{tool} failed",
  // Friendly names for the planning belt's tools (internal/planchat/tools.go);
  // an unknown wire name falls back to de-snake_cased text in the component.
  "chat.toolSearchTranscripts": "transcript search",
  "chat.toolFindNode": "wiki lookup",
  "chat.toolAppearancesOf": "appearances index",
  "chat.toolProposeKnowledge": "wiki suggestion",
  "chat.toolRecap": "session recap",
  "chat.suggestionFiled": "Suggestion filed",
  "chat.suggestionReview": "Review",
  "chat.suggestionReviewAria": "Review the suggestion in Suggestions",
  "chat.copy": "Copy",
  "chat.copied": "Copied",
  "chat.copyAria": "Copy this reply as Markdown",
  "chat.copyCode": "Copy code",
  "chat.copyFailed": "Couldn't copy to the clipboard.",
  "chat.jumpLatest": "Jump to latest",
  "chat.stopped": "Stopped — this reply is incomplete.",
  "chat.incomplete": "Incomplete — the reply broke off here.",
  "chat.notSent": "Not sent",
  "chat.replyingIn": "The Butler is replying in “{title}”.",
  "chat.replyingInOpen": "Open it",

  // ── Composer ─────────────────────────────────────────────────────────────
  "chat.inputAria": "Message to the Butler",
  "chat.inputPlaceholder": "Ask your Butler — recaps, loose ends, what the players know…",
  "chat.send": "Send",
  "chat.stop": "Stop",
  "chat.stopAria": "Stop the Butler's reply",
  "chat.counter": "{count} / {max}",
  "chat.tooLong": "{over} characters too long — the limit is {max}. Shorten the message to send it.",
  "chat.sendHint": "Enter sends · Shift+Enter starts a new line",
  // The footer under the composer: which model answers, and where to change it.
  "chat.answeringWith": "Answering with {provider} · {model}",
  "chat.changeModel": "Change",
  "chat.changeModelAria": "Change the planning chat model in Setup",
  // The standing notice above the composer when the chat's model has no key.
  // Shown BEFORE anything is sent, so it is not the after-a-failure wording of
  // chat.errNoKey ("…then try again").
  "chat.noKeyNotice": "The planning chat has no AI model key yet. Add one in Setup.",

  // ── Exchange errors (mapped from the Connect error code) ─────────────────
  // A localized heading per kind; under it the server's own sentence, or the
  // fallback body when the server said nothing.
  "chat.errAllowanceTitle": "Included usage used up",
  "chat.errAllowance":
    "This month's included usage is used up. The planning chat is paused until it resets at the start of next month.",
  "chat.errNoKeyTitle": "No AI model key for the planning chat",
  "chat.errNoKey": "No AI model key is set up for the planning chat. Add one in Setup, then try again.",
  "chat.errProviderTitle": "The AI provider couldn't answer",
  "chat.errGenericTitle": "The Butler couldn't answer",
  "chat.errGeneric": "That didn't go through — the Butler couldn't answer. Please try again.",
  "chat.openSetup": "Open Setup",
  "chat.retry": "Try again",

  // ── Setup screen: the planning chat model choice (ADR-0062) ──────────────
  "chat.configSlotName": "Planning chat",
  "chat.configSlotDesc":
    "The model behind the Butler's planning chat. Voice sessions keep their own model — this changes nothing at the table.",
  "chat.configProviderAria": "Planning chat provider",
  "chat.configSameAsVoice": "Same as voice ({provider} / {model})",
  "chat.configSameAsVoiceBare": "Same as voice",
  "chat.configOptionOwnKey": "{provider} · your key",
  "chat.configOptionIncluded": "{provider} · included",
  "chat.configOptionNeedsKey": "{provider} · needs a key",
  "chat.configAnswersWith": "Answers with {provider} / {model}",
  "chat.configKeyChat": "On a key saved for the chat only (ends in {last4}).",
  "chat.configKeyProvider": "On your {provider} key (ends in {last4}).",
  "chat.configKeyPlatform": "Included with your plan.",
  "chat.configKeyNone":
    "No {provider} key is available, so the planning chat can't answer yet. Add a key to use it.",
  "chat.configChainVoice": "You haven't picked a separate chat model, so it follows your voice model.",
  "chat.configChainDefault":
    "You haven't picked a chat model or a voice model, so the chat uses the platform default.",
  "chat.configSeparateKey": "Use a separate key for the chat",
  "chat.configPasteKey": "Paste your {provider} API key",
  "chat.configKeyAria": "{provider} key for the planning chat",
  "chat.configKeyHint": "Saved for the planning chat only. Your voice model stays as it is.",
  "chat.configResetDropsKey": "Saving this also removes the key saved for the chat.",
  "chat.configUnsaved": "Not saved yet.",
  "chat.configSave": "Save chat model",
  "chat.configLoadError": "Couldn't load the planning chat model: {message}",
} as const;

export const de: Record<keyof typeof en, string> = {
  // ── Campaign screen: tab + lede ──────────────────────────────────────────
  "chat.tabPlanning": "Planung",
  "chat.ledePlanning":
    "Plane mit deinem Butler. Er kann Mitschriften und dein Welt-Wiki durchsuchen — Unterhaltungen bleiben bei der Kampagne gespeichert.",

  // ── Thread list ──────────────────────────────────────────────────────────
  "chat.threadsAria": "Planungs-Unterhaltungen",
  "chat.newThread": "Neue Unterhaltung",
  "chat.untitledThread": "Unbenannte Unterhaltung",
  "chat.threadsEmpty": "Noch keine Unterhaltungen. Frag den Butler etwas, dann beginnt eine.",
  "chat.threadsLoadError": "Unterhaltungen konnten nicht geladen werden",
  "chat.threadLoadError": "Diese Unterhaltung konnte nicht geladen werden",
  "chat.createError": "Unterhaltung konnte nicht gestartet werden: {message}",
  "chat.threadsButton": "Unterhaltungen ({count})",
  "chat.threadsSheetTitle": "Unterhaltungen",
  "chat.threadSearchLabel": "Unterhaltungen durchsuchen",
  "chat.threadSearchPlaceholder": "Nach Titel suchen",
  "chat.threadSearchEmpty": "Kein Titel passt zu „{query}“.",
  "chat.groupToday": "Heute",
  "chat.groupWeek": "Letzte 7 Tage",
  "chat.groupEarlier": "Älter",
  "chat.threadActionsAria": "Aktionen für „{name}“",
  "chat.renameAction": "Umbenennen",
  "chat.deleteAction": "Löschen…",
  "chat.renameInputAria": "Name der Unterhaltung",
  "chat.renameSaveAria": "Namen speichern",
  "chat.renameCancelAria": "Umbenennen abbrechen",
  "chat.renameError": "Umbenennen fehlgeschlagen: {message}",
  "chat.deleteTitle": "„{name}“ löschen?",
  "chat.deleteBody":
    "Das löscht die Unterhaltung und alle ihre Nachrichten endgültig. Das lässt sich nicht rückgängig machen.",
  "chat.deleteConfirm": "Unterhaltung löschen",
  "chat.deleteError": "Löschen fehlgeschlagen: {message}",
  "chat.threadGone": "Diese Unterhaltung gibt es nicht mehr.",

  // ── Starters ─────────────────────────────────────────────────────────────
  "chat.startersTitle": "Was kann der Butler?",
  "chat.startersLede":
    "Er schlägt in deinen Session-Mitschriften und im Welt-Wiki nach, bevor er antwortet, und reicht Wiki-Vorschläge ein, die du freigibst. Fang mit einer dieser Fragen an oder frag einfach.",
  "chat.startersAria": "Einstiegsfragen",
  "chat.starterRecap": "Fass die letzte Session zusammen",
  "chat.starterRecapTool": "Session-Recap",
  "chat.starterLooseEnds": "Welche offenen Fäden gibt es noch?",
  "chat.starterLooseEndsTool": "Mitschrift-Suche",
  "chat.starterKnow": "Was wissen die Spieler über …?",
  "chat.starterKnowPrefill": "Was wissen die Spieler über ",
  "chat.starterKnowTool": "Wiki-Suche",
  "chat.starterLastSeen": "Wann haben wir zuletzt … gesehen?",
  "chat.starterLastSeenPrefill": "Wann haben wir zuletzt ",
  "chat.starterLastSeenTool": "Auftritts-Index",
  "chat.starterHooks": "Entwirf drei Aufhänger für die nächste Session",
  "chat.starterHooksTool": "Deine Kampagne bisher",
  "chat.starterNote": "Notier das fürs Wiki: …",
  "chat.starterNotePrefill": "Notier das fürs Wiki: ",
  "chat.starterNoteTool": "Wiki-Vorschlag",
  "chat.starterFills": "Füllt die Nachricht vor, du schreibst sie zu Ende",

  // ── Message pane ─────────────────────────────────────────────────────────
  "chat.messagesAria": "Nachrichten",
  "chat.roleUser": "Du",
  "chat.roleAssistant": "Butler",
  "chat.working": "Der Butler schreibt…",
  "chat.replyDone": "Der Butler hat geantwortet.",
  "chat.toolsAria": "Was der Butler nachgeschlagen hat",
  "chat.toolChip": "{tool} verwendet",
  "chat.toolChipError": "{tool} fehlgeschlagen",
  "chat.toolSearchTranscripts": "Mitschrift-Suche",
  "chat.toolFindNode": "Wiki-Suche",
  "chat.toolAppearancesOf": "Auftritts-Index",
  "chat.toolProposeKnowledge": "Wiki-Vorschlag",
  "chat.toolRecap": "Session-Recap",
  "chat.suggestionFiled": "Vorschlag eingereicht",
  "chat.suggestionReview": "Prüfen",
  "chat.suggestionReviewAria": "Den Vorschlag unter Vorschläge prüfen",
  "chat.copy": "Kopieren",
  "chat.copied": "Kopiert",
  "chat.copyAria": "Diese Antwort als Markdown kopieren",
  "chat.copyCode": "Code kopieren",
  "chat.copyFailed": "Kopieren in die Zwischenablage fehlgeschlagen.",
  "chat.jumpLatest": "Zum Neuesten",
  "chat.stopped": "Angehalten — diese Antwort ist unvollständig.",
  "chat.incomplete": "Unvollständig — die Antwort ist hier abgebrochen.",
  "chat.notSent": "Nicht gesendet",
  "chat.replyingIn": "Der Butler antwortet gerade in „{title}“.",
  "chat.replyingInOpen": "Öffnen",

  // ── Composer ─────────────────────────────────────────────────────────────
  "chat.inputAria": "Nachricht an den Butler",
  "chat.inputPlaceholder": "Frag deinen Butler — Recaps, offene Fäden, was die Spieler wissen…",
  "chat.send": "Senden",
  "chat.stop": "Stopp",
  "chat.stopAria": "Die Antwort des Butlers anhalten",
  "chat.counter": "{count} / {max}",
  "chat.tooLong": "{over} Zeichen zu lang — erlaubt sind {max}. Kürze die Nachricht, um sie zu senden.",
  "chat.sendHint": "Enter sendet · Shift+Enter macht eine neue Zeile",
  "chat.answeringWith": "Antwortet mit {provider} · {model}",
  "chat.changeModel": "Ändern",
  "chat.changeModelAria": "Modell für den Planungs-Chat in der Einrichtung ändern",
  "chat.noKeyNotice": "Der Planungs-Chat hat noch keinen KI-Modell-Schlüssel. Füge in der Einrichtung einen hinzu.",

  // ── Exchange errors ──────────────────────────────────────────────────────
  "chat.errAllowanceTitle": "Inklusives Kontingent aufgebraucht",
  "chat.errAllowance":
    "Das inklusive Kontingent für diesen Monat ist aufgebraucht. Der Planungs-Chat pausiert, bis es zu Anfang des nächsten Monats zurückgesetzt wird.",
  "chat.errNoKeyTitle": "Kein KI-Modell-Schlüssel für den Planungs-Chat",
  "chat.errNoKey":
    "Für den Planungs-Chat ist kein KI-Modell-Schlüssel hinterlegt. Füge in der Einrichtung einen hinzu und versuch es dann noch einmal.",
  "chat.errProviderTitle": "Der KI-Anbieter konnte nicht antworten",
  "chat.errGenericTitle": "Der Butler konnte nicht antworten",
  "chat.errGeneric": "Das hat nicht geklappt — der Butler konnte nicht antworten. Bitte versuch es noch einmal.",
  "chat.openSetup": "Einrichtung öffnen",
  "chat.retry": "Erneut versuchen",

  // ── Setup screen: the planning chat model choice ─────────────────────────
  "chat.configSlotName": "Planungs-Chat",
  "chat.configSlotDesc":
    "Das Modell hinter dem Planungs-Chat des Butlers. Voice-Sessions behalten ihr eigenes Modell — am Tisch ändert sich nichts.",
  "chat.configProviderAria": "Anbieter für den Planungs-Chat",
  "chat.configSameAsVoice": "Wie das Sprachmodell ({provider} / {model})",
  "chat.configSameAsVoiceBare": "Wie das Sprachmodell",
  "chat.configOptionOwnKey": "{provider} · dein Schlüssel",
  "chat.configOptionIncluded": "{provider} · inklusive",
  "chat.configOptionNeedsKey": "{provider} · braucht einen Schlüssel",
  "chat.configAnswersWith": "Antwortet mit {provider} / {model}",
  "chat.configKeyChat": "Mit einem nur für den Chat gespeicherten Schlüssel (endet auf {last4}).",
  "chat.configKeyProvider": "Mit deinem {provider}-Schlüssel (endet auf {last4}).",
  "chat.configKeyPlatform": "In deinem Tarif enthalten.",
  "chat.configKeyNone":
    "Es ist kein {provider}-Schlüssel verfügbar, deshalb kann der Planungs-Chat noch nicht antworten. Füge einen Schlüssel hinzu.",
  "chat.configChainVoice":
    "Du hast kein eigenes Chat-Modell gewählt, deshalb folgt der Chat deinem Sprachmodell.",
  "chat.configChainDefault":
    "Du hast weder ein Chat-Modell noch ein Sprachmodell gewählt, deshalb nutzt der Chat den Plattform-Standard.",
  "chat.configSeparateKey": "Eigenen Schlüssel für den Chat verwenden",
  "chat.configPasteKey": "Füge deinen {provider}-API-Schlüssel ein",
  "chat.configKeyAria": "{provider}-Schlüssel für den Planungs-Chat",
  "chat.configKeyHint": "Wird nur für den Planungs-Chat gespeichert. Dein Sprachmodell bleibt, wie es ist.",
  "chat.configResetDropsKey": "Beim Speichern wird auch der für den Chat gespeicherte Schlüssel entfernt.",
  "chat.configUnsaved": "Noch nicht gespeichert.",
  "chat.configSave": "Chat-Modell speichern",
  "chat.configLoadError": "Das Modell für den Planungs-Chat konnte nicht geladen werden: {message}",
};
