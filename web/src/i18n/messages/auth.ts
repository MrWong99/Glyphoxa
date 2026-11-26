// Auth / landing / onboarding fragment. `en` is the key source of truth; `de`
// must cover every key (compile-checked via Record<keyof typeof en, string>).
//
// Deliberately NOT keyed here: the proper names of the authored German legal
// documents (Nutzungsbedingungen, Datenschutzerklärung, Impressum) — they are
// the titles of the documents themselves (#518) and stay verbatim in both
// display languages, so the consent line and footer link to them by name.

export const en = {
  // ---- Login screen (ADR-0016 / ADR-0055 / #518) ----
  "auth.loginDocTitle": "Sign in",
  // The wordmark is the way back to the start page.
  "auth.loginHome": "Glyphoxa — start page",
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
  // Why the Discord button is not available yet — tied to it with
  // aria-describedby, so the reason is read with the button.
  "auth.continueNeedsConsent": "Tick the box above to continue.",
  "auth.continueChecking": "Checking how sign-in works on this server…",
  "auth.whatIsGlyphoxa": "What is Glyphoxa?",

  // ---- Onboarding: name-your-table step (ADR-0055) ----
  "auth.onboardingTitle": "Welcome to Glyphoxa",
  "auth.onboardingLede": "Your table is ready — give it a name.",
  "auth.onboardingTableExplainer":
    "A table is your group's own space in Glyphoxa: its campaigns, NPCs, Discord server and settings all live there.",
  "auth.onboardingNameLabel": "Name your table",
  "auth.onboardingLoadError": "Couldn't load your account. Please reload to try again.",
  "auth.onboardingSaveError":
    "Couldn't save the name — you can rename your table later, or try again now.",
  "auth.onboardingSaveContinue": "Save and continue",
  "auth.onboardingSkip": "Skip for now",

  // ---- AuthGate boot error ----
  "auth.gateLoadError": "Couldn't load your account: {message}",

  // ---- Landing page (#521) ----
  // COPY RULE (#258): everything here describes what ships today. No roadmap
  // in the present tense, no invented numbers, no price figures.
  "auth.landingDocTitle": "Voiced NPCs for tabletop RPGs on Discord",
  "auth.landingHome": "Glyphoxa — top of the page",
  "auth.landingSignIn": "Sign in",
  // The sticky bar's in-page links.
  "auth.landingNavAria": "On this page",
  "auth.landingNavStory": "What it does",
  "auth.landingNavHow": "How it works",
  "auth.landingNavBeta": "Free beta",
  "auth.landingNavPrivacy": "Privacy",

  // The hero headline, split around the gradient-highlighted span.
  "auth.landingHeadlinePre": "Give your ",
  "auth.landingHeadlineAccent": "NPCs a voice",
  "auth.landingHeadlinePost": " at the table",
  // The lede, by posture: an open deployment is the hosted beta (someone else
  // runs the server for you); anything else is an install its operator runs.
  "auth.landingLedeHosted":
    "Glyphoxa is a companion for tabletop RPGs played over Discord. It voices your campaign's characters in the voice channel, keeps the transcript, and remembers what happened — while the GM stays in charge of all of it.",
  "auth.landingLedeSelfHosted":
    "Glyphoxa is a self-hosted companion for tabletop RPGs played over Discord. It voices your campaign's characters in the voice channel, keeps the transcript, and remembers what happened — while the GM stays in charge of all of it.",
  "auth.landingCtaOpen": "Start with Discord",
  "auth.landingCtaSignIn": "Sign in with Discord",
  "auth.landingCtaNoteOpen": "Free while in beta · sign in with the Discord account you play on",
  "auth.landingCtaNote": "Sign in with the Discord account you play on",
  "auth.landingCtaHow": "See how it works",

  // Screenshots: real captures of a staged demo campaign. The caption is the
  // image's text alternative too.
  "auth.shotEnlarge": "View larger: {caption}",
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

  // The story: five rows, each a moment of running a campaign.
  "auth.storyHeading": "What Glyphoxa does",
  "auth.storyTableOverline": "At the table",
  "auth.storyTableTitle": "NPCs that talk back",
  "auth.storyTableBody":
    "Your NPCs join the Discord voice channel, hear the table, and answer out loud in their own voice — when they are called by name, or when the scene is pointing at them. You write who they are: a personality, a voice, and whether they wait to be addressed.",
  "auth.storyTablePoint1": "Mute any NPC in the middle of a scene",
  "auth.storyTablePoint2": "Put words in their mouth with /say",
  "auth.storyTablePoint3": "Steer the next reply with a directive",
  "auth.storyAfterOverline": "After the session",
  "auth.storyAfterTitle": "The session, written down",
  "auth.storyAfterBody":
    "Every session is transcribed and searchable afterwards, across the whole campaign. The table's best moments are kept as highlights, and a recap is one click away.",
  "auth.storyWorldOverline": "Your world",
  "auth.storyWorldTitle": "A world that remembers",
  "auth.storyWorldBody":
    "The world wiki holds your places, people and factions, and who knows whom. What happens at the table comes back as suggestions you approve, and your NPCs draw on it when they answer. Maps pin the world's entries in place.",
  "auth.storyPrepOverline": "Prep",
  "auth.storyPrepTitle": "Prepare with the Butler",
  "auth.storyPrepBody":
    "Between sessions, ask the Butler in the planning chat. It reads your transcripts and your wiki before it answers, so a question about last session is answered from what actually happened at your table.",
  "auth.storyKeysOverline": "Your keys",
  "auth.storyKeysTitle": "Your keys, your data",
  "auth.storyKeysBody":
    "Bring your own AI service keys and the usage is yours alone. Everything lives in a single database on the operator's own server. You can export a campaign at any time, and deleting it removes all of it.",

  // "How it works": the wording follows the app — the screen and section
  // names in these steps are the ones the GM will see (pinned by a test).
  "auth.howHeading": "How it works",
  "auth.howLede": "Five steps from signing in to the first NPC answering.",
  "auth.howStep1Title": "Sign in with Discord",
  "auth.howStep1BodyOpen": "Your first sign-in creates your own table: its campaigns, NPCs and settings live there.",
  "auth.howStep1Body": "Use the Discord account you play on.",
  "auth.howStep2Title": "Connect your Discord server",
  "auth.howStep2Body":
    "In Setup, under “Discord connection”, add Glyphoxa to your server and save the server your group plays on.",
  "auth.howStep3Title": "Add your AI service keys",
  "auth.howStep3Body":
    "Still in Setup, under “AI service keys”: one for the AI model, one for voice & speech. Each is checked and shows “Working” once it answers.",
  "auth.howStep3BodyOpen":
    "On the free beta you can skip this at first: a small monthly allowance is included.",
  "auth.howStep4Title": "Create a campaign and its cast",
  "auth.howStep4Body":
    "Your AI game assistant, the Butler, is created with the campaign. Add your NPCs under Cast and give each a personality and a voice.",
  "auth.howStep5Title": "Start a session",
  "auth.howStep5Body":
    "On the Session screen, pick the voice channel and press “Start session”. Glyphoxa joins, and your NPCs answer when they are spoken to.",

  // The free beta — rendered on an explicitly OPEN deployment only.
  "auth.betaHeading": "How the free beta works",
  "auth.betaTrialTitle": "Try it on us",
  "auth.betaTrialLede":
    "New tables start with a small monthly allowance of AI usage paid for by this server.",
  "auth.betaTrialPoint1": "No card, nothing to cancel",
  "auth.betaTrialPoint2": "When the allowance is used up, the voice features pause until the next month",
  // Split around the link to the operator's contact (the Impressum).
  "auth.betaTrialPoint3Pre": "Or ",
  "auth.betaAskOperator": "ask the operator",
  "auth.betaTrialPoint3Post": " to move you to the free bring-your-own-keys plan",
  "auth.betaByokTitle": "Then bring your own keys",
  "auth.betaByokLede":
    "For real campaigns, add your own AI service keys (speech, language AI, images) in Setup.",
  "auth.betaByokPoint1": "Usage is billed to you by those services",
  "auth.betaByokPoint2": "This server charges nothing on top",
  "auth.betaByokPoint3Pre": "To switch, ",
  "auth.betaByokPoint3Post": " to move your table to the bring-your-own-keys plan",
  "auth.betaNote":
    "This is an open beta on a small, privately run server: expect rough edges and the occasional restart, and keep your own notes on anything you would hate to lose.",

  // "Privacy at the table" — the transcription honesty note, split around the
  // Datenschutzerklärung link. Shown on every deployment.
  "auth.privacyHeading": "Privacy at the table",
  "auth.transcribedNotePre":
    "Sessions are transcribed — the bot says so in the voice channel's chat when a session starts — and audio recording is opt-in per player. Details in the ",
  "auth.transcribedNotePost": ".",

  // The closing call to action and the footer.
  "auth.closingHeading": "Your NPCs are waiting for a voice",
  "auth.closingBody": "Sign in, connect your Discord server, and let them speak at your next session.",
  "auth.footerTagline": "Voiced NPCs and a campaign memory for tabletop RPGs on Discord.",
  "auth.footerOpenSource": "Glyphoxa is open-source software.",
  "auth.footerSource": "Source code on GitHub",
  "auth.footerNavAria": "Glyphoxa",

  // ---- Placeholder (in-shell not-found) screen + router notFound ----
  "auth.notFoundTitle": "Not found",
  "auth.placeholderLede": "This page doesn't exist — the link may be outdated or mistyped.",
  "auth.placeholderBackToSetup": "Go to Setup",

  // ---- Legal page chrome (#518) — the documents themselves stay verbatim ----
  "auth.legalUpdatedLabel": "Last updated:",
  "auth.legalHome": "Glyphoxa — start page",
  "auth.legalDocsNav": "Legal documents",
  // Shown when the display language is not German: the documents are authored
  // in German and that text is the one that counts.
  "auth.legalGermanNotice":
    "This document is written in German. The German text is the authoritative version.",

  // ---- Router-level error + not-found pages ----
  "auth.errorTitle": "Something went wrong",
  "auth.errorLede": "This page ran into an unexpected error. Nothing you saved is lost — try again, or reload the page.",
  "auth.errorRetry": "Try again",
  "auth.errorReload": "Reload page",
  "auth.errorDetails": "Technical details",
  "auth.notFoundLede": "There is no page at this address.",
  "auth.notFoundHome": "Back to the start page",
} as const;

