// Auth / landing / onboarding fragment. `en` is the key source of truth; `de`
// must cover every key (compile-checked via Record<keyof typeof en, string>).
//
// Deliberately NOT keyed here: the proper names of the authored German legal
// documents (Nutzungsbedingungen, Datenschutzerklärung, Impressum) — they are
// the titles of the documents themselves (#518) and stay verbatim in both
// display languages, so the consent line and footer link to them by name.

export const en = {
  // ---- Login screen (ADR-0016 / ADR-0055 / #518) ----
  "auth.ledeOpen": "Sign in with Discord — your first sign-in creates your own table.",
  "auth.ledeAllowlist": "Sign in to run your table.",
  // Mode-neutral and non-leaky by design (ADR-0041/ADR-0055): the copy must
  // name neither the allowlist nor a suspension as the cause.
  "auth.notAuthorized": "This Discord account isn't authorized here.",
  "auth.aupRequired": "Please accept the Nutzungsbedingungen below before signing in.",
  // The AUP consent sentence, split around the two document links (the JSX
  // anchors live in the component; the document names stay verbatim).
  "auth.consentPre": "I agree to the ",
  "auth.consentMid": " and have read the ",
  "auth.consentPost": ".",
  "auth.continueDiscord": "Continue with Discord",
  "auth.continueGoogleSoon": "Continue with Google · coming soon",
  "auth.continueGithubSoon": "Continue with GitHub · coming soon",

  // ---- Onboarding: name-your-table step (ADR-0055) ----
  "auth.onboardingTitle": "Welcome to Glyphoxa",
  "auth.onboardingLede": "Your table is ready — give it a name.",
  "auth.onboardingNameLabel": "Name your table",
  "auth.onboardingLoadError": "Couldn't load your account. Please reload to try again.",
  "auth.onboardingSaveError":
    "Couldn't save the name — you can rename your table later, or try again now.",
  "auth.onboardingSaveContinue": "Save and continue",
  "auth.onboardingSkip": "Skip for now",

  // ---- AuthGate boot error ----
  "auth.gateLoadError": "Could not load your account: {message}",

  // ---- Landing page (#521) ----
  "auth.landingSignIn": "Sign in",
  // The hero headline, split around the gradient-highlighted span.
  "auth.landingHeadlinePre": "Give your ",
  "auth.landingHeadlineAccent": "NPCs a voice",
  "auth.landingHeadlinePost": " at the table",
  "auth.landingLede":
    "Glyphoxa is a self-hosted companion for tabletop RPGs played over Discord. It voices your campaign's characters in the voice channel, keeps the transcript, and remembers what happened — while the GM stays in charge of all of it.",
  "auth.landingCtaOpen": "Start with Discord",
  "auth.landingCtaSignIn": "Sign in with Discord",
  "auth.landingCtaNoteOpen": "Free while in beta · sign in with the Discord account you play on",
  "auth.landingCtaNote": "Sign in with the Discord account you play on",
  "auth.landingFeaturesAria": "What Glyphoxa does",
  "auth.featureVoiceTitle": "NPCs that talk back",
  "auth.featureVoiceBody":
    "Your NPCs join the Discord voice channel, hear the table, and answer out loud in their own voice — addressed by name, or by whoever the scene is pointing at.",
  "auth.featureTranscriptTitle": "The session, written down",
  "auth.featureTranscriptBody":
    "Every session is transcribed and searchable afterwards, and the table's own history feeds back into what the NPCs remember.",
  "auth.featureGmTitle": "Run by the GM, not by the bot",
  "auth.featureGmBody":
    "You write the personalities, mute an NPC mid-scene, put words in their mouth with /say, and steer the next reply with a directive. Audio recording is opt-in, per player.",
  "auth.featureKeysTitle": "Your keys, your data",
  "auth.featureKeysBody":
    "Bring your own AI service keys and the usage is yours alone. Everything lives in a single database on the operator's own server, and deleting a campaign removes all of it.",
  "auth.landingShotsAria": "Screenshots",
  "auth.shotSessionCaption":
    "The live session screen: the transcript as it happens, every NPC one mute button away.",
  "auth.shotHighlightsCaption":
    "Highlights: the table's best moments — clipped, scored and illustrated.",
  "auth.shotSearchCaption":
    "Every session is transcribed, and searchable across the whole campaign.",
  "auth.shotPersonaCaption": "An NPC's personality and voice, in the campaign editor.",
  "auth.shotWikiCaption":
    "The world wiki as a relationship map: who knows whom, and what stays GM-only.",
  "auth.shotMapsCaption":
    "Maps — uploaded or generated — with the world's entries pinned in place.",
  "auth.shotPlanningCaption":
    "Planning chat with the Butler: it reads your transcripts and wiki before it answers.",
  "auth.shotKeysCaption":
    "Your AI service keys, checked and working — models and voices stay your choice.",
  "auth.betaHeading": "How the free beta works",
  "auth.betaTrialTitle": "Try it on us",
  "auth.betaTrialBody":
    "New tables start with a small monthly allowance of AI usage paid for by this server. No card, nothing to cancel. When the allowance is used up, the voice features pause until the next month — or ask the operator to move you to the free bring-your-own-keys plan.",
  "auth.betaByokTitle": "Then bring your own keys",
  "auth.betaByokBody":
    "For real campaigns, add your own AI service keys (speech, language AI, images) in the settings and ask to be moved to the bring-your-own-keys plan. Usage is then billed to you by those services, and this server charges nothing on top.",
  "auth.betaNote":
    "This is an open beta on a small self-hosted server: expect rough edges and the occasional restart, and keep your own notes on anything you would hate to lose.",
  // The transcription honesty note, split around the Datenschutzerklärung link.
  "auth.transcribedNotePre":
    "Sessions are transcribed — the bot says so in the voice channel before it starts — and audio recording is opt-in per player. Details in the ",
  "auth.transcribedNotePost": ".",

  // ---- Placeholder screen + router notFound ----
  "auth.notFoundTitle": "Not found",
  "auth.placeholderLede": "This screen is coming in a later update.",
  "auth.placeholderSoon": "coming soon",

  // ---- Legal page chrome (#518) — the documents themselves stay verbatim ----
  "auth.legalUpdatedLabel": "Last updated:",
} as const;

