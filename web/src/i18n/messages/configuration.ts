// Configuration ("Setup") screen strings. `en` is the key source of truth;
// `de` must cover every key (compile-checked). Provider PRODUCT names (Groq,
// ElevenLabs, Gemini, Discord, Glyphoxa, Butler) are never translated — they
// ride in as {name}/{tag}/{guild} params or live in the component as labels.
// "Table" is the GM's word for the Tenant (CONTEXT.md); never "tenant".

export const en = {
  // Screen chrome — heading matches the sidebar nav ("Setup", shell.navSetup).
  "config.title": "Setup",
  "config.lede": "Connect Discord and the AI services that power your table.",
  "config.scopeLine": "These settings belong to your whole table: every campaign uses the same Discord server and keys.",

  // Header overline of the first-run create form (shared with Campaign and Session).
  "config.activeCampaign": "Active campaign",

  // First-run CTA (#267) — Butler is a product name, explained in plain words.
  "config.firstCampaignTitle": "Create your first campaign",
  "config.firstCampaignLede":
    "No campaign yet. Create one to get started — your AI game assistant, the Butler, is created automatically.",

  // The Setup checklist (#695): four steps in order, then optional extras.
  // The step labels double as the onboarding "Next" list (auth.onboardingNext*).
  "config.checklistTitle": "Get ready for your first session",
  "config.checklistProgress": "{done} of {total} done",
  "config.readyTitle": "Your table is ready to play",
  "config.readyLede": "Everything a voice session needs is in place.",
  "config.goToSession": "Go to Session",
  "config.optionalTitle": "Optional",
  "config.stepStatusDone": "Done",
  "config.stepStatusTodo": "Still to do",
  "config.stepStatusChecking": "Checking…",
  "config.stepStatusUnavailable": "Couldn't check",
  "config.stepUnavailableHint": "Setup couldn't read this. Retry below.",
  "config.readyPausedTitle": "Ready, but new sessions are paused",
  "config.readyPausedLede": "This month's included usage is used up. Your plan says how to go on.",
  "config.seePlan": "See your plan",
  "config.stepCampaign": "Create a campaign",
  "config.stepCampaignHint": "Your NPCs, world wiki and sessions live in a campaign.",
  "config.stepCampaignDone": "{name}",
  "config.stepDiscord": "Connect Discord",
  "config.stepDiscordHint": "Add Glyphoxa to your server and link it. You need Manage Server rights there.",
  "config.stepDiscordDone": "Linked to {server}",
  "config.stepDiscordFailedHint": "The Discord connection failed. Check the bot token under Discord.",
  "config.stepVoice": "Voice and speech",
  "config.stepVoiceHint": "ElevenLabs hears the table and speaks for your NPCs.",
  "config.stepAiModel": "AI model",
  "config.stepAiModelHint": "The language model your NPCs think and answer with.",
  "config.optionalImages": "Images",
  "config.optionalImagesHint": "Gemini draws portraits, maps and highlight pictures.",
  "config.optionalSpendingLimits": "Spending limits",
  "config.optionalSpendingLimitsHint": "Pause or end a session when its estimated cost crosses an amount.",

  // Honest loading of the key rows and the Discord card.
  "config.loadingKeys": "Loading your keys and Discord connection…",
  "config.loadErrorTitle": "Couldn't load your setup",
  "config.loadErrorDesc": "Your keys and Discord connection are not shown until they can be read.",
  "config.couldntLoadCampaignTitle": "Couldn't check your campaigns",

  // Plan and usage (GetUsageSummary). Figures are estimates (ADR-0046).
  "config.planOverline": "Plan and usage",
  "config.planNone": "No plan",
  "config.planLoading": "Loading your plan…",
  "config.planLoadError": "Couldn't load your plan",
  "config.usageMeterLabel": "Included usage this month",
  "config.usageLine": "About {used} of {included} included usage used this month",
  "config.usageResets": "· resets {date}",
  "config.usageNearlyUsed": "Most of this month's included usage is used. It starts over on {date}.",
  "config.usageExhaustedTitle": "This month's included usage is used up",
  "config.usageExhaustedLede":
    "New sessions and AI generation are paused. Saving your own key does not lift this; there are two ways on:",
  "config.usageRemedyWait": "Wait until {date}, when the allowance starts over.",
  // Split around the link to the operator's contact (the Impressum).
  "config.usageRemedyByokPre": "Or ",
  "config.usageRemedyAskOperator": "ask the operator",
  "config.usageRemedyByokPost": " to move your table to the bring-your-own-keys plan, then add your own keys below.",
  "config.usageIncludedNoAllowance": "Included services run on this server's keys; no monthly allowance applies.",
  "config.usageOwnKeys": "On your own keys — usage is billed to you by the providers.",
  "config.usageEstimateNote": "Amounts are estimates from list prices, not a bill.",

  // AI service keys (the write-only BYOK slots, ADR-0004). Slot kinds are the
  // plain-language overlines; the product name stays untranslated.
  "config.aiServiceKeysTitle": "AI services",
  "config.kindAiModel": "AI model",
  "config.kindVoiceSpeech": "Voice & speech",
  "config.kindImages": "Images",
  "config.kindBot": "Bot",
  "config.kindPlanningChat": "Planning chat model",
  "config.pasteApiKey": "Paste your {name} API key",
  "config.descGroq": "Fast language model for your NPCs' replies.",
  "config.descAnthropic": "Claude models for your NPCs' replies.",
  "config.descElevenLabs": "Hears the table and speaks for your NPCs.",
  "config.descGemini": "Draws portraits, maps and highlight pictures.",
  "config.getKey": "Get your {name} key",
  "config.voiceLlmTitle": "AI model",
  "config.otherServicesTitle": "Voice, images and planning",

  // SecretRow controls + aria. {name} is the slot's display name ("Groq",
  // "Bot token"), so the aria strings compose e.g. "Groq key" / "Groq saved".
  "config.secretKeyAria": "{name} key",
  "config.secretSavedAria": "{name} saved",
  "config.savedOn": "saved {date}",
  "config.replace": "Replace",
  "config.connectedAs": "Connected as {tag}",
  "config.includedWithPlan": "Included with your plan",
  "config.useOwnKey": "Use my own key",

  // Removing a voice LLM key (ADR-0004 amendment 2026-11-26).
  "config.removeKey": "Remove key",
  "config.removeKeyTitle": "Remove your {name} key?",
  "config.removeKeyDesc":
    "Glyphoxa forgets this key. If {name} is your voice model, sessions move to your other AI model key, or else to this server's default model.",
  "config.couldntRemove": "Couldn't remove: {message}",
  "config.toastKeyRemoved": "{name} key removed",
  "config.backToDefault": "Back to platform default",
  "config.backToDefaultTitle": "Go back to the platform default?",
  "config.backToDefaultDesc":
    "This removes your Groq and Anthropic keys for voice. Sessions then run on the model this server provides, if your plan includes it.",
  "config.toastBackToDefault": "Back on the platform default model",
  "config.toastChatModelReset": "The planning chat moved to its default model: the one it used needs your own key.",

  // Model picker (#227).
  "config.modelAria": "{name} model",
  "config.modelPlaceholder": "Model…",
  "config.modelSearchPlaceholder": "Search or type a model id…",
  "config.saveModel": "Save model",
  // The voice-LLM choice (#702). "Platform default" is what a table that
  // never picked runs; "your choice" is the tenant's own saved row.
  "config.llmInUsePlatform": "Voice model in use: {provider} / {model} (platform default)",
  "config.llmInUseOwn": "Voice model in use: {provider} / {model} (your choice)",
  "config.useForVoice": "Use for voice",
  "config.keySwitchesVoice": "Saving a key makes {name} your voice model.",
  "config.badgeInUse": "In use",

  // Health badges (#70) — short and friendly, never internal status words.
  // "Checking…" while the live probe is out; "Working" only once it succeeded.
  "config.badgeKeyNeeded": "Key needed",
  "config.badgeOptional": "Optional",
  // An unsaved slot the deployment provides for this tenant (trial Platform
  // Keys, the central Bot) — pasting an own key still overrides it.
  "config.badgeIncluded": "Included",
  "config.badgeChecking": "Checking…",
  "config.badgeSaved": "Saved",
  "config.badgeWorking": "Working",
  "config.badgeDegraded": "Having trouble",

  // Why a probe failed — one sentence per stable ProviderHealth.code.
  "config.healthMissingKey": "No key yet — paste one to use this service.",
  "config.healthKeyRejected": "The key was rejected — check it or paste a new one.",
  "config.healthQuotaExhausted": "Out of quota or rate-limited — check your plan, or try again later.",
  "config.healthUnreachable": "Couldn't reach the service — try again shortly.",
  "config.healthNoPlatformKeys": "Your plan doesn't include our keys — paste your own key.",
  "config.healthCheckFailed": "The check failed — try again shortly.",

  // Standing Discord integration badge (#489).
  "config.discordIntegrationFailed": "Discord connection failed",
  "config.discordIntegrationReconnecting": "Reconnecting to Discord",
  // Why it failed — one sentence per code, saying what to do on this screen (#695).
  "config.integrationReasonInvalidBotToken":
    "Discord rejected the bot token. Paste a current token under “Use your own Discord bot” below.",
  "config.integrationReasonDisallowedIntents":
    "Discord refused the bot's permissions (gateway intents). Check the bot's intents in the Discord Developer Portal.",
  "config.integrationReasonBotNotAuthorized":
    "Discord did not authorize the bot. Check the bot in the Discord Developer Portal, then paste its token again below.",
  "config.integrationReasonGatewayRejected":
    "Discord refused the connection. If it stays this way, check the bot token under “Use your own Discord bot” below.",
  "config.botConnected": "Bot connected",

  // Discord connection card, as two numbered steps (#695).
  "config.discordTitle": "Discord",
  "config.discordStep1Title": "Add Glyphoxa to your server",
  "config.discordStep2Title": "Link your server",
  "config.discordStep2Copy": "Paste an invite or channel link from your server, then link it.",
  "config.linkServer": "Link server",
  "config.linkServerNamed": "Link {server}",
  "config.alreadyLinked": "This server is already linked.",
  "config.relinkForName": "Linked before server names were kept: choose Change and link this server again to show its name.",
  "config.linkNeedsManageServer": "Linking needs Manage Server rights on that Discord server.",
  "config.couldntLinkTitle": "Couldn't link the server",
  "config.toastLinked": "Linked {server}",
  "config.enterServerId": "Enter the server ID instead",
  "config.linkedServer": "Linked server",
  "config.serverIdValue": "Server ID {id}",
  "config.changeServer": "Change",
  "config.ownBotTitle": "Use your own Discord bot",
  "config.ownBotHint": "Only if you run your own bot",
  "config.botTokenName": "Bot token",
  "config.botGetKey": "Create a bot in the Discord Developer Portal",
  "config.pasteBotToken": "Paste the Discord bot token",
  "config.botTokenHint":
    "Only needed if you run your own Discord bot. Otherwise leave it empty: Glyphoxa's bot joins through step 1.",
  "config.serverIdLabel": "Server ID",
  "config.serverIdPlaceholder": "e.g. 472093001100",
  "config.serverIdHint":
    "The Discord server your group plays on. Sessions pick their voice channel on the Session screen.",

  // Unlink (#504) — "table", never "tenant", in user-facing copy.
  "config.unlinkServer": "Unlink server",
  "config.unlinkTitle": "Unlink {server}?",
  "config.unlinkNote":
    "Sessions stop working until a server is linked again, and another table can then link this one.",
  "config.couldntUnlink": "Couldn't unlink: {message}",
  "config.toastUnlinked": "Unlinked {server}",

  // "Paste a Discord link" autofill (#101/#105).
  "config.linkPasteLabel": "Paste a Discord link",
  "config.linkPastePlaceholder": "https://discord.com/channels/… or discord.gg/…",
  "config.linkPasteHint": "An invite or channel link from the server your group plays on.",
  "config.linkUnreadable": "Couldn't read that link — paste a Discord channel or invite link.",
  "config.resolvingInvite": "Checking the invite…",
  "config.inviteFound": "Found {guild}.",
  "config.inviteInvalid": "That invite looks invalid or expired.",
  "config.inviteFailed": "Couldn't check that invite. Please try again.",
  // Appended ONLY to the not-a-member precondition (ADR-0047); "step 1" must
  // keep matching where the Add-Glyphoxa action renders.
  "config.addBotHint": "Use “Add Glyphoxa to your server” in step 1, then paste the invite again.",

  // Add-Glyphoxa bot authorization (#110).
  "config.addBotCopy":
    "Glyphoxa's bot has to be a member of your server before a voice session can join. Discord asks which server to add it to.",
  "config.addBotAction": "Add Glyphoxa to your server",
  "config.addBotNoAppId":
    "No Discord application id is configured (DISCORD_OAUTH_CLIENT_ID), so this link can't be built.",

  // Spending limits (spend caps, #130 / ADR-0046), labelled by outcome.
  "config.spendingLimitsTitle": "Spending limits",
  "config.spendingLimitsHint": "Optional cost guardrails per voice session.",
  "config.spendCapsLede":
    "Figures are estimates, not billed amounts. Leave a field blank to turn that limit off; changes apply to the next session.",
  "config.softLimitLabel": "Pause new AI replies at (USD)",
  "config.softLimitPlaceholder": "e.g. 5.00",
  "config.softLimitHint": "Replies in progress finish; transcription carries on.",
  "config.hardLimitLabel": "End the session at (USD)",
  "config.hardLimitPlaceholder": "e.g. 10.00",
  "config.hardLimitHint": "Must be at least the pause amount.",
  "config.capInvalid": "Enter an amount of 0 or more.",
  "config.capHardBelowSoft": "Must be at least the amount where replies pause.",
  "config.capsBoth": "Pause at {pause} · end at {end}",
  "config.capsPauseOnly": "Pause at {pause}",
  "config.capsEndOnly": "End at {end}",
  "config.capsNone": "No limits set",
  "config.saveSpendingLimits": "Save spending limits",
  "config.couldntSaveLimits": "Couldn't save the limits",
  "config.toastLimitsSaved": "Spending limits saved: {limits}",
  "config.spendCapsLoadError": "Couldn't load the stored limits: {message}",

  // "Your table" (RenameTenant; deletion goes through the operator).
  "config.tableOverline": "Your table",
  "config.tableTitle": "Name and removal",
  "config.tableNameLabel": "Table name",
  "config.tableNameHint": "Shown in the sidebar and to players you invite.",
  "config.renameTable": "Rename table",
  "config.couldntRenameTable": "Couldn't rename the table",
  "config.toastTableRenamed": "Table renamed to {name}",
  // Split around the link to the operator.
  "config.tableDeletePre": "To delete your table and everything in it, ",
  "config.tableDeleteLink": "write to the operator",
  "config.tableDeletePost": ". It can't be undone.",
  "config.tableDeleteSubject": "Glyphoxa: delete my table",
  "config.planChangeSubject": "Glyphoxa: move my table to the bring-your-own-keys plan",
} as const;

