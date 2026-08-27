// Configuration ("Setup") screen strings. `en` is the key source of truth;
// `de` must cover every key (compile-checked). Provider PRODUCT names (Groq,
// ElevenLabs, Gemini, Discord, Glyphoxa, Butler) are never translated — they
// ride in as {name}/{tag}/{guild} params or live in the component as labels.

export const en = {
  // Screen chrome — heading matches the sidebar nav ("Setup", shell.navSetup).
  "config.title": "Setup",
  "config.lede": "Connect Discord and the AI services that power your table.",

  // Active-campaign header card.
  "config.activeCampaign": "Active campaign",
  "config.campaignSystem": "System",
  "config.couldntLoadCampaign": "Couldn't load the active campaign: {message}",

  // First-run CTA (#267) — Butler is a product name, explained in plain words.
  "config.firstCampaignTitle": "Create your first campaign",
  "config.firstCampaignLede":
    "No campaign yet. Create one to get started — your AI game assistant, the Butler, is created automatically.",

  // AI service keys (the write-only BYOK slots, ADR-0004). Slot kinds are the
  // plain-language overlines; the product name stays untranslated.
  "config.aiServiceKeysTitle": "AI service keys",
  "config.kindAiModel": "AI model",
  "config.kindVoiceSpeech": "Voice & speech",
  "config.kindImages": "Images",
  "config.kindBot": "Bot",
  "config.pasteApiKey": "Paste your {name} API key",

  // SecretRow controls + aria. {name} is the slot's display name ("Groq",
  // "Bot token"), so the aria strings compose e.g. "Groq key" / "Groq saved".
  "config.secretKeyAria": "{name} key",
  "config.secretSavedAria": "{name} saved",
  "config.replace": "Replace",
  "config.connectedAs": "Connected as {tag}",

  // Groq model picker (#227).
  "config.modelAria": "{name} model",
  "config.modelPlaceholder": "Model…",
  "config.modelSearchPlaceholder": "Search or type a model id…",
  "config.saveModel": "Save model",

  // Health badges (#70) — short and friendly, never internal status words.
  "config.badgeKeyNeeded": "Key needed",
  "config.badgeWorking": "Working",
  "config.badgeDegraded": "Having trouble",

  // Standing Discord integration badge (#489).
  "config.discordIntegrationFailed": "Discord connection failed",
  "config.botConnected": "Bot connected",

  // Discord connection card.
  "config.discordTitle": "Discord connection",
  "config.botTokenName": "Bot token",
  "config.pasteBotToken": "Paste the Discord bot token",
  "config.serverIdLabel": "Server ID",
  "config.serverIdPlaceholder": "e.g. 472093001100",
  "config.serverIdHint":
    "The Discord server your group plays on. Sessions pick their voice channel on the Session screen.",
  "config.saveDiscordSettings": "Save Discord settings",

  // Unlink (#504) — "table", never "tenant", in user-facing copy.
  "config.unlinkServer": "Unlink server",
  "config.unlinkNote":
    "Unlink this Discord server? Sessions stop working until a server is linked again, and another table can then link it.",
  "config.confirmUnlink": "Confirm unlink",
  "config.couldntUnlink": "Couldn't unlink: {message}",

  // "Paste a Discord link" autofill (#101/#105).
  "config.linkPasteLabel": "Paste a Discord link",
  "config.linkPastePlaceholder": "https://discord.com/channels/… or discord.gg/…",
  "config.linkPasteHint": "Paste a channel or invite link to fill in the Server ID.",
  "config.linkUnreadable": "Couldn't read that link — paste a Discord channel or invite link.",
  "config.resolvingInvite": "Checking the invite…",
  "config.serverIdFilledFrom": "Server ID filled in from {guild}.",
  "config.inviteInvalid": "That invite looks invalid or expired.",
  "config.inviteFailed": "Couldn't check that invite. Please try again.",
  // Appended ONLY to the not-a-member precondition (ADR-0047); "foot of this
  // card" must keep matching where the Add-Glyphoxa action renders.
  "config.addBotHint":
    "Use the “Add Glyphoxa to your server” button at the foot of this card, then paste the invite again.",

  // Add-Glyphoxa bot authorization (#110).
  "config.addBotCopy":
    "Adding the Bot to your server is a separate step from saving the Server ID above — the Bot has to be a member before a voice session can join.",
  "config.addBotAction": "Add Glyphoxa to your server",
  "config.addBotNoAppId":
    "No Discord application id is configured (DISCORD_OAUTH_CLIENT_ID), so this link can't be built.",

  // Spending limits (spend caps, #130 / ADR-0046) — behind an AdvancedCard.
  "config.spendingLimitsTitle": "Spending limits",
  "config.spendingLimitsHint": "Optional cost guardrails per voice session.",
  "config.spendCapsLede":
    "Stop a voice session when its estimated AI service spend crosses a limit. Figures are estimates, not billed amounts. Leave a field blank to turn that limit off; changes apply to the next session.",
  "config.softLimitLabel": "Soft limit (USD)",
  "config.softLimitPlaceholder": "e.g. 5.00",
  "config.softLimitHint": "Once crossed, the AI stops taking new turns; replies in progress finish.",
  "config.hardLimitLabel": "Hard limit (USD)",
  "config.hardLimitPlaceholder": "e.g. 10.00",
  "config.hardLimitHint": "Ends the session. Must be at least the soft limit.",
  "config.saveSpendingLimits": "Save spending limits",
} as const;

