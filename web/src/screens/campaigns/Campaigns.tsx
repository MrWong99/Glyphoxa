import { useId, useMemo, useState } from "react";
import { useQuery } from "@connectrpc/connect-query";
import { timestampDate, timestampMs, type Timestamp } from "@bufbuild/protobuf/wkt";
import { ChevronDown, ChevronRight, Plus, Search, SearchX, Swords } from "lucide-react";

import { CampaignService, type Campaign } from "@gen/glyphoxa/management/v1/management_pb";
import { useScreenNav } from "@/app/screenNav";
import { CampaignRowActions } from "@/components/CampaignRowActions";
import { ImportCampaignButton } from "@/components/ImportCampaignButton";
import { NewCampaignDialog } from "@/components/NewCampaignDialog";
import { useCampaignLifecycle, type CampaignLifecycle } from "@/components/useCampaignLifecycle";
import { useCampaignSwitch } from "@/components/useCampaignSwitch";
import { Avatar } from "@/components/ui/Avatar";
import { Badge } from "@/components/ui/Badge";
import { Button } from "@/components/ui/Button";
import { EmptyState } from "@/components/ui/EmptyState";
import { Input } from "@/components/ui/Input";
import { Page, PageHeader } from "@/components/ui/Page";
import { QueryBoundary } from "@/components/ui/QueryBoundary";
import { SegmentedControl } from "@/components/ui/SegmentedControl";
import { Skeleton } from "@/components/ui/Skeleton";
import { useI18n, type Lang } from "@/i18n";
import { useLiveSession } from "@/lib/useLiveSession";

import "./campaigns.css";

// The Campaigns screen (/t/:tenantSlug/campaigns, ADR-0063 amendment): the home
// of campaign management. One card per campaign — the ones in use, and the
// archived ones in a section of their own — with everything that can be done
// with a campaign on the card: Open, and a menu with Settings, Export,
// Duplicate, Archive / Unarchive and Delete. New campaign and Import bundle are
// the page's own actions.
//
// Until now all of this lived in the topbar switcher's 300px popover, half of
// it behind a "Show archived" toggle.
//
// Open makes the campaign the Active Campaign through the guarded switch
// (useCampaignSwitch: asks before unsaved edits are lost, sweeps the caches,
// says what happened) and then lands on that campaign's Overview.
//
// What a card does NOT show: how many NPCs, wiki entries or sessions a campaign
// has, or when it was last played. ListCampaigns returns the campaign rows
// only; those numbers need a summary read the server does not have yet.

type Sort = "name" | "edited";

/** Search and sort appear once there are more campaigns than fit one glance. */
const TOOLBAR_FROM = 7;

const ms = (ts: Timestamp | undefined) => (ts ? timestampMs(ts) : 0);

function formatDate(ts: Timestamp | undefined, lang: Lang): string | null {
  if (!ts) return null;
  return timestampDate(ts).toLocaleDateString(lang, { dateStyle: "medium" });
}

// The Campaign Language as a name in the display language ("German"), falling
// back to the stored tag when the runtime cannot name it.
function languageName(code: string, lang: Lang): string {
  if (code === "") return "";
  try {
    return new Intl.DisplayNames([lang], { type: "language" }).of(code) ?? code;
  } catch {
    return code;
  }
}

