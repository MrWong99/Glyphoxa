import { useId, useMemo, useState, type ReactNode } from "react";
import { useQuery } from "@connectrpc/connect-query";
import { timestampDate, timestampMs } from "@bufbuild/protobuf/wkt";
import {
  ArrowRight,
  Check,
  ChevronRight,
  Circle,
  CircleCheck,
  Lightbulb,
  Play,
  ScrollText,
  Settings,
  SlidersHorizontal,
  TriangleAlert,
  Users,
  BookOpen,
} from "lucide-react";

import {
  CampaignService,
  ChatService,
  HealthStatus,
  ProviderService,
  SessionService,
  VoiceService,
  type ProviderHealth,
  type VoiceSession,
} from "@gen/glyphoxa/management/v1/management_pb";
import { campaignDestination } from "@/app/destinations";
import { useScreenNav } from "@/app/screenNav";
import { Badge } from "@/components/ui/Badge";
import { Button } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { EmptyState } from "@/components/ui/EmptyState";
import { PageHeader } from "@/components/ui/Page";
import { QueryBoundary, type QueryLike } from "@/components/ui/QueryBoundary";
import { Skeleton } from "@/components/ui/Skeleton";
import { useI18n, type Lang, type MessageKey } from "@/i18n";
import { deriveNextSteps, unmetSteps, type NextStep } from "@/lib/readiness";
import { formatElapsed, useElapsedSeconds, useLiveSession } from "@/lib/useLiveSession";
import { rosterRetry, useCampaignScreen } from "./campaignContext";
import { worldHealth } from "./graph/worldHealth";
import { rosterPrep, type Readiness } from "./rosterPrep";
import type { CampaignView } from "./views";

import "./overview.css";

// The campaign's Overview — where a campaign opens (ADR-0063 amendment). It
// answers three questions from reads the console already had: what happened
// (the live session, the last sessions), what needs me (pending suggestions,
// NPCs that are not ready, things to tidy in the wiki, a setup that does not
// work) and what is next (the steps a new campaign has not taken yet).
//
// Every block owns its reads and its own loading and failure states, so one
// read failing costs one block, never the page. The blocks share reads through
// the query cache (the same GetKnowledgeGraph feeds the attention band, the
// checklist and a tile), so the page makes each call once.
//
// The panel is router-free: it moves through useCampaignScreen (views of this
// campaign) and useScreenNav (the Session and Setup screens). Its targets are
// buttons, not anchors, for that reason; the same destinations are real links
// in the sidebar beside it.

// GetProviderHealth runs live test calls against the providers, so the
// Overview — the page a GM lands on every visit — reuses an answer for a
// while instead of probing on every mount and window refocus.
const HEALTH_STALE_MS = 10 * 60_000;
// …but not a degraded one. The server caches only a probe that completed, so a
// slot that timed out is retried by the next call; pinning that answer here for
// ten minutes would put the pin back. A degraded answer is fresh for a minute,
// and a window refocus after that asks again.
const HEALTH_DEGRADED_STALE_MS = 60_000;

// How many past sessions the Overview lists.
const LAST_SESSIONS = 3;
// How many NPCs the attention band names before it says "and N more".
const NAMED_NPCS = 3;

// The pending-suggestions read is one cache entry shared with the shell and
// the Suggestions view; like theirs, this observer does not retry the settled
// "no campaign yet" answer.
const retryUnlessNotFound = rosterRetry;

type Query<T> = QueryLike<T> & { isFetching: boolean; fetchStatus: "fetching" | "paused" | "idle" };

// allOf folds several reads into the one QueryBoundary understands: loaded
// once every read has data, failed as soon as one has none and an error, and
// its retry re-runs only the reads that are still missing.
function allOf(...queries: Query<unknown>[]): QueryLike<true> {
  const missing = queries.filter((q) => q.data === undefined);
  const failed = missing.filter((q) => q.isError);
  return {
    data: missing.length === 0 ? true : undefined,
    error: failed[0]?.error,
    isError: failed.length > 0,
    isPending: missing.some((q) => q.isPending),
    isFetching: queries.some((q) => q.isFetching),
    fetchStatus: missing.some((q) => q.fetchStatus === "fetching") ? "fetching" : "idle",
    refetch: () => Promise.all(missing.map((q) => q.refetch())),
  };
}