export const de: Record<keyof typeof en, string> = {
  "auth.loginDocTitle": "Anmelden",
  "auth.loginHome": "Glyphoxa — Startseite",
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
  "auth.continueNeedsConsent": "Setze oben das Häkchen, um fortzufahren.",
  "auth.continueChecking": "Es wird geprüft, wie die Anmeldung auf diesem Server funktioniert…",
  "auth.whatIsGlyphoxa": "Was ist Glyphoxa?",

  "auth.onboardingTitle": "Willkommen bei Glyphoxa",
  "auth.onboardingLede": "Dein Tisch ist bereit — gib ihm einen Namen.",
  "auth.onboardingTableExplainer":
    "Ein Tisch ist der eigene Bereich deiner Gruppe in Glyphoxa: Dort liegen ihre Kampagnen, NPCs, der Discord-Server und alle Einstellungen.",
  "auth.onboardingNameLabel": "Name deines Tisches",
  "auth.onboardingLoadError":
    "Dein Konto konnte nicht geladen werden. Lade die Seite neu und versuch es noch einmal.",
  "auth.onboardingSaveError":
    "Der Name konnte nicht gespeichert werden — du kannst deinen Tisch später umbenennen oder es jetzt noch einmal versuchen.",
  "auth.onboardingSaveContinue": "Speichern und weiter",
  "auth.onboardingSkip": "Jetzt überspringen",

  "auth.gateLoadError": "Dein Konto konnte nicht geladen werden: {message}",

  "auth.landingDocTitle": "NPCs mit Stimme für Pen-&-Paper-Runden auf Discord",
  "auth.landingHome": "Glyphoxa — zum Seitenanfang",
  "auth.landingSignIn": "Anmelden",
  "auth.landingNavAria": "Auf dieser Seite",
  "auth.landingNavStory": "Was es kann",
  "auth.landingNavHow": "So funktioniert es",
  "auth.landingNavBeta": "Kostenlose Beta",
  "auth.landingNavPrivacy": "Datenschutz",

  "auth.landingHeadlinePre": "Gib deinen ",
  "auth.landingHeadlineAccent": "NPCs eine Stimme",
  "auth.landingHeadlinePost": " am Spieltisch",
  "auth.landingLedeHosted":
    "Glyphoxa ist ein Begleiter für Pen-&-Paper-Runden über Discord. Es gibt den Charakteren deiner Kampagne im Sprachkanal eine Stimme, führt die Mitschrift und erinnert sich, was passiert ist — und der GM behält über alles die Kontrolle.",
  "auth.landingLedeSelfHosted":
    "Glyphoxa ist ein selbst gehosteter Begleiter für Pen-&-Paper-Runden über Discord. Es gibt den Charakteren deiner Kampagne im Sprachkanal eine Stimme, führt die Mitschrift und erinnert sich, was passiert ist — und der GM behält über alles die Kontrolle.",
  "auth.landingCtaOpen": "Mit Discord loslegen",
  "auth.landingCtaSignIn": "Mit Discord anmelden",
  "auth.landingCtaNoteOpen":
    "Kostenlos während der Beta · melde dich mit dem Discord-Account an, mit dem du spielst",
  "auth.landingCtaNote": "Melde dich mit dem Discord-Account an, mit dem du spielst",
  "auth.landingCtaHow": "So funktioniert es",

  "auth.shotEnlarge": "Größer ansehen: {caption}",
  "auth.shotSessionCaption":
    "Der Live-Bildschirm der Session: die Mitschrift, während sie entsteht — jeder NPC nur einen Stummschalter entfernt.",
  "auth.shotHighlightsCaption":
    "Highlights: die besten Momente des Tisches — als Clip, bewertet und illustriert.",
  "auth.shotSearchCaption":
    "Jede Session wird mitgeschrieben — und ist über die ganze Kampagne durchsuchbar.",
  "auth.shotPersonaCaption": "Persönlichkeit und Stimme eines NPCs im Kampagnen-Editor.",
  "auth.shotWikiCaption":
    "Das Welt-Wiki als Beziehungskarte: wer wen kennt — und was nur für GM bleibt.",
  "auth.shotMapsCaption":
    "Karten — hochgeladen oder generiert — mit den Einträgen deiner Welt als Pins.",
  "auth.shotPlanningCaption":
    "Planungs-Chat mit dem Butler: Er liest erst Mitschriften und Wiki, dann antwortet er.",
  "auth.shotKeysCaption":
    "Deine KI-Dienst-Schlüssel, geprüft und einsatzbereit — Modelle und Stimmen bleiben deine Wahl.",

  "auth.storyHeading": "Was Glyphoxa kann",
  "auth.storyTableOverline": "Am Spieltisch",
  "auth.storyTableTitle": "NPCs, die antworten",
  "auth.storyTableBody":
    "Deine NPCs kommen mit in den Discord-Sprachkanal, hören dem Tisch zu und antworten laut mit eigener Stimme — wenn sie beim Namen gerufen werden oder die Szene gerade auf sie zeigt. Wer sie sind, schreibst du: eine Persönlichkeit, eine Stimme, und ob sie warten, bis man sie anspricht.",
  "auth.storyTablePoint1": "Jeden NPC mitten in der Szene stummschalten",
  "auth.storyTablePoint2": "Ihm mit /say Worte in den Mund legen",
  "auth.storyTablePoint3": "Die nächste Antwort mit einer Regieanweisung lenken",
  "auth.storyAfterOverline": "Nach der Session",
  "auth.storyAfterTitle": "Die Session, schwarz auf weiß",
  "auth.storyAfterBody":
    "Jede Session wird mitgeschrieben und ist danach durchsuchbar — über die ganze Kampagne. Die besten Momente des Tisches bleiben als Highlights erhalten, und eine Zusammenfassung ist einen Klick entfernt.",
  "auth.storyWorldOverline": "Deine Welt",
  "auth.storyWorldTitle": "Eine Welt, die sich erinnert",
  "auth.storyWorldBody":
    "Das Welt-Wiki hält deine Orte, Personen und Fraktionen fest — und wer wen kennt. Was am Tisch passiert, kommt als Vorschlag zurück, den du freigibst, und deine NPCs greifen darauf zurück, wenn sie antworten. Karten verorten die Einträge deiner Welt.",
  "auth.storyPrepOverline": "Vorbereitung",
  "auth.storyPrepTitle": "Vorbereiten mit dem Butler",
  "auth.storyPrepBody":
    "Frag zwischen den Sessions den Butler im Planungs-Chat. Er liest deine Mitschriften und dein Wiki, bevor er antwortet — eine Frage zur letzten Session beantwortet er also aus dem, was an eurem Tisch wirklich passiert ist.",
  "auth.storyKeysOverline": "Deine Schlüssel",
  "auth.storyKeysTitle": "Deine Schlüssel, deine Daten",
  "auth.storyKeysBody":
    "Bring deine eigenen KI-Dienst-Schlüssel mit — die Nutzung gehört dir allein. Alles liegt in einer einzigen Datenbank auf dem Server des Betreibers. Du kannst eine Kampagne jederzeit exportieren, und wenn du sie löschst, ist alles davon weg.",

  "auth.howHeading": "So funktioniert es",
  "auth.howLede": "Fünf Schritte vom Anmelden bis zur ersten NPC-Antwort.",
  "auth.howStep1Title": "Mit Discord anmelden",
  "auth.howStep1BodyOpen":
    "Deine erste Anmeldung erstellt deinen eigenen Tisch: Dort liegen seine Kampagnen, NPCs und Einstellungen.",
  "auth.howStep1Body": "Nimm den Discord-Account, mit dem du spielst.",
  "auth.howStep2Title": "Deinen Discord-Server verbinden",
  "auth.howStep2Body":
    "In der Einrichtung unter „Discord-Verbindung“ fügst du Glyphoxa zu deinem Server hinzu und speicherst den Server, auf dem deine Gruppe spielt.",
  "auth.howStep3Title": "Deine KI-Dienst-Schlüssel hinterlegen",
  "auth.howStep3Body":
    "Ebenfalls in der Einrichtung, unter „KI-Dienst-Schlüssel“: einer für das KI-Modell, einer für Stimme & Sprache. Jeder wird geprüft und zeigt „Funktioniert“, sobald er antwortet.",
  "auth.howStep3BodyOpen":
    "In der kostenlosen Beta kannst du das zunächst überspringen: Ein kleines monatliches Guthaben ist inklusive.",
  "auth.howStep4Title": "Kampagne und Besetzung anlegen",
  "auth.howStep4Body":
    "Dein KI-Spielassistent, der Butler, entsteht zusammen mit der Kampagne. Lege deine NPCs unter „Besetzung“ an und gib jedem eine Persönlichkeit und eine Stimme.",
  "auth.howStep5Title": "Eine Session starten",
  "auth.howStep5Body":
    "Wähle auf dem Bildschirm „Spielrunde“ den Sprachkanal und drücke „Session starten“. Glyphoxa kommt dazu, und deine NPCs antworten, wenn man sie anspricht.",

  "auth.betaHeading": "So funktioniert die kostenlose Beta",
  "auth.betaTrialTitle": "Geht auf uns",
  "auth.betaTrialLede":
    "Neue Tische starten mit einem kleinen monatlichen Guthaben an KI-Nutzung, das dieser Server übernimmt.",
  "auth.betaTrialPoint1": "Keine Kreditkarte, nichts zu kündigen",
  "auth.betaTrialPoint2": "Ist das Guthaben aufgebraucht, pausieren die Sprachfunktionen bis zum nächsten Monat",
  // "Oder <bitte den Betreiber>, dich auf den kostenlosen Plan … umzustellen"
  "auth.betaTrialPoint3Pre": "Oder ",
  "auth.betaAskOperator": "bitte den Betreiber",
  "auth.betaTrialPoint3Post": ", dich auf den kostenlosen Plan mit eigenen Schlüsseln umzustellen",
  "auth.betaByokTitle": "Dann mit eigenen Schlüsseln",
  "auth.betaByokLede":
    "Für richtige Kampagnen hinterlegst du deine eigenen KI-Dienst-Schlüssel (Sprache, KI-Modell, Bilder) in der Einrichtung.",
  "auth.betaByokPoint1": "Die Nutzung rechnen diese Dienste direkt mit dir ab",
  "auth.betaByokPoint2": "Dieser Server verlangt nichts obendrauf",
  "auth.betaByokPoint3Pre": "Zum Wechseln ",
  "auth.betaByokPoint3Post": ", deinen Tisch auf den Plan mit eigenen Schlüsseln umzustellen",
  "auth.betaNote":
    "Das ist eine offene Beta auf einem kleinen, privat betriebenen Server: Rechne mit Ecken und Kanten und gelegentlichen Neustarts — und behalte eigene Notizen zu allem, was du ungern verlieren würdest.",

  "auth.privacyHeading": "Datenschutz am Spieltisch",
  "auth.transcribedNotePre":
    "Sessions werden mitgeschrieben — der Bot sagt das im Chat des Sprachkanals, wenn eine Session beginnt — und Audio wird nur mit Zustimmung der einzelnen Spieler aufgenommen. Details in der ",
  "auth.transcribedNotePost": ".",

  "auth.closingHeading": "Deine NPCs warten auf eine Stimme",
  "auth.closingBody":
    "Melde dich an, verbinde deinen Discord-Server und lass sie in eurer nächsten Session sprechen.",
  "auth.footerTagline": "NPCs mit Stimme und ein Kampagnengedächtnis für Pen-&-Paper-Runden auf Discord.",
  "auth.footerOpenSource": "Glyphoxa ist quelloffene Software.",
  "auth.footerSource": "Quellcode auf GitHub",
  "auth.footerNavAria": "Glyphoxa",

  "auth.notFoundTitle": "Nicht gefunden",
  "auth.placeholderLede": "Diese Seite gibt es nicht — der Link ist vielleicht veraltet oder vertippt.",
  "auth.placeholderBackToSetup": "Zur Einrichtung",

  "auth.legalUpdatedLabel": "Stand:",
  "auth.legalHome": "Glyphoxa — Startseite",
  "auth.legalDocsNav": "Rechtliche Dokumente",
  // Never shown in German; present so the catalogs stay in step.
  "auth.legalGermanNotice":
    "Dieses Dokument ist auf Deutsch verfasst. Maßgeblich ist die deutsche Fassung.",

  "auth.errorTitle": "Etwas ist schiefgelaufen",
  "auth.errorLede": "Auf dieser Seite ist ein unerwarteter Fehler aufgetreten. Gespeichertes geht dabei nicht verloren — versuch es noch einmal oder lade die Seite neu.",
  "auth.errorRetry": "Erneut versuchen",
  "auth.errorReload": "Seite neu laden",
  "auth.errorDetails": "Technische Details",
  "auth.notFoundLede": "Unter dieser Adresse gibt es keine Seite.",
  "auth.notFoundHome": "Zur Startseite",
};