export function Campaigns() {
  const { t, lang } = useI18n();
  const nav = useScreenNav();
  // ONE read for both sections; archived campaigns are told apart by
  // archivedAt. (The topbar switcher reads the non-archived list under its own
  // cache key; every change here invalidates both.)
  const listQ = useQuery(CampaignService.method.listCampaigns, { includeArchived: true });
  // retry:false like every other observer of this shared entry.
  const activeQ = useQuery(CampaignService.method.getActiveCampaign, {}, { retry: false });
  const activeId = activeQ.data?.campaign?.id;
  // Resolution is live-first (#222): while a Voice Session runs, the Active
  // Campaign IS the session's campaign — so "live" belongs on that card.
  const { live } = useLiveSession();

  const [creating, setCreating] = useState(false);
  const [query, setQuery] = useState("");
  const [sort, setSort] = useState<Sort>("name");
  const [archivedOpen, setArchivedOpen] = useState(false);

  const campaignSwitch = useCampaignSwitch();

  const open = (campaign: { id: string; name: string }) => {
    if (campaign.id === activeId) {
      nav.goCampaignView("overview");
      return;
    }
    campaignSwitch.switchTo(campaign, {
      // A switch deferred by a live Voice Session has not happened yet: the
      // Overview would still show the session's campaign, so stay here and let
      // the toast explain.
      onSwitched: ({ deferred }) => {
        if (!deferred) nav.goCampaignView("overview");
      },
    });
  };

  const all = useMemo(() => listQ.data?.campaigns ?? [], [listQ.data]);
  const inUse = useMemo(() => all.filter((c) => c.archivedAt === undefined), [all]);
  const archived = useMemo(() => all.filter((c) => c.archivedAt !== undefined), [all]);

  const lifecycle = useCampaignLifecycle({
    activeId,
    campaigns: listQ.data ? inUse : undefined,
    onOpen: open,
  });

  const needle = query.trim().toLowerCase();
  const shown = (list: Campaign[]) => {
    const matching =
      needle === ""
        ? list
        : list.filter(
            (c) => c.name.toLowerCase().includes(needle) || c.system.toLowerCase().includes(needle),
          );
    return [...matching].sort((a, b) =>
      sort === "edited"
        ? ms(b.updatedAt) - ms(a.updatedAt) || a.name.localeCompare(b.name, lang)
        : a.name.localeCompare(b.name, lang) || (a.id < b.id ? -1 : 1),
    );
  };
  const shownInUse = shown(inUse);
  const shownArchived = shown(archived);
  // A search that only matches archived campaigns must not look like "nothing
  // found": the section opens for as long as the search has hits in it.
  const archivedExpanded = archivedOpen || (needle !== "" && shownArchived.length > 0);
  const archivedListId = useId();

  const card = (c: Campaign) => (
    <CampaignCard
      key={c.id}
      campaign={c}
      isActive={c.id === activeId}
      isLive={live && c.id === activeId}
      lifecycle={lifecycle}
      lang={lang}
      switching={campaignSwitch.pending}
      onOpen={() => open({ id: c.id, name: c.name })}
    />
  );

  const newCampaignButton = (
    <Button variant="primary" iconStart={<Plus />} onClick={() => setCreating(true)}>
      {t("components.newCampaign")}
    </Button>
  );

  return (
    <Page className="gx-campaigns">
      <PageHeader
        title={t("shell.titleCampaigns")}
        description={t("shell.campaignsLede")}
        actions={
          <>
            <ImportCampaignButton />
            {newCampaignButton}
          </>
        }
      />

      <QueryBoundary
        query={listQ}
        errorTitle={t("campaigns.loadErrorTitle")}
        skeleton={
          <Skeleton className="gx-campaigns__grid">
            <Skeleton.Block height={184} />
            <Skeleton.Block height={184} />
            <Skeleton.Block height={184} />
          </Skeleton>
        }
        isEmpty={(data) => data.campaigns.length === 0}
        empty={
          <EmptyState
            icon={<Swords size={22} />}
            titleAs="h2"
            title={t("campaigns.emptyTitle")}
            description={t("campaigns.emptyDesc")}
            action={newCampaignButton}
          />
        }
      >
        {all.length >= TOOLBAR_FROM && (
          <div className="gx-campaigns__toolbar">
            <div className="gx-campaigns__search" role="search">
              <Input
                type="search"
                aria-label={t("campaigns.searchLabel")}
                placeholder={t("campaigns.searchPlaceholder")}
                icon={<Search />}
                value={query}
                onChange={(e) => setQuery(e.target.value)}
              />
            </div>
            <SegmentedControl<Sort>
              label={t("campaigns.sortLabel")}
              size="sm"
              value={sort}
              onChange={setSort}
              options={[
                { value: "name", label: t("campaigns.sortName") },
                { value: "edited", label: t("campaigns.sortEdited") },
              ]}
            />
          </div>
        )}

        {needle !== "" && shownInUse.length === 0 && shownArchived.length === 0 ? (
          <EmptyState
            icon={<SearchX size={22} />}
            title={t("campaigns.noMatchesTitle", { query: query.trim() })}
            description={t("campaigns.noMatchesDesc")}
            action={
              <Button variant="secondary" onClick={() => setQuery("")}>
                {t("campaigns.clearSearch")}
              </Button>
            }
          />
        ) : (
          <>
            {inUse.length === 0 && (
              // Campaigns exist, but none is in use: say so instead of an empty
              // gap above the archived section.
              <EmptyState
                icon={<Swords size={22} />}
                titleAs="h2"
                title={t("campaigns.allArchivedTitle")}
                description={t("campaigns.allArchivedDesc")}
              />
            )}
            {shownInUse.length > 0 && (
              <ul className="gx-campaigns__grid" aria-label={t("shell.campaignsList")}>
                {shownInUse.map(card)}
              </ul>
            )}

            {archived.length > 0 && (
              <section className="gx-campaigns__archived">
                <button
                  type="button"
                  className="gx-campaigns__archived-toggle"
                  aria-expanded={archivedExpanded}
                  aria-controls={archivedListId}
                  onClick={() => setArchivedOpen(!archivedExpanded)}
                >
                  {archivedExpanded ? <ChevronDown size={16} aria-hidden /> : <ChevronRight size={16} aria-hidden />}
                  {t("campaigns.archivedSection", {
                    count: needle === "" ? archived.length : shownArchived.length,
                  })}
                </button>
                {archivedExpanded && (
                  <ul
                    id={archivedListId}
                    className="gx-campaigns__grid"
                    aria-label={t("campaigns.archivedList")}
                  >
                    {shownArchived.map(card)}
                  </ul>
                )}
              </section>
            )}
          </>
        )}
      </QueryBoundary>

      <NewCampaignDialog open={creating} onOpenChange={setCreating} />
      {lifecycle.dialogs}
      {campaignSwitch.dialog}
    </Page>
  );
}