function languageName(lang: Lang, code: string): string {
  try {
    return new Intl.DisplayNames([lang], { type: "language" }).of(code) ?? code;
  } catch {
    return code; // a tag Intl does not know reads as itself
  }
}

export function OverviewPanel() {
  const { t, lang } = useI18n();
  const { openView } = useCampaignScreen();
  const { goScreen } = useScreenNav();
  // The same cache entry the host resolved the campaign from. Read here rather
  // than from the host's context so the panel also stands on its own.
  const rosterQ = useQuery(CampaignService.method.getCampaignRoster, {}, { retry: rosterRetry });
  const campaign = rosterQ.data?.campaign;
  const { live, startedMs } = useLiveSession();

  const identity = campaign
    ? [campaign.system, campaign.language ? languageName(lang, campaign.language) : ""].filter(Boolean)
    : [];
  const openSession = () => goScreen("session");

  return (
    <div className="gx-overview">
      <PageHeader
        eyebrow={identity.length > 0 ? identity.join(" · ") : undefined}
        title={campaign?.name ?? t("campaign.fallbackTitle")}
        description={t("home.lede")}
        actions={
          <>
            <Button
              variant="secondary"
              iconStart={<SlidersHorizontal />}
              onClick={() => openView("settings")}
            >
              {t("home.campaignSettings")}
            </Button>
            <Button iconStart={live ? <ScrollText /> : <Play />} onClick={openSession}>
              {live ? t("home.openSession") : t("home.startSession")}
            </Button>
          </>
        }
      />

      {live && <LiveCard startedMs={startedMs} onOpen={openSession} />}

      <div className="gx-overview__columns">
        <AttentionBand />
        <NextSteps />
      </div>

      <StatTiles campaignId={campaign?.id} />

      <LastSessions />
    </div>
  );
}

// Section is a titled block of the page: a real region named by its heading.
function Section({
  title,
  aside,
  className = "",
  children,
}: {
  title: string;
  aside?: ReactNode;
  className?: string;
  children: ReactNode;
}) {
  const id = useId();
  return (
    <section className={`gx-overview__section ${className}`.trim()} aria-labelledby={id}>
      <div className="gx-overview__section-head">
        <h2 id={id} className="gx-overview__h">
          {title}
        </h2>
        {aside}
      </div>
      {children}
    </section>
  );
}

// ---- Live session -----------------------------------------------------------

function LiveCard({ startedMs, onOpen }: { startedMs: number | null; onOpen: () => void }) {
  const { t } = useI18n();
  return (
    <Card live className="gx-overview__live">
      <Badge variant="live" dot pulse>
        {t("shell.live")}
      </Badge>
      <div className="gx-overview__live-text">
        <h2 className="gx-overview__h">{t("home.liveTitle")}</h2>
        <p className="gx-overview__muted">{t("home.liveBody")}</p>
      </div>
      <LiveClock startedMs={startedMs} />
      <Button variant="secondary" iconEnd={<ArrowRight />} onClick={onOpen}>
        {t("home.openSession")}
      </Button>
    </Card>
  );
}

// The clock is its own component because it re-renders every second.
function LiveClock({ startedMs }: { startedMs: number | null }) {
  const { t } = useI18n();
  const elapsed = formatElapsed(useElapsedSeconds(startedMs));
  if (startedMs == null) return null;
  return (
    // role=timer: named for a screen reader, and not announced on every tick.
    <span className="gx-overview__clock" role="timer" aria-label={t("home.liveElapsed", { elapsed })}>
      {elapsed}
    </span>
  );
}

// ---- Attention band ---------------------------------------------------------

type AttentionItem = {
  key: string;
  icon: ReactNode;
  tone: "warning" | "danger";
  title: string;
  detail?: ReactNode;
  action: string;
  onAction: () => void;
};

// What a failed readiness check is called in a sentence ("Bart: no voice").
// The roster's own labels name the check when it PASSES ("Voice configured").
const MISSING: Record<string, MessageKey> = {
  persona: "home.missingPersona",
  voice: "home.missingVoice",
  link: "home.missingLink",
  content: "home.missingContent",
  facts: "home.missingFacts",
};

// World-health categories that restate an NPC's readiness. The band reports
// those NPCs once, under "not ready", instead of twice.
const NPC_HEALTH_CATEGORIES = new Set(["empty-voiced", "cast-without-entry"]);