export const de: Record<keyof typeof en, string> = {
  "config.title": "Einrichtung",
  "config.lede": "Verbinde Discord und die KI-Dienste, die deinen Spieltisch antreiben.",
  "config.scopeLine":
    "Diese Einstellungen gelten für deinen ganzen Tisch: Alle Kampagnen nutzen denselben Discord-Server und dieselben Schlüssel.",

  "config.activeCampaign": "Aktive Kampagne",

  "config.firstCampaignTitle": "Erstelle deine erste Kampagne",
  "config.firstCampaignLede":
    "Noch keine Kampagne. Erstelle eine, um loszulegen — dein KI-Spielassistent, der Butler, wird automatisch mit angelegt.",

  "config.checklistTitle": "Bereit für deine erste Session",
  "config.checklistProgress": "{done} von {total} erledigt",
  "config.readyTitle": "Dein Tisch ist spielbereit",
  "config.readyLede": "Alles, was eine Sprach-Session braucht, ist eingerichtet.",
  "config.goToSession": "Zur Spielrunde",
  "config.optionalTitle": "Optional",
  "config.stepStatusDone": "Erledigt",
  "config.stepStatusTodo": "Noch offen",
  "config.stepStatusChecking": "Wird geprüft…",
  "config.stepStatusUnavailable": "Nicht prüfbar",
  "config.stepUnavailableHint": "Setup konnte das nicht lesen. Versuche es unten erneut.",
  "config.readyPausedTitle": "Bereit, aber neue Sessions sind pausiert",
  "config.readyPausedLede": "Das enthaltene Kontingent dieses Monats ist aufgebraucht. Dein Tarif zeigt, wie es weitergeht.",
  "config.seePlan": "Zum Tarif",
  "config.stepCampaign": "Kampagne erstellen",
  "config.stepCampaignHint": "Deine NSCs, das Welt-Wiki und die Sessions gehören zu einer Kampagne.",
  "config.stepCampaignDone": "{name}",
  "config.stepDiscord": "Discord verbinden",
  "config.stepDiscordHint":
    "Füge Glyphoxa zu deinem Server hinzu und verknüpfe ihn. Dafür brauchst du dort das Recht „Server verwalten“.",
  "config.stepDiscordDone": "Verknüpft mit {server}",
  "config.stepDiscordFailedHint": "Die Discord-Verbindung ist fehlgeschlagen. Prüfe den Bot-Token unter Discord.",
  "config.stepVoice": "Stimme und Sprache",
  "config.stepVoiceHint": "ElevenLabs hört dem Tisch zu und spricht für deine NSCs.",
  "config.stepAiModel": "KI-Modell",
  "config.stepAiModelHint": "Das Sprachmodell, mit dem deine NSCs denken und antworten.",
  "config.optionalImages": "Bilder",
  "config.optionalImagesHint": "Gemini zeichnet Porträts, Karten und Bilder zu Highlights.",
  "config.optionalSpendingLimits": "Ausgabenlimits",
  "config.optionalSpendingLimitsHint":
    "Eine Session pausieren oder beenden, wenn ihre geschätzten Kosten einen Betrag überschreiten.",

  "config.loadingKeys": "Deine Schlüssel und die Discord-Verbindung werden geladen…",
  "config.loadErrorTitle": "Deine Einrichtung konnte nicht geladen werden",
  "config.loadErrorDesc":
    "Deine Schlüssel und die Discord-Verbindung werden erst angezeigt, wenn sie gelesen werden konnten.",
  "config.couldntLoadCampaignTitle": "Deine Kampagnen konnten nicht geprüft werden",

  "config.planOverline": "Tarif und Verbrauch",
  "config.planNone": "Kein Tarif",
  "config.planLoading": "Dein Tarif wird geladen…",
  "config.planLoadError": "Dein Tarif konnte nicht geladen werden",
  "config.usageMeterLabel": "Inklusivverbrauch in diesem Monat",
  "config.usageLine": "Etwa {used} von {included} Inklusivverbrauch in diesem Monat genutzt",
  "config.usageResets": "· beginnt neu am {date}",
  "config.usageNearlyUsed": "Der Großteil des Inklusivverbrauchs ist genutzt. Er beginnt am {date} neu.",
  "config.usageExhaustedTitle": "Der Inklusivverbrauch dieses Monats ist aufgebraucht",
  "config.usageExhaustedLede":
    "Neue Sessions und KI-Generierung sind pausiert. Ein eigener Schlüssel hebt das nicht auf; es gibt zwei Wege weiter:",
  "config.usageRemedyWait": "Warte bis zum {date}, dann beginnt das Kontingent neu.",
  "config.usageRemedyByokPre": "Oder ",
  "config.usageRemedyAskOperator": "bitte den Betreiber",
  "config.usageRemedyByokPost":
    ", deinen Tisch in den Tarif mit eigenen Schlüsseln zu verschieben, und trage dann unten deine Schlüssel ein.",
  "config.usageIncludedNoAllowance":
    "Die enthaltenen Dienste laufen über die Schlüssel dieses Servers; es gilt kein monatliches Kontingent.",
  "config.usageOwnKeys": "Mit deinen eigenen Schlüsseln — die Anbieter rechnen direkt mit dir ab.",
  "config.usageEstimateNote": "Die Beträge sind Schätzungen nach Listenpreisen, keine Rechnung.",

  "config.aiServiceKeysTitle": "KI-Dienste",
  "config.kindAiModel": "KI-Modell",
  "config.kindVoiceSpeech": "Stimme & Sprache",
  "config.kindImages": "Bilder",
  "config.kindBot": "Bot",
  "config.kindPlanningChat": "Modell für den Planungs-Chat",
  "config.pasteApiKey": "Füge deinen {name}-API-Schlüssel ein",
  "config.descGroq": "Schnelles Sprachmodell für die Antworten deiner NSCs.",
  "config.descAnthropic": "Claude-Modelle für die Antworten deiner NSCs.",
  "config.descElevenLabs": "Hört dem Tisch zu und spricht für deine NSCs.",
  "config.descGemini": "Zeichnet Porträts, Karten und Bilder zu Highlights.",
  "config.getKey": "{name}-Schlüssel holen",
  "config.voiceLlmTitle": "KI-Modell",
  "config.otherServicesTitle": "Stimme, Bilder und Planung",

  "config.secretKeyAria": "{name}-Schlüssel",
  "config.secretSavedAria": "{name} gespeichert",
  "config.savedOn": "gespeichert am {date}",
  "config.replace": "Ersetzen",
  "config.connectedAs": "Verbunden als {tag}",
  "config.includedWithPlan": "In deinem Tarif enthalten",
  "config.useOwnKey": "Eigenen Schlüssel verwenden",

  "config.removeKey": "Schlüssel entfernen",
  "config.removeKeyTitle": "Deinen {name}-Schlüssel entfernen?",
  "config.removeKeyDesc":
    "Glyphoxa vergisst diesen Schlüssel. Ist {name} dein Sprachmodell, laufen Sessions danach mit deinem anderen KI-Modell-Schlüssel oder sonst mit dem Standardmodell dieses Servers.",
  "config.couldntRemove": "Entfernen fehlgeschlagen: {message}",
  "config.toastKeyRemoved": "{name}-Schlüssel entfernt",
  "config.backToDefault": "Zurück zum Plattform-Standard",
  "config.backToDefaultTitle": "Zurück zum Plattform-Standard?",
  "config.backToDefaultDesc":
    "Damit werden deine Groq- und Anthropic-Schlüssel für Sprache entfernt. Sessions laufen dann mit dem Modell dieses Servers, sofern dein Tarif es enthält.",
  "config.toastBackToDefault": "Wieder beim Standardmodell der Plattform",
  "config.toastChatModelReset":
    "Der Planungs-Chat nutzt jetzt sein Standardmodell: Das bisherige braucht deinen eigenen Schlüssel.",

  "config.modelAria": "{name}-Modell",
  "config.modelPlaceholder": "Modell…",
  "config.modelSearchPlaceholder": "Modell-ID suchen oder eintippen…",
  "config.saveModel": "Modell speichern",
  "config.llmInUsePlatform": "Sprachmodell in Verwendung: {provider} / {model} (Plattform-Standard)",
  "config.llmInUseOwn": "Sprachmodell in Verwendung: {provider} / {model} (deine Auswahl)",
  "config.useForVoice": "Für Sprache verwenden",
  "config.keySwitchesVoice": "Mit dem Speichern eines Schlüssels wird {name} dein Sprachmodell.",
  "config.badgeInUse": "In Verwendung",

  "config.badgeKeyNeeded": "Schlüssel fehlt",
  "config.badgeOptional": "Optional",
  "config.badgeIncluded": "Inklusive",
  "config.badgeChecking": "Wird geprüft…",
  "config.badgeSaved": "Gespeichert",
  "config.badgeWorking": "Funktioniert",
  "config.badgeDegraded": "Hat Probleme",

  "config.healthMissingKey": "Noch kein Schlüssel — füge einen ein, um diesen Dienst zu nutzen.",
  "config.healthKeyRejected": "Der Schlüssel wurde abgelehnt — prüfe ihn oder füge einen neuen ein.",
  "config.healthQuotaExhausted":
    "Kontingent aufgebraucht oder Ratenlimit erreicht — prüfe deinen Tarif oder versuche es später erneut.",
  "config.healthUnreachable": "Der Dienst ist nicht erreichbar — versuche es gleich noch einmal.",
  "config.healthNoPlatformKeys": "Dein Tarif enthält unsere Schlüssel nicht — füge deinen eigenen ein.",
  "config.healthCheckFailed": "Die Prüfung ist fehlgeschlagen — versuche es gleich noch einmal.",

  "config.discordIntegrationFailed": "Discord-Verbindung fehlgeschlagen",
  "config.discordIntegrationReconnecting": "Verbindet neu mit Discord",
  "config.integrationReasonInvalidBotToken":
    "Discord hat den Bot-Token abgelehnt. Füge unten unter „Eigenen Discord-Bot verwenden“ einen aktuellen Token ein.",
  "config.integrationReasonDisallowedIntents":
    "Discord hat die Berechtigungen des Bots (Gateway Intents) abgelehnt. Prüfe die Intents des Bots im Discord Developer Portal.",
  "config.integrationReasonBotNotAuthorized":
    "Discord hat den Bot nicht autorisiert. Prüfe den Bot im Discord Developer Portal und füge seinen Token unten erneut ein.",
  "config.integrationReasonGatewayRejected":
    "Discord hat die Verbindung abgelehnt. Bleibt es dabei, prüfe den Bot-Token unten unter „Eigenen Discord-Bot verwenden“.",
  "config.botConnected": "Bot verbunden",

  "config.discordTitle": "Discord",
  "config.discordStep1Title": "Glyphoxa zu deinem Server hinzufügen",
  "config.discordStep2Title": "Deinen Server verknüpfen",
  "config.discordStep2Copy": "Füge einen Einladungs- oder Kanallink von deinem Server ein und verknüpfe ihn dann.",
  "config.linkServer": "Server verknüpfen",
  "config.linkServerNamed": "{server} verknüpfen",
  "config.alreadyLinked": "Dieser Server ist bereits verknüpft.",
  "config.relinkForName":
    "Verknüpft, bevor Servernamen gespeichert wurden: Wähle „Ändern“ und verknüpfe diesen Server erneut, damit sein Name erscheint.",
  "config.linkNeedsManageServer": "Zum Verknüpfen brauchst du auf diesem Discord-Server das Recht „Server verwalten“.",
  "config.couldntLinkTitle": "Der Server konnte nicht verknüpft werden",
  "config.toastLinked": "{server} verknüpft",
  "config.enterServerId": "Stattdessen die Server-ID eingeben",
  "config.linkedServer": "Verknüpfter Server",
  "config.serverIdValue": "Server-ID {id}",
  "config.changeServer": "Ändern",
  "config.ownBotTitle": "Eigenen Discord-Bot verwenden",
  "config.ownBotHint": "Nur, wenn du einen eigenen Bot betreibst",
  "config.botTokenName": "Bot-Token",
  "config.botGetKey": "Bot im Discord Developer Portal erstellen",
  "config.pasteBotToken": "Füge den Discord-Bot-Token ein",
  "config.botTokenHint":
    "Nur nötig, wenn du einen eigenen Discord-Bot betreibst. Sonst lass das Feld leer: Der Bot von Glyphoxa kommt über Schritt 1 dazu.",
  "config.serverIdLabel": "Server-ID",
  "config.serverIdPlaceholder": "z. B. 472093001100",
  "config.serverIdHint":
    "Der Discord-Server, auf dem deine Gruppe spielt. Den Sprachkanal wählst du im Bereich „Spielrunde“.",

  "config.unlinkServer": "Server trennen",
  "config.unlinkTitle": "{server} trennen?",
  "config.unlinkNote":
    "Sessions funktionieren erst wieder, wenn erneut ein Server verknüpft ist — und ein anderer Tisch kann diesen dann verknüpfen.",
  "config.couldntUnlink": "Trennen fehlgeschlagen: {message}",
  "config.toastUnlinked": "{server} getrennt",

  "config.linkPasteLabel": "Discord-Link einfügen",
  "config.linkPastePlaceholder": "https://discord.com/channels/… oder discord.gg/…",
  "config.linkPasteHint": "Ein Einladungs- oder Kanallink vom Server, auf dem deine Gruppe spielt.",
  "config.linkUnreadable":
    "Dieser Link wurde nicht erkannt — füge einen Discord-Kanal- oder Einladungslink ein.",
  "config.resolvingInvite": "Einladung wird geprüft…",
  "config.inviteFound": "{guild} gefunden.",
  "config.inviteInvalid": "Diese Einladung scheint ungültig oder abgelaufen zu sein.",
  "config.inviteFailed": "Die Einladung konnte nicht geprüft werden. Bitte versuch es noch einmal.",
  "config.addBotHint":
    "Nutze in Schritt 1 „Glyphoxa zu deinem Server hinzufügen“ und füge die Einladung dann erneut ein.",

  "config.addBotCopy":
    "Der Bot von Glyphoxa muss Mitglied deines Servers sein, bevor er einer Sprach-Session beitreten kann. Discord fragt, zu welchem Server er hinzugefügt werden soll.",
  "config.addBotAction": "Glyphoxa zu deinem Server hinzufügen",
  "config.addBotNoAppId":
    "Es ist keine Discord-Anwendungs-ID konfiguriert (DISCORD_OAUTH_CLIENT_ID), daher kann dieser Link nicht erstellt werden.",

  "config.spendingLimitsTitle": "Ausgabenlimits",
  "config.spendingLimitsHint": "Optionale Kostengrenzen pro Session.",
  "config.spendCapsLede":
    "Die Beträge sind Schätzungen, keine Abrechnung. Lass ein Feld leer, um das Limit auszuschalten; Änderungen gelten ab der nächsten Session.",
  "config.softLimitLabel": "Neue KI-Antworten pausieren bei (USD)",
  "config.softLimitPlaceholder": "z. B. 5,00",
  "config.softLimitHint": "Laufende Antworten werden zu Ende geführt; die Transkription läuft weiter.",
  "config.hardLimitLabel": "Session beenden bei (USD)",
  "config.hardLimitPlaceholder": "z. B. 10,00",
  "config.hardLimitHint": "Muss mindestens so hoch sein wie der Pausenbetrag.",
  "config.capInvalid": "Gib einen Betrag von 0 oder mehr ein.",
  "config.capHardBelowSoft": "Muss mindestens so hoch sein wie der Betrag, bei dem Antworten pausieren.",
  "config.capsBoth": "Pause bei {pause} · Ende bei {end}",
  "config.capsPauseOnly": "Pause bei {pause}",
  "config.capsEndOnly": "Ende bei {end}",
  "config.capsNone": "Keine Limits gesetzt",
  "config.saveSpendingLimits": "Ausgabenlimits speichern",
  "config.couldntSaveLimits": "Die Limits konnten nicht gespeichert werden",
  "config.toastLimitsSaved": "Ausgabenlimits gespeichert: {limits}",
  "config.spendCapsLoadError": "Gespeicherte Limits konnten nicht geladen werden: {message}",

  "config.tableOverline": "Dein Tisch",
  "config.tableTitle": "Name und Löschen",
  "config.tableNameLabel": "Name des Tisches",
  "config.tableNameHint": "Steht in der Seitenleiste und bei Spielern, die du einlädst.",
  "config.renameTable": "Tisch umbenennen",
  "config.couldntRenameTable": "Der Tisch konnte nicht umbenannt werden",
  "config.toastTableRenamed": "Tisch umbenannt in {name}",
  "config.tableDeletePre": "Um deinen Tisch mit allem darin zu löschen, ",
  "config.tableDeleteLink": "schreib dem Betreiber",
  "config.tableDeletePost": ". Das lässt sich nicht rückgängig machen.",
  "config.tableDeleteSubject": "Glyphoxa: meinen Tisch löschen",
  "config.planChangeSubject": "Glyphoxa: meinen Tisch auf den Tarif mit eigenen Schlüsseln umstellen",
};
