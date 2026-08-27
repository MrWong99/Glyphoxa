// Filled by the per-area localization pass. `en` is the key source of truth;
// `de` must cover every key (compile-checked).

export const en = {
  // — CampaignSwitcher (topbar trigger + panel, #266/#267) —
  "components.campaignOverline": "Campaign",
  "components.createFirstCampaign": "Create your first campaign",
  "components.selectCampaign": "Select campaign",
  "components.switchCampaign": "Switch campaign",
  "components.switchCampaignActive": "Switch campaign — active: {name}",
  "components.campaignsGroupLabel": "Campaigns",
  "components.archivedBadge": "Archived",
  "components.newCampaign": "New campaign",
  "components.campaignSettings": "Campaign settings",
  "components.showArchived": "Show archived",
  "components.hideArchived": "Hide archived",
  "components.couldntLoadCampaigns": "Couldn't load campaigns: {message}",
  "components.couldntSwitchCampaign": "Couldn't switch campaign: {message}",
  "components.sessionLiveNotice":
    "A voice session is live — switching takes effect after it ends.",
  "components.sessionCheckFailedNotice":
    "Couldn't check for a live voice session — a switch may take effect only after it ends.",

  // — CampaignRowActions (per-row lifecycle menu, #269) —
  "components.campaignActions": "Campaign actions",
  "components.campaignActionsFor": "Actions for {name}",
  "components.exportCampaign": "Export (backup file)",
  "components.exporting": "Exporting…",
  "components.archive": "Archive",
  "components.unarchive": "Unarchive",
  "components.deleteEllipsis": "Delete…",
  "components.couldntExportCampaign": "Couldn't export campaign: {message}",
  "components.couldntArchiveCampaign": "Couldn't archive campaign: {message}",
  "components.couldntUnarchiveCampaign": "Couldn't unarchive campaign: {message}",
  "components.couldntDeleteCampaign": "Couldn't delete campaign: {message}",
  "components.deleteCampaignTitle": "Delete “{name}”?",
  "components.deleteCampaignDescription":
    "This permanently deletes the campaign and everything in it — NPCs, world wiki, transcripts, and voice sessions. This cannot be undone.",
  "components.deleteCampaignConfirm": "Delete campaign",
  "components.typeCampaignNameToConfirm": "Type the campaign name to confirm",

  // — Create / settings forms (shared field labels, #267/#268) —
  "components.nameLabel": "Name",
  "components.namePlaceholder": "e.g. The Sunless Citadel",
  "components.gameSystemLabel": "Game system",
  "components.spokenLanguageLabel": "Spoken language",
  "components.createSystemHint": "e.g. dnd5e, pf2e",
  "components.createLanguageHint": "The language your group plays in — e.g. en, de",
  "components.createCampaign": "Create campaign",
  "components.retryActivation": "Retry activation",
  "components.couldntCreateCampaignToast": "Couldn't create the campaign: {message}",
  "components.createdCampaignCouldntSwitchToast":
    "Created the campaign, but couldn't switch to it: {message}",
  "components.createdCouldntSwitchInline": "Created it, but couldn't switch to it: {message}",
  "components.couldntCreateInline": "Couldn't create: {message}",
  "components.settingsSystemHint": "e.g. D&D 5e, Pathfinder 2e",
  "components.spokenLanguageHint":
    "The language your group plays in — applies from the next voice session.",
  "components.languageUnsupported": "{label} (unsupported)",
  "components.couldntLoadLanguages": "Couldn't load the language choices: {message}",
  "components.couldntSaveCampaignSettings": "Couldn't save campaign settings: {message}",
  "components.highlightRecordingLabel": "Highlight recording",
  "components.highlightRecordingHint":
    "Posts a consent message in the voice channel; only speakers who consent are recorded. Applies from the next session.",

  // — ImportCampaignButton (bundle restore, #294) —
  "components.importCampaign": "Import campaign",
  "components.importing": "Importing…",
  "components.importConfirmTitle": "Import campaign?",
  "components.importConfirmBefore": "Create a new campaign from ",
  "components.importConfirmAfter": ". Nothing existing is overwritten.",
  "components.importConfirmAction": "Import",
  "components.importedTitle": "Imported “{name}”",
  "components.importedSwitchPrompt":
    "“{name}” was imported as a new campaign — switch to it now?",
  "components.importSwitch": "Switch",
  "components.importNotNow": "Not now",
  "components.couldntImportCampaign": "Couldn't import campaign: {message}",
  "components.importedCouldntSwitch": "Imported it, but couldn't switch to it: {message}",
  "components.importDroppedRefOne":
    "1 participant reference could not be mapped and was dropped.",
  "components.importDroppedRefMany":
    "{n} participant references could not be mapped and were dropped.",

  // — PlayerBindForm (Character bind, #279) —
  "components.characterNameLabel": "Character name",
  "components.characterNamePlaceholder": "Who does the player play?",
  "components.aliasesLabel": "Aliases",
  "components.removeAlias": "Remove alias {alias}",
  "components.addAliasLabel": "Add alias",
  "components.addAliasPlaceholder": "Add an alias…",
  "components.add": "Add",
  "components.discordUserLabel": "Discord user",
  "components.discordUserIdLabel": "Discord user ID",
  "components.discordUserIdPlaceholder": "Discord user ID (numbers only)",
  "components.discordUserIdDigitsOnly": "A Discord user ID is digits only.",
  "components.fromVoice": "From voice",
  "components.voiceChannelMembers": "Voice channel members",
  "components.bindsTo": "Binds to {name}.",
  "components.bindHint": "Pick a voice-channel member, or paste a Discord user ID.",

  // — SidebarUser / LegalFooter —
  "components.logOut": "Log out",
  "components.legalNavLabel": "Legal",

  // — ui primitives (Select / Combobox / ConfirmDialog defaults) —
  "components.selectPlaceholder": "Select…",
  "components.searchPlaceholder": "Search…",
  "components.noMatches": "No matches",
  "components.useCustomValue": 'Use "{text}"',
  "components.comboboxOptionsLabel": "Options",
  "components.typeToConfirm": "Type “{text}” to confirm",
} as const;