// How ProviderHealth names what failed, by component (#702).
const COMPONENT_LABEL: Record<string, MessageKey> = {
  llm: "home.componentLlm",
  tts: "home.componentTts",
  image: "home.componentImage",
  discord: "home.componentDiscord",
};

// The stable failure classes of ProviderHealth.code, in the GM's language. An
// unknown code (a newer server) falls back to the server's English detail.
const HEALTH_CODE: Record<string, MessageKey> = {
  missing_key: "config.healthMissingKey",
  key_rejected: "config.healthKeyRejected",
  quota_exhausted: "config.healthQuotaExhausted",
  unreachable: "config.healthUnreachable",
  plan_excludes_platform_keys: "config.healthNoPlatformKeys",
  check_failed: "config.healthCheckFailed",
};

// "Never set up" is not "broken". The health probe reports every slot, and a
// slot with no key comes back degraded — which is a fault for the language
// model and the voice service (no session can run without them), but not for
// the two below: image generation is optional, and a Discord connection that
// was never made is the checklist's "Link your Discord server", not an alarm
// on every visit. For those, only a key that exists and fails is reported.
const OPTIONAL_COMPONENTS = new Set(["image", "discord"]);
const NOT_SET_UP_CODES = new Set(["missing_key", "plan_excludes_platform_keys"]);