export const de: Record<keyof typeof en, string> = {
  "config.title": "Einrichtung",
  "config.lede": "Verbinde Discord und die KI-Dienste, die deinen Spieltisch antreiben.",

  "config.activeCampaign": "Aktive Kampagne",
  "config.campaignSystem": "System",
  "config.couldntLoadCampaign": "Die aktive Kampagne konnte nicht geladen werden: {message}",

  "config.firstCampaignTitle": "Erstelle deine erste Kampagne",
  "config.firstCampaignLede":
    "Noch keine Kampagne. Erstelle eine, um loszulegen — dein KI-Spielassistent, der Butler, wird automatisch mit angelegt.",

  "config.aiServiceKeysTitle": "KI-Dienst-Schlüssel",
  "config.kindAiModel": "KI-Modell",
  "config.kindVoiceSpeech": "Stimme & Sprache",
  "config.kindImages": "Bilder",
  "config.kindBot": "Bot",
  "config.pasteApiKey": "Füge deinen {name}-API-Schlüssel ein",

  "config.secretKeyAria": "{name}-Schlüssel",
  "config.secretSavedAria": "{name} gespeichert",
  "config.replace": "Ersetzen",
  "config.connectedAs": "Verbunden als {tag}",

  "config.modelAria": "{name}-Modell",
  "config.modelPlaceholder": "Modell…",
  "config.modelSearchPlaceholder": "Modell-ID suchen oder eintippen…",
  "config.saveModel": "Modell speichern",

  "config.badgeKeyNeeded": "Schlüssel fehlt",
  "config.badgeWorking": "Funktioniert",
  "config.badgeDegraded": "Hat Probleme",

  "config.discordIntegrationFailed": "Discord-Verbindung fehlgeschlagen",
  "config.botConnected": "Bot verbunden",

  "config.discordTitle": "Discord-Verbindung",
  "config.botTokenName": "Bot-Token",
  "config.pasteBotToken": "Füge den Discord-Bot-Token ein",
  "config.serverIdLabel": "Server-ID",
  "config.serverIdPlaceholder": "z. B. 472093001100",
  "config.serverIdHint":
    "Der Discord-Server, auf dem deine Gruppe spielt. Den Sprachkanal wählst du im Bereich „Spielrunde“.",
  "config.saveDiscordSettings": "Discord-Einstellungen speichern",

  "config.unlinkServer": "Server trennen",
  "config.unlinkNote":
    "Diesen Discord-Server trennen? Sessions funktionieren erst wieder, wenn erneut ein Server verknüpft ist — und ein anderer Tisch kann ihn dann verknüpfen.",
  "config.confirmUnlink": "Trennen bestätigen",
  "config.couldntUnlink": "Trennen fehlgeschlagen: {message}",

  "config.linkPasteLabel": "Discord-Link einfügen",
  "config.linkPastePlaceholder": "https://discord.com/channels/… oder discord.gg/…",
  "config.linkPasteHint": "Füge einen Kanal- oder Einladungslink ein, um die Server-ID auszufüllen.",
  "config.linkUnreadable":
    "Dieser Link wurde nicht erkannt — füge einen Discord-Kanal- oder Einladungslink ein.",
  "config.resolvingInvite": "Einladung wird geprüft…",
  "config.serverIdFilledFrom": "Server-ID von {guild} übernommen.",
  "config.inviteInvalid": "Diese Einladung scheint ungültig oder abgelaufen zu sein.",
  "config.inviteFailed": "Die Einladung konnte nicht geprüft werden. Bitte versuch es noch einmal.",
  "config.addBotHint":
    "Nutze unten auf dieser Karte den Button „Glyphoxa zu deinem Server hinzufügen“ und füge die Einladung dann erneut ein.",

  "config.addBotCopy":
    "Den Bot zu deinem Server hinzuzufügen ist ein eigener Schritt neben dem Speichern der Server-ID oben — der Bot muss Mitglied sein, bevor er einer Session im Sprachkanal beitreten kann.",
  "config.addBotAction": "Glyphoxa zu deinem Server hinzufügen",
  "config.addBotNoAppId":
    "Es ist keine Discord-Anwendungs-ID konfiguriert (DISCORD_OAUTH_CLIENT_ID), daher kann dieser Link nicht erstellt werden.",

  "config.spendingLimitsTitle": "Ausgabenlimits",
  "config.spendingLimitsHint": "Optionale Kostengrenzen pro Session.",
  "config.spendCapsLede":
    "Beendet eine Session, wenn ihre geschätzten KI-Dienst-Kosten ein Limit überschreiten. Die Beträge sind Schätzungen, keine Abrechnung. Lass ein Feld leer, um das Limit auszuschalten; Änderungen gelten ab der nächsten Session.",
  "config.softLimitLabel": "Weiches Limit (USD)",
  "config.softLimitPlaceholder": "z. B. 5,00",
  "config.softLimitHint":
    "Danach beginnt die KI keine neuen Antworten mehr; laufende Antworten werden zu Ende geführt.",
  "config.hardLimitLabel": "Hartes Limit (USD)",
  "config.hardLimitPlaceholder": "z. B. 10,00",
  "config.hardLimitHint": "Beendet die Session. Muss mindestens so hoch sein wie das weiche Limit.",
  "config.saveSpendingLimits": "Ausgabenlimits speichern",
};