export const de: Record<keyof typeof en, string> = {
  // — CampaignSwitcher —
  "components.campaignOverline": "Kampagne",
  "components.createFirstCampaign": "Erstelle deine erste Kampagne",
  "components.selectCampaign": "Kampagne auswählen",
  "components.switchCampaign": "Kampagne wechseln",
  "components.switchCampaignActive": "Kampagne wechseln — aktiv: {name}",
  "components.campaignsGroupLabel": "Kampagnen",
  "components.archivedBadge": "Archiviert",
  "components.newCampaign": "Neue Kampagne",
  "components.campaignSettings": "Kampagnen-Einstellungen",
  "components.showArchived": "Archivierte anzeigen",
  "components.hideArchived": "Archivierte ausblenden",
  "components.couldntLoadCampaigns": "Kampagnen konnten nicht geladen werden: {message}",
  "components.couldntSwitchCampaign": "Kampagne konnte nicht gewechselt werden: {message}",
  "components.sessionLiveNotice":
    "Gerade läuft eine Session — der Wechsel gilt erst, wenn sie endet.",
  "components.sessionCheckFailedNotice":
    "Konnte nicht prüfen, ob gerade eine Session läuft — ein Wechsel gilt eventuell erst nach ihrem Ende.",

  // — CampaignRowActions —
  "components.campaignActions": "Kampagnen-Aktionen",
  "components.campaignActionsFor": "Aktionen für {name}",
  "components.exportCampaign": "Exportieren (Backup-Datei)",
  "components.exporting": "Wird exportiert…",
  "components.archive": "Archivieren",
  "components.unarchive": "Wiederherstellen",
  "components.deleteEllipsis": "Löschen…",
  "components.couldntExportCampaign": "Export fehlgeschlagen: {message}",
  "components.couldntArchiveCampaign": "Archivieren fehlgeschlagen: {message}",
  "components.couldntUnarchiveCampaign": "Wiederherstellen fehlgeschlagen: {message}",
  "components.couldntDeleteCampaign": "Löschen fehlgeschlagen: {message}",
  "components.deleteCampaignTitle": "„{name}“ löschen?",
  "components.deleteCampaignDescription":
    "Das löscht die Kampagne endgültig — mit allen NPCs, dem Welt-Wiki, Mitschriften und Sessions. Das kann nicht rückgängig gemacht werden.",
  "components.deleteCampaignConfirm": "Kampagne löschen",
  "components.typeCampaignNameToConfirm": "Gib den Kampagnennamen ein, um zu bestätigen",

  // — Create / settings forms —
  "components.nameLabel": "Name",
  "components.namePlaceholder": "z. B. The Sunless Citadel",
  "components.gameSystemLabel": "Spielsystem",
  "components.spokenLanguageLabel": "Gesprochene Sprache",
  "components.createSystemHint": "z. B. dnd5e, pf2e",
  "components.createLanguageHint":
    "Die Sprache, in der eure Gruppe spielt — z. B. en, de",
  "components.createCampaign": "Kampagne erstellen",
  "components.retryActivation": "Aktivierung wiederholen",
  "components.couldntCreateCampaignToast":
    "Kampagne konnte nicht erstellt werden: {message}",
  "components.createdCampaignCouldntSwitchToast":
    "Kampagne erstellt, aber der Wechsel ist fehlgeschlagen: {message}",
  "components.createdCouldntSwitchInline":
    "Erstellt, aber der Wechsel ist fehlgeschlagen: {message}",
  "components.couldntCreateInline": "Erstellen fehlgeschlagen: {message}",
  "components.settingsSystemHint": "z. B. D&D 5e, Pathfinder 2e",
  "components.spokenLanguageHint":
    "Die Sprache, in der eure Gruppe spielt — gilt ab der nächsten Session.",
  "components.languageUnsupported": "{label} (nicht unterstützt)",
  "components.couldntLoadLanguages":
    "Sprachauswahl konnte nicht geladen werden: {message}",
  "components.couldntSaveCampaignSettings":
    "Einstellungen konnten nicht gespeichert werden: {message}",
  "components.highlightRecordingLabel": "Highlight-Aufnahme",
  "components.highlightRecordingHint":
    "Postet eine Zustimmungsnachricht im Sprachkanal; aufgenommen wird nur, wer zustimmt. Gilt ab der nächsten Session.",

  // — ImportCampaignButton —
  "components.importCampaign": "Kampagne importieren",
  "components.importing": "Wird importiert…",
  "components.importConfirmTitle": "Kampagne importieren?",
  "components.importConfirmBefore": "Erstellt eine neue Kampagne aus ",
  "components.importConfirmAfter": ". Bestehendes wird nicht überschrieben.",
  "components.importConfirmAction": "Importieren",
  "components.importedTitle": "„{name}“ importiert",
  "components.importedSwitchPrompt":
    "„{name}“ wurde als neue Kampagne importiert — jetzt zu ihr wechseln?",
  "components.importSwitch": "Wechseln",
  "components.importNotNow": "Nicht jetzt",
  "components.couldntImportCampaign": "Import fehlgeschlagen: {message}",
  "components.importedCouldntSwitch":
    "Importiert, aber der Wechsel ist fehlgeschlagen: {message}",
  "components.importDroppedRefOne":
    "1 Teilnehmer-Verknüpfung konnte nicht zugeordnet werden und wurde entfernt.",
  "components.importDroppedRefMany":
    "{n} Teilnehmer-Verknüpfungen konnten nicht zugeordnet werden und wurden entfernt.",

  // — PlayerBindForm —
  "components.characterNameLabel": "Charaktername",
  "components.characterNamePlaceholder": "Wen spielt der Spieler?",
  "components.aliasesLabel": "Aliasse",
  "components.removeAlias": "Alias {alias} entfernen",
  "components.addAliasLabel": "Alias hinzufügen",
  "components.addAliasPlaceholder": "Alias hinzufügen…",
  "components.add": "Hinzufügen",
  "components.discordUserLabel": "Discord-Nutzer",
  "components.discordUserIdLabel": "Discord-Nutzer-ID",
  "components.discordUserIdPlaceholder": "Discord-Nutzer-ID (nur Ziffern)",
  "components.discordUserIdDigitsOnly": "Eine Discord-Nutzer-ID besteht nur aus Ziffern.",
  "components.fromVoice": "Aus dem Sprachkanal",
  "components.voiceChannelMembers": "Mitglieder im Sprachkanal",
  "components.bindsTo": "Wird mit {name} verknüpft.",
  "components.bindHint":
    "Wähle jemanden aus dem Sprachkanal oder füge eine Discord-Nutzer-ID ein.",

  // — SidebarUser / LegalFooter —
  "components.logOut": "Abmelden",
  "components.legalNavLabel": "Rechtliches",

  // — ui primitives —
  "components.selectPlaceholder": "Auswählen…",
  "components.searchPlaceholder": "Suchen…",
  "components.noMatches": "Keine Treffer",
  "components.useCustomValue": "„{text}“ verwenden",
  "components.comboboxOptionsLabel": "Optionen",
  "components.typeToConfirm": "Gib „{text}“ ein, um zu bestätigen",
};