function AttentionBand() {
  const { t } = useI18n();
  const { openView, openAgent } = useCampaignScreen();
  const { goScreen } = useScreenNav();

  const proposalsQ = useQuery(
    CampaignService.method.listKnowledgeProposals,
    {},
    { retry: retryUnlessNotFound },
  );
  const rosterQ = useQuery(CampaignService.method.getCampaignRoster, {}, { retry: rosterRetry });
  const graphQ = useQuery(CampaignService.method.getKnowledgeGraph, {});
  const readyQ = useQuery(CampaignService.method.getRosterReadiness, {});
  const providersQ = useQuery(ProviderService.method.listProviderConfigs, {});
  const healthQ = useQuery(
    VoiceService.method.getProviderHealth,
    {},
    {
      staleTime: (query) =>
        query.state.data?.providers.some((p) => p.status === HealthStatus.DEGRADED)
          ? HEALTH_DEGRADED_STALE_MS
          : HEALTH_STALE_MS,
    },
  );

  // The NPCs that fail a readiness check, in roster order. Graded by the same
  // rosterPrep() the Cast view's Session prep uses, so the two cannot disagree.
  const notReady = useMemo(() => {
    if (!rosterQ.data || !graphQ.data || !readyQ.data) return [];
    const readiness = new Map<string, Readiness>();
    for (const r of readyQ.data.agents) {
      readiness.set(r.agentId, {
        linked: r.linked,
        factCount: r.factCount,
        chars: r.chars,
        maxChars: r.maxChars,
        truncated: r.truncated,
        lastSpokeAt: r.lastSpokeAt ? timestampDate(r.lastSpokeAt) : undefined,
      });
    }
    const groups = rosterPrep(rosterQ.data.roster, graphQ.data.nodes, graphQ.data.edges, readiness);
    // An NPC in a faction AND a town sits in two groups; it is one NPC.
    const failing = new Map<string, string[]>();
    for (const g of groups) {
      for (const e of g.entries) {
        if (e.exempt || e.ready) continue;
        failing.set(
          e.agent.id,
          e.checks.filter((c) => !c.ok).map((c) => c.key),
        );
      }
    }
    return rosterQ.data.roster
      .filter((a) => failing.has(a.id))
      .map((a) => ({ id: a.id, name: a.name, missing: failing.get(a.id) ?? [] }));
  }, [rosterQ.data, graphQ.data, readyQ.data]);

  const findings = useMemo(() => {
    if (!rosterQ.data || !graphQ.data) return [];
    return worldHealth(graphQ.data.nodes, graphQ.data.edges, rosterQ.data.roster).filter(
      (c) => !NPC_HEALTH_CATEGORIES.has(c.key),
    );
  }, [rosterQ.data, graphQ.data]);

  const suggestionItems: AttentionItem[] = [];
  const pending = proposalsQ.data?.proposals.length ?? 0;
  if (pending > 0) {
    suggestionItems.push({
      key: "suggestions",
      icon: <Lightbulb />,
      tone: "warning",
      title: pending === 1 ? t("home.attnSuggestionsOne") : t("home.attnSuggestions", { n: pending }),
      detail: t("home.attnSuggestionsDetail"),
      action: t("home.attnSuggestionsAction"),
      onAction: () => openView("proposals"),
    });
  }

  const worldItems: AttentionItem[] = [];
  if (notReady.length > 0) {
    const named = notReady.slice(0, NAMED_NPCS);
    const rest = notReady.length - named.length;
    worldItems.push({
      key: "npcs",
      icon: <Users />,
      tone: "warning",
      title: notReady.length === 1 ? t("home.attnNpcsOne") : t("home.attnNpcs", { n: notReady.length }),
      detail: (
        <>
          {named.map((npc, i) => (
            <span key={npc.id} className="gx-overview__npc">
              {i > 0 && " · "}
              <button
                type="button"
                className="gx-overview__link"
                aria-label={t("home.attnNpcOpen", { name: npc.name })}
                onClick={() => openAgent(npc.id)}
              >
                {npc.name}
              </button>
              {": "}
              {npc.missing.map((key) => (MISSING[key] ? t(MISSING[key]) : key)).join(", ")}
            </span>
          ))}
          {rest > 0 && ` · ${t("home.attnMore", { n: rest })}`}
        </>
      ),
      action: t("home.attnNpcsAction"),
      onAction: () => openView("cast"),
    });
  }
  const findingCount = findings.reduce((sum, c) => sum + c.findings.length, 0);
  if (findingCount > 0) {
    worldItems.push({
      key: "world",
      icon: <BookOpen />,
      tone: "warning",
      title: findingCount === 1 ? t("home.attnWorldOne") : t("home.attnWorld", { n: findingCount }),
      detail: findings
        .map((c) => t("home.attnWorldCategory", { title: t(c.title), n: c.findings.length }))
        .join(" · "),
      action: t("home.attnWorldAction"),
      onAction: () => openView("knowledge"),
    });
  }

  const setupItems: AttentionItem[] = [];
  const openSetup = () => goScreen("configuration");
  const discordFailed = providersQ.data?.integrationState === "failed";
  if (discordFailed) {
    setupItems.push({
      key: "discord-failed",
      icon: <Settings />,
      tone: "danger",
      title: t("home.attnDiscordFailed"),
      detail: t("home.attnDiscordFailedDetail"),
      action: t("home.attnSetupAction"),
      onAction: openSetup,
    });
  }
  for (const h of healthQ.data?.providers ?? []) {
    if (h.status !== HealthStatus.DEGRADED) continue;
    const component = h.component || h.provider;
    // The rejected Bot token is already reported, in plainer words.
    if (discordFailed && component === "discord") continue;
    if (OPTIONAL_COMPONENTS.has(component) && NOT_SET_UP_CODES.has(h.code)) continue;
    setupItems.push({
      key: `health-${component}-${h.provider}`,
      icon: <Settings />,
      tone: "danger",
      title: t("home.attnSetup", { what: componentLabel(h, t) }),
      // A code this client does not know yet reads as the generic failure in
      // the display language; Setup, where the action leads, shows the detail.
      detail: t(HEALTH_CODE[h.code] ?? "config.healthCheckFailed"),
      action: t("home.attnSetupAction"),
      onAction: openSetup,
    });
  }

  const sources = [
    { key: "suggestions", query: allOf(proposalsQ), items: suggestionItems, error: t("home.attnSuggestionsError") },
    { key: "world", query: allOf(rosterQ, graphQ, readyQ), items: worldItems, error: t("home.attnWorldError") },
    { key: "setup", query: allOf(providersQ, healthQ), items: setupItems, error: t("home.attnSetupError") },
  ];
  const allLoaded = sources.every((s) => s.query.data !== undefined);
  const total = sources.reduce((sum, s) => sum + s.items.length, 0);
  // One loading placeholder for the band, however many of its reads are still
  // out: the first source that is loading shows it.
  const firstLoading = sources.find((s) => s.query.data === undefined && !s.query.isError)?.key;

  return (
    <Section title={t("home.attentionTitle")} className="gx-overview__attention">
      <ul className="gx-overview__attn-list">
        {sources.map((source) =>
          source.query.data !== undefined ? (
            source.items.map((item) => (
              <li key={item.key} className="gx-overview__attn-item" data-tone={item.tone}>
                <span className="gx-overview__attn-icon" aria-hidden="true">
                  {item.icon}
                </span>
                <div className="gx-overview__attn-text">
                  <p className="gx-overview__attn-title">{item.title}</p>
                  {item.detail && <p className="gx-overview__muted">{item.detail}</p>}
                </div>
                <Button variant="secondary" size="sm" onClick={item.onAction}>
                  {item.action}
                </Button>
              </li>
            ))
          ) : (
            // Not loaded: the source's own placeholder or its own failure, with
            // a retry that re-runs only what this source is missing.
            <li key={source.key} className="gx-overview__attn-state">
              <QueryBoundary
                query={source.query}
                errorTitle={source.error}
                errorSize="inline"
                skeleton={source.key === firstLoading ? <Skeleton lines={2} /> : <></>}
              >
                {null}
              </QueryBoundary>
            </li>
          ),
        )}
      </ul>
      {allLoaded && total === 0 && (
        <p className="gx-overview__clear">
          <CircleCheck aria-hidden="true" />
          {t("home.attentionClear")}
        </p>
      )}
    </Section>
  );
}

