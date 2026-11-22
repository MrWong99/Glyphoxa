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
  "chat.threadsEmpty": "No conversations yet. Start one and plan the next session with your Butler.",
  "chat.threadsLoadError": "Couldn't load conversations: {message}",
  "chat.threadLoadError": "Couldn't load this conversation: {message}",
  "chat.createError": "Couldn't start a conversation: {message}",
  "chat.renameAria": "Rename “{name}”",
  "chat.renameInputAria": "Conversation name",
  "chat.renameSaveAria": "Save name",
  "chat.renameCancelAria": "Cancel renaming",
  "chat.renameError": "Couldn't rename: {message}",
  "chat.deleteAria": "Delete “{name}”",
  "chat.deleteTitle": "Delete “{name}”?",
  "chat.deleteBody": "This permanently deletes the conversation and all its messages. This can't be undone.",
  "chat.deleteConfirm": "Delete conversation",
  "chat.deleteError": "Couldn't delete: {message}",

  // ── Message pane ─────────────────────────────────────────────────────────
  "chat.messagesAria": "Messages",
  "chat.emptyThread": "No messages yet. Ask about your world, past sessions, or what to prep next.",
  "chat.working": "The Butler is writing…",
  "chat.toolChip": "Used {tool}…",
  "chat.toolChipError": "{tool} failed",
  // Friendly names for the planning belt's tools (internal/planchat/tools.go);
  // an unknown wire name falls back to de-snake_cased text in the component.
  "chat.toolSearchTranscripts": "transcript search",
  "chat.toolFindNode": "wiki lookup",
  "chat.toolAppearancesOf": "appearances index",
  "chat.toolProposeKnowledge": "wiki suggestion",

  // ── Composer ─────────────────────────────────────────────────────────────
  "chat.inputAria": "Message to the Butler",
  "chat.inputPlaceholder": "Ask your Butler — recaps, loose ends, what the players know…",
  "chat.send": "Send",
  "chat.sendHint": "Enter sends · Shift+Enter starts a new line",
  // The footer under the composer: which model answers, and where to change it.
  "chat.answeringWith": "Answering with {provider} · {model}",
  "chat.changeModel": "Change",
  "chat.changeModelAria": "Change the planning chat model in Setup",

  // ── Exchange errors (mapped from the Connect error code) ─────────────────
  "chat.errAllowance":
    "This month's included usage is used up. The planning chat is paused until it resets at the start of next month.",
  "chat.errNoKey": "No AI model key is set up for the planning chat. Add one in Setup, then try again.",
  "chat.errGeneric": "That didn't go through — the Butler couldn't answer. Please try again.",

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
  "chat.configPasteKey": "Paste a {provider} API key",
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
  "chat.threadsEmpty": "Noch keine Unterhaltungen. Starte eine und plane die nächste Session mit deinem Butler.",
  "chat.threadsLoadError": "Unterhaltungen konnten nicht geladen werden: {message}",
  "chat.threadLoadError": "Diese Unterhaltung konnte nicht geladen werden: {message}",
  "chat.createError": "Unterhaltung konnte nicht gestartet werden: {message}",
  "chat.renameAria": "„{name}“ umbenennen",
  "chat.renameInputAria": "Name der Unterhaltung",
  "chat.renameSaveAria": "Namen speichern",
  "chat.renameCancelAria": "Umbenennen abbrechen",
  "chat.renameError": "Umbenennen fehlgeschlagen: {message}",
  "chat.deleteAria": "„{name}“ löschen",
  "chat.deleteTitle": "„{name}“ löschen?",
  "chat.deleteBody":
    "Das löscht die Unterhaltung und alle ihre Nachrichten endgültig. Das lässt sich nicht rückgängig machen.",
  "chat.deleteConfirm": "Unterhaltung löschen",
  "chat.deleteError": "Löschen fehlgeschlagen: {message}",

  // ── Message pane ─────────────────────────────────────────────────────────
  "chat.messagesAria": "Nachrichten",
  "chat.emptyThread":
    "Noch keine Nachrichten. Frag nach deiner Welt, vergangenen Sessions oder was du als Nächstes vorbereiten solltest.",
  "chat.working": "Der Butler schreibt…",
  "chat.toolChip": "{tool} verwendet…",
  "chat.toolChipError": "{tool} fehlgeschlagen",
  "chat.toolSearchTranscripts": "Mitschrift-Suche",
  "chat.toolFindNode": "Wiki-Suche",
  "chat.toolAppearancesOf": "Auftritts-Index",
  "chat.toolProposeKnowledge": "Wiki-Vorschlag",

  // ── Composer ─────────────────────────────────────────────────────────────
  "chat.inputAria": "Nachricht an den Butler",
  "chat.inputPlaceholder": "Frag deinen Butler — Recaps, offene Fäden, was die Spieler wissen…",
  "chat.send": "Senden",
  "chat.sendHint": "Enter sendet · Shift+Enter macht eine neue Zeile",
  "chat.answeringWith": "Antwortet mit {provider} · {model}",
  "chat.changeModel": "Ändern",
  "chat.changeModelAria": "Modell für den Planungs-Chat in der Einrichtung ändern",

  // ── Exchange errors ──────────────────────────────────────────────────────
  "chat.errAllowance":
    "Das inklusive Kontingent für diesen Monat ist aufgebraucht. Der Planungs-Chat pausiert, bis es zu Anfang des nächsten Monats zurückgesetzt wird.",
  "chat.errNoKey":
    "Für den Planungs-Chat ist kein KI-Modell-Schlüssel hinterlegt. Füge in der Einrichtung einen hinzu und versuch es dann noch einmal.",
  "chat.errGeneric": "Das hat nicht geklappt — der Butler konnte nicht antworten. Bitte versuch es noch einmal.",

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
  "chat.configPasteKey": "{provider}-API-Schlüssel einfügen",
  "chat.configKeyAria": "{provider}-Schlüssel für den Planungs-Chat",
  "chat.configKeyHint": "Wird nur für den Planungs-Chat gespeichert. Dein Sprachmodell bleibt, wie es ist.",
  "chat.configResetDropsKey": "Beim Speichern wird auch der für den Chat gespeicherte Schlüssel entfernt.",
  "chat.configUnsaved": "Noch nicht gespeichert.",
  "chat.configSave": "Chat-Modell speichern",
  "chat.configLoadError": "Das Modell für den Planungs-Chat konnte nicht geladen werden: {message}",
};