export const de: Record<keyof typeof en, string> = {
  "auth.ledeOpen": "Melde dich mit Discord an — deine erste Anmeldung erstellt deinen eigenen Tisch.",
  "auth.ledeAllowlist": "Melde dich an, um deinen Tisch zu leiten.",
  "auth.notAuthorized": "Dieser Discord-Account ist hier nicht freigeschaltet.",
  "auth.aupRequired": "Bitte akzeptiere unten die Nutzungsbedingungen, bevor du dich anmeldest.",
  // "Ich akzeptiere die <Nutzungsbedingungen> und habe die
  // <Datenschutzerklärung> gelesen." — the verb lands in the Post segment.
  "auth.consentPre": "Ich akzeptiere die ",
  "auth.consentMid": " und habe die ",
  "auth.consentPost": " gelesen.",
  "auth.continueDiscord": "Weiter mit Discord",
  "auth.continueGoogleSoon": "Weiter mit Google · bald verfügbar",
  "auth.continueGithubSoon": "Weiter mit GitHub · bald verfügbar",

  "auth.onboardingTitle": "Willkommen bei Glyphoxa",
  "auth.onboardingLede": "Dein Tisch ist bereit — gib ihm einen Namen.",
  "auth.onboardingNameLabel": "Name deines Tisches",
  "auth.onboardingLoadError":
    "Dein Konto konnte nicht geladen werden. Lade die Seite neu und versuch es noch einmal.",
  "auth.onboardingSaveError":
    "Der Name konnte nicht gespeichert werden — du kannst deinen Tisch später umbenennen oder es jetzt noch einmal versuchen.",
  "auth.onboardingSaveContinue": "Speichern und weiter",
  "auth.onboardingSkip": "Jetzt überspringen",

  "auth.gateLoadError": "Dein Konto konnte nicht geladen werden: {message}",

  "auth.landingSignIn": "Anmelden",
  "auth.landingHeadlinePre": "Gib deinen ",
  "auth.landingHeadlineAccent": "NPCs eine Stimme",
  "auth.landingHeadlinePost": " am Spieltisch",
  "auth.landingLede":
    "Glyphoxa ist ein selbst gehosteter Begleiter für Tischrollenspiele über Discord. Es gibt den Charakteren deiner Kampagne im Voice-Channel eine Stimme, führt die Mitschrift und erinnert sich, was passiert ist — und der GM behält über alles die Kontrolle.",
  "auth.landingCtaOpen": "Mit Discord loslegen",
  "auth.landingCtaSignIn": "Mit Discord anmelden",
  "auth.landingCtaNoteOpen":
    "Kostenlos während der Beta · melde dich mit dem Discord-Account an, mit dem du spielst",
  "auth.landingCtaNote": "Melde dich mit dem Discord-Account an, mit dem du spielst",
  "auth.landingFeaturesAria": "Was Glyphoxa kann",
  "auth.featureVoiceTitle": "NPCs, die antworten",
  "auth.featureVoiceBody":
    "Deine NPCs kommen mit in den Discord-Voice-Channel, hören dem Tisch zu und antworten laut mit eigener Stimme — wenn sie beim Namen gerufen werden oder die Szene gerade auf sie zeigt.",
  "auth.featureTranscriptTitle": "Die Session, schwarz auf weiß",
  "auth.featureTranscriptBody":
    "Jede Session wird mitgeschrieben und ist danach durchsuchbar — und die Geschichte eures Tisches fließt zurück in das, woran sich die NPCs erinnern.",
  "auth.featureGmTitle": "Der GM führt Regie, nicht der Bot",
  "auth.featureGmBody":
    "Du schreibst die Persönlichkeiten, schaltest einen NPC mitten in der Szene stumm, legst ihm mit /say Worte in den Mund und lenkst die nächste Antwort mit einer Regieanweisung. Audio wird nur aufgenommen, wenn Spieler einzeln zustimmen.",
  "auth.featureKeysTitle": "Deine Schlüssel, deine Daten",
  "auth.featureKeysBody":
    "Bring deine eigenen KI-Dienst-Schlüssel mit — die Nutzung gehört dir allein. Alles liegt in einer einzigen Datenbank auf dem Server des Betreibers, und wenn du eine Kampagne löschst, ist alles davon weg.",
  "auth.landingShotsAria": "Screenshots",
  "auth.shotSessionCaption":
    "Der Live-Bildschirm der Session: die Mitschrift, während sie entsteht — jeder NPC nur einen Stummschalter entfernt.",
  "auth.shotHighlightsCaption":
    "Highlights: die besten Momente des Tisches — als Clip, bewertet und illustriert.",
  "auth.shotSearchCaption":
    "Jede Session wird mitgeschrieben — und ist über die ganze Kampagne durchsuchbar.",
  "auth.shotPersonaCaption": "Persönlichkeit und Stimme eines NPCs im Kampagnen-Editor.",
  "auth.shotWikiCaption":
    "Das Welt-Wiki als Beziehungskarte: wer wen kennt — und was GM-only bleibt.",
  "auth.shotMapsCaption":
    "Karten — hochgeladen oder generiert — mit den Einträgen deiner Welt als Pins.",
  "auth.shotPlanningCaption":
    "Planungs-Chat mit dem Butler: Er liest erst Mitschriften und Wiki, dann antwortet er.",
  "auth.shotKeysCaption":
    "Deine KI-Dienst-Schlüssel, geprüft und einsatzbereit — Modelle und Stimmen bleiben deine Wahl.",
  "auth.betaHeading": "So funktioniert die kostenlose Beta",
  "auth.betaTrialTitle": "Geht auf uns",
  "auth.betaTrialBody":
    "Neue Tische starten mit einem kleinen monatlichen Guthaben an KI-Nutzung, das dieser Server übernimmt. Keine Kreditkarte, nichts zu kündigen. Ist das Guthaben aufgebraucht, pausieren die Sprachfunktionen bis zum nächsten Monat — oder du bittest den Betreiber, dich auf den kostenlosen Plan mit eigenen Schlüsseln umzustellen.",
  "auth.betaByokTitle": "Dann mit eigenen Schlüsseln",
  "auth.betaByokBody":
    "Für richtige Kampagnen hinterlegst du deine eigenen KI-Dienst-Schlüssel (Sprache, KI-Modell, Bilder) in den Einstellungen und bittest darum, auf den Plan mit eigenen Schlüsseln umgestellt zu werden. Die Nutzung rechnen diese Dienste dann direkt mit dir ab — dieser Server verlangt nichts obendrauf.",
  "auth.betaNote":
    "Das ist eine offene Beta auf einem kleinen, selbst gehosteten Server: Rechne mit Ecken und Kanten und gelegentlichen Neustarts — und behalte eigene Notizen zu allem, was du ungern verlieren würdest.",
  "auth.transcribedNotePre":
    "Sessions werden mitgeschrieben — der Bot sagt das im Voice-Channel an, bevor es losgeht — und Audio wird nur mit Zustimmung der einzelnen Spieler aufgenommen. Details in der ",
  "auth.transcribedNotePost": ".",

  "auth.notFoundTitle": "Nicht gefunden",
  "auth.placeholderLede": "Dieser Bereich kommt mit einem späteren Update.",
  "auth.placeholderSoon": "bald verfügbar",

  "auth.legalUpdatedLabel": "Stand:",
};