function componentLabel(h: ProviderHealth, t: (key: MessageKey) => string): string {
  const key = COMPONENT_LABEL[h.component || h.provider];
  return key ? t(key) : h.provider;
}

// ---- Next steps -------------------------------------------------------------

function NextSteps() {
  const { t } = useI18n();
  const { openView } = useCampaignScreen();
  const { goScreen } = useScreenNav();

  const providersQ = useQuery(ProviderService.method.listProviderConfigs, {});
  const rosterQ = useQuery(CampaignService.method.getCampaignRoster, {}, { retry: rosterRetry });
  const graphQ = useQuery(CampaignService.method.getKnowledgeGraph, {});
  const charactersQ = useQuery(CampaignService.method.listCharacters, {});
  const mapsQ = useQuery(CampaignService.method.listMaps, {});
  const sessionsQ = useQuery(SessionService.method.listSessions, {});

  const queries = [providersQ, rosterQ, graphQ, charactersQ, mapsQ, sessionsQ];
  // Wait until every read has answered, one way or the other: a checklist that
  // appears step by step, or shows for a moment on a campaign that needs none,
  // reads as the page changing its mind. It waits for that first answer only: a
  // retry of a failed read puts it back to pending (it has no data), and the
  // checklist must not vanish while that runs — the step stays left out.
  const [answered, setAnswered] = useState(false);
  if (!answered && queries.every((q) => !q.isPending)) setAnswered(true);
  if (!answered && queries.some((q) => q.isPending)) return null;

  const steps = deriveNextSteps({
    providers: providersQ.data,
    roster: rosterQ.data?.roster,
    graph: graphQ.data,
    characters: charactersQ.data,
    maps: mapsQ.data,
    sessions: sessionsQ.data,
  });
  // Shown only while something is known to be open. A step whose read failed
  // is left out: the block that owns that read reports the failure.
  if (unmetSteps(steps).length === 0) return null;
  const known = steps.filter((s) => s.done !== undefined);
  const done = known.filter((s) => s.done).length;

  const go = (step: NextStep) => {
    if ("view" in step.target) openView(step.target.view);
    else goScreen(step.target.screen);
  };

  return (
    <Section
      title={t("home.nextTitle")}
      className="gx-overview__next"
      aside={
        <span className="gx-overview__progress">
          {t("home.nextProgress", { done, total: known.length })}
        </span>
      }
    >
      <p className="gx-overview__muted">{t("home.nextLede")}</p>
      <ol className="gx-overview__steps">
        {known.map((step) =>
          step.done ? (
            <li key={step.key} className="gx-overview__step" data-done="true">
              <span className="gx-overview__step-mark" aria-hidden="true">
                <Check />
              </span>
              <span className="gx-overview__step-text">
                <span className="gx-overview__step-label">{t(step.label)}</span>
              </span>
              <span className="gx-visually-hidden">{t("home.stepDone")}</span>
            </li>
          ) : (
            <li key={step.key}>
              <button type="button" className="gx-overview__step" onClick={() => go(step)}>
                <span className="gx-overview__step-mark" aria-hidden="true">
                  <Circle />
                </span>
                <span className="gx-overview__step-text">
                  <span className="gx-overview__step-label">{t(step.label)}</span>
                  <span className="gx-overview__step-hint">{t(step.hint)}</span>
                </span>
                <ChevronRight className="gx-overview__chevron" aria-hidden="true" />
              </button>
            </li>
          ),
        )}
      </ol>
    </Section>
  );
}

// ---- Stat tiles -------------------------------------------------------------