function CampaignCard({
  campaign,
  isActive,
  isLive,
  lifecycle,
  lang,
  switching,
  onOpen,
}: {
  campaign: Campaign;
  isActive: boolean;
  isLive: boolean;
  lifecycle: CampaignLifecycle;
  lang: Lang;
  switching: boolean;
  onOpen: () => void;
}) {
  const { t } = useI18n();
  const headingId = useId();
  const isArchived = campaign.archivedAt !== undefined;
  const busy = lifecycle.busy.has(campaign.id);
  const created = formatDate(campaign.createdAt, lang);
  // updated_at is the campaign ROW's modification time (a rename, a settings
  // change, archiving) — not when it was last played. It is labelled "Edited"
  // and never anything that suggests play.
  const edited = formatDate(campaign.updatedAt, lang);
  const archivedOn = formatDate(campaign.archivedAt, lang);
  const language = languageName(campaign.language, lang);

  return (
    <li
      className="gx-campaign-card"
      data-active={isActive || undefined}
      data-archived={isArchived || undefined}
      aria-busy={busy || undefined}
    >
      <article className="gx-campaign-card__body" aria-labelledby={headingId}>
        <div className="gx-campaign-card__head">
          {/* A monogram, not a picture: decorative beside the name it is made
              from. */}
          <Avatar name={campaign.name} shape="rounded" size="lg" aria-hidden="true" />
          <div className="gx-campaign-card__titles">
            <h2 id={headingId} className="gx-campaign-card__name">
              {campaign.name}
            </h2>
            {(isLive || isActive || isArchived || busy) && (
              <div className="gx-campaign-card__badges">
                {isLive && (
                  <Badge variant="live" size="sm" dot pulse>
                    {t("shell.live")}
                  </Badge>
                )}
                {isActive && (
                  <Badge variant="arcane" size="sm">
                    {t("shell.campaignActive")}
                  </Badge>
                )}
                {isArchived && (
                  <Badge variant="neutral" size="sm">
                    {t("components.archivedBadge")}
                  </Badge>
                )}
                {busy && (
                  <Badge variant="neutral" size="sm" role="status">
                    {t("campaigns.working")}
                  </Badge>
                )}
              </div>
            )}
          </div>
          <CampaignRowActions campaign={campaign} lifecycle={lifecycle} isActive={isActive} />
        </div>

        <dl className="gx-campaign-card__meta">
          <div>
            <dt>{t("campaigns.metaSystem")}</dt>
            <dd data-empty={campaign.system === "" || undefined}>
              {campaign.system || t("campaigns.metaNone")}
            </dd>
          </div>
          <div>
            <dt>{t("campaigns.metaLanguage")}</dt>
            <dd data-empty={language === "" || undefined}>{language || t("campaigns.metaNone")}</dd>
          </div>
          {created && (
            <div>
              <dt>{t("campaigns.metaCreated")}</dt>
              <dd>{created}</dd>
            </div>
          )}
          {isArchived && archivedOn ? (
            <div>
              <dt>{t("campaigns.metaArchived")}</dt>
              <dd>{archivedOn}</dd>
            </div>
          ) : (
            edited && (
              <div>
                <dt>{t("campaigns.metaEdited")}</dt>
                <dd>{edited}</dd>
              </div>
            )
          )}
        </dl>

        <div className="gx-campaign-card__actions">
          {isArchived ? (
            // An archived campaign cannot be the Active Campaign (#269), so
            // there is nothing to open: the way back in is to unarchive it.
            <Button
              variant="secondary"
              size="sm"
              aria-label={t("campaigns.unarchiveNamed", { name: campaign.name })}
              aria-disabled={busy || undefined}
              onClick={() => {
                if (!busy) lifecycle.unarchive(campaign);
              }}
            >
              {t("components.unarchive")}
            </Button>
          ) : (
            <Button
              variant={isActive ? "primary" : "secondary"}
              size="sm"
              // The visible label is just "Open"; the accessible name says
              // which campaign, or a screen reader hears a column of "Open".
              aria-label={t("shell.campaignOpenNamed", { name: campaign.name })}
              // Not `disabled` while a switch is being written: a button that
              // turns disabled under the keyboard drops focus to <body>, and a
              // switch deferred by a live session leaves the GM on this page.
              aria-disabled={switching || undefined}
              onClick={() => {
                if (!switching) onOpen();
              }}
            >
              {t("shell.campaignOpen")}
            </Button>
          )}
        </div>
      </article>
    </li>
  );
}