type Tile = {
  view: CampaignView;
  /** The figure; undefined while its read is out or after it failed. */
  count: number | undefined;
  loading: boolean;
  unitOne: MessageKey;
  unitMany: MessageKey;
};

function StatTiles({ campaignId }: { campaignId: string | undefined }) {
  const { t } = useI18n();
  const { openView } = useCampaignScreen();

  const rosterQ = useQuery(CampaignService.method.getCampaignRoster, {}, { retry: rosterRetry });
  const graphQ = useQuery(CampaignService.method.getKnowledgeGraph, {});
  const mapsQ = useQuery(CampaignService.method.listMaps, {});
  const charactersQ = useQuery(CampaignService.method.listCharacters, {});
  const proposalsQ = useQuery(
    CampaignService.method.listKnowledgeProposals,
    {},
    { retry: retryUnlessNotFound },
  );
  // Planning threads are read by campaign id (ADR-0062), so this one waits for
  // the roster read to name the campaign.
  const threadsQ = useQuery(
    ChatService.method.listPlanningThreads,
    { campaignId: campaignId ?? "" },
    { enabled: campaignId !== undefined },
  );

  // A disabled read is "pending" without being on its way; it still counts as
  // loading here, because it starts the moment the campaign is known.
  const loading = (q: { data: unknown; isError: boolean }) => q.data === undefined && !q.isError;
  const tiles: Tile[] = [
    {
      view: "cast",
      count: rosterQ.data?.roster.filter((a) => a.role === "character").length,
      loading: loading(rosterQ),
      unitOne: "home.tileNpcOne",
      unitMany: "home.tileNpcs",
    },
    {
      view: "knowledge",
      count: graphQ.data?.nodes.length,
      loading: loading(graphQ),
      unitOne: "home.tileEntryOne",
      unitMany: "home.tileEntries",
    },
    {
      view: "maps",
      count: mapsQ.data?.maps.length,
      loading: loading(mapsQ),
      unitOne: "home.tileMapOne",
      unitMany: "home.tileMaps",
    },
    {
      view: "players",
      count: charactersQ.data?.characters.length,
      loading: loading(charactersQ),
      unitOne: "home.tilePlayerOne",
      unitMany: "home.tilePlayers",
    },
    {
      view: "proposals",
      count: proposalsQ.data?.proposals.length,
      loading: loading(proposalsQ),
      unitOne: "home.tilePending",
      unitMany: "home.tilePending",
    },
    {
      view: "planning",
      count: threadsQ.data?.threads.length,
      // Waiting for a campaign id that a failed roster read will not deliver
      // is not loading: the tile shows no figure, like the Cast tile beside it.
      loading: loading(threadsQ) && !(campaignId === undefined && rosterQ.isError),
      unitOne: "home.tileThreadOne",
      unitMany: "home.tileThreads",
    },
  ];

  // The reads only the tiles make. A failure of one of the others (roster,
  // wiki, suggestions) is reported, with its retry, by the attention band; the
  // tile just shows no figure.
  const own = allOf(mapsQ, charactersQ, ...(campaignId !== undefined ? [threadsQ] : []));

  return (
    <div className="gx-overview__stats">
      <ul className="gx-overview__tiles" aria-label={t("shell.overviewSections")}>
        {tiles.map((tile) => {
          const destination = campaignDestination(tile.view);
          const Icon = destination.icon;
          const label = t(destination.label);
          const unit = t(tile.count === 1 ? tile.unitOne : tile.unitMany);
          const name =
            tile.count !== undefined
              ? t("home.tileAria", { label, count: tile.count, unit })
              : tile.loading
                ? t("home.tileLoadingAria", { label })
                : t("home.tileUnknownAria", { label });
          return (
            <li key={tile.view}>
              <button
                type="button"
                className="gx-overview__tile"
                aria-label={name}
                onClick={() => openView(tile.view)}
              >
                <span className="gx-overview__tile-head">
                  <span className="gx-overview__icon" aria-hidden="true">
                    <Icon size={16} />
                  </span>
                  <span className="gx-overview__tile-label">{label}</span>
                </span>
                <span className="gx-overview__tile-figure">
                  {tile.count !== undefined ? (
                    <span className="gx-overview__tile-count">{tile.count}</span>
                  ) : tile.loading ? (
                    <span className="gx-skeleton gx-overview__tile-skel" aria-hidden="true" />
                  ) : (
                    <span className="gx-overview__tile-count" data-unknown="true">
                      –
                    </span>
                  )}
                  <span className="gx-overview__tile-unit">{unit}</span>
                </span>
              </button>
            </li>
          );
        })}
      </ul>
      {own.data === undefined && (
        <QueryBoundary query={own} errorTitle={t("home.tilesError")} errorSize="inline" skeleton={<></>}>
          {null}
        </QueryBoundary>
      )}
    </div>
  );
}

// ---- Last sessions ----------------------------------------------------------

function LastSessions() {
  const { t } = useI18n();
  const { goScreen } = useScreenNav();
  const sessionsQ = useQuery(SessionService.method.listSessions, {});

  return (
    <Section
      title={t("home.sessionsTitle")}
      className="gx-overview__sessions"
      aside={
        <Button variant="ghost" size="sm" iconEnd={<ArrowRight />} onClick={() => goScreen("session")}>
          {t("home.sessionsAll")}
        </Button>
      }
    >
      <QueryBoundary
        query={sessionsQ}
        errorTitle={t("home.sessionsError")}
        errorSize="inline"
        skeleton={<Skeleton.Row count={LAST_SESSIONS} avatar={false} />}
      >
        {(data) => {
          // The list is newest-first and includes a session that is still
          // running; that one is the live card's, not history.
          const past = data.sessions.filter((s) => s.status !== "running").slice(0, LAST_SESSIONS);
          if (past.length === 0) {
            return (
              <EmptyState
                size="inline"
                icon={<ScrollText size={16} />}
                title={t("home.sessionsEmptyTitle")}
                description={t("home.sessionsEmptyDesc")}
              />
            );
          }
          return (
            <ul className="gx-overview__session-list">
              {past.map((s) => (
                <li key={s.id}>
                  <SessionRow session={s} onOpen={() => goScreen("session", { session: s.id })} />
                </li>
              ))}
            </ul>
          );
        }}
      </QueryBoundary>
    </Section>
  );
}

function SessionRow({ session, onOpen }: { session: VoiceSession; onOpen: () => void }) {
  const { t, lang } = useI18n();
  const startedMs = session.startedAt ? Number(timestampMs(session.startedAt)) : null;
  const endedMs = session.endedAt ? Number(timestampMs(session.endedAt)) : null;

  const when = startedMs != null ? formatWhen(new Date(startedMs), lang) : "—";
  let duration = t("home.sessionNoDuration");
  if (startedMs != null && endedMs != null) {
    const minutes = Math.max(0, Math.round((endedMs - startedMs) / 60_000));
    duration = t("session.durationHm", { h: Math.floor(minutes / 60), m: minutes % 60 });
  }
  const lines =
    session.lineCount === 1 ? t("home.sessionLinesOne") : t("home.sessionLines", { n: session.lineCount });
  // "failed" is the session breaking (#123). An "ended" row that carries an
  // end_reason was stopped by the system, not by the GM: idle, a resource or
  // spend ceiling, or a restart that found it orphaned.
  const failed = session.status === "failed";
  const autoEnded = !failed && session.endReason !== "";

  return (
    <button
      type="button"
      className="gx-overview__session"
      // The name leads with the action; the visible cells follow as content.
      aria-label={[
        t("home.sessionOpen", { when }),
        duration,
        lines,
        failed ? t("home.sessionFailed") : autoEnded ? t("home.sessionAutoEnded") : "",
      ]
        .filter(Boolean)
        .join(", ")}
      onClick={onOpen}
    >
      <span className="gx-overview__session-when">{when}</span>
      <span className="gx-overview__session-meta">
        <span>{duration}</span>
        <span>{lines}</span>
      </span>
      {failed ? (
        <Badge variant="danger" size="sm">
          <TriangleAlert size={10} aria-hidden="true" /> {t("home.sessionFailed")}
        </Badge>
      ) : autoEnded ? (
        <Badge variant="warning" size="sm">
          {t("home.sessionAutoEnded")}
        </Badge>
      ) : null}
      <ChevronRight className="gx-overview__chevron" aria-hidden="true" />
    </button>
  );
}

// A session's start, in the display language. A session from another year
// carries the year: without it two sessions a year apart read the same.
function formatWhen(d: Date, lang: Lang): string {
  return d.toLocaleString(lang, {
    weekday: "short",
    year: d.getFullYear() === new Date().getFullYear() ? undefined : "numeric",
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });
}
