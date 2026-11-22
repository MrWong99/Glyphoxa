import { Check, CircleCheck, Plus, Scissors, TriangleAlert, Users, X } from "lucide-react";

import type { Agent } from "@gen/glyphoxa/management/v1/management_pb";
import { Button } from "@/components/ui/Button";
import { EmptyState } from "@/components/ui/EmptyState";
import { QueryBoundary, type QueryLike } from "@/components/ui/QueryBoundary";
import { SegmentedControl } from "@/components/ui/SegmentedControl";
import { Skeleton } from "@/components/ui/Skeleton";
import { Tooltip } from "@/components/ui/Tooltip";
import { useI18n } from "@/i18n";
import { useCampaignPanelState } from "./campaignContext";
import { useCastReadiness } from "./castReadiness";
import {
  UNPLACED_TITLE,
  type Check as ReadinessCheck,
  type Readiness,
  type RosterEntry,
} from "./rosterPrep";

import "./cast.css";

// The Readiness view (#544). The roster was a flat list of Agents; this shows
// what a GM needs BEFORE a session: whether each NPC can actually say anything,
// and where it sits in the world.
//
// The failure it exists for: an NPC created through the normal flow has a voice, a
// persona, and an entry that is empty (ADR-0008 second amendment auto-creates the
// Node without copying the Persona). Every signal below was already queryable and
// none of it was shown, so that state was found live, at the table.
//
// Two ways to read it:
//
//   Needs attention   a flat to-do list: each NPC that is not ready, ONCE, with
//                     only the checks that fail. The view opens here whenever
//                     somebody is not ready.
//   All               every roster member under the faction or place it
//                     belongs to (an NPC in a guild AND a town is listed under
//                     both), with every check, done or not.
//
// The headline counts NPCs, not rows: unique Character NPCs, the Butler exempt.

type PrepMode = "attention" | "all";

export function RosterPrep({
  roster,
  onSelectAgent,
  onOpenNode,
  onOpenKnowledge,
  onAddNpc,
}: {
  roster: Agent[];
  onSelectAgent: (id: string) => void;
  /** Opens ONE entry in the World wiki. */
  onOpenNode: (id: string) => void;
  /** Opens the World wiki, where the "Voiced by" link control lives. */
  onOpenKnowledge: () => void;
  /** Starts the New NPC dialog (the empty view's way forward). */
  onAddNpc?: () => void;
}) {
  const { t, lang } = useI18n();
  // Two reads on top of the roster the screen already has — one graph, one batch
  // readiness. Not a per-NPC RPC storm on a screen opened before every session.
  const { graphQuery, readyQuery, worldKnown, groups, summary } = useCastReadiness(roster);

  // null = "wherever the work is": the to-do list while anybody is not ready,
  // the full roster once everyone is. A choice the GM makes sticks (and comes
  // back with them after a trip to the wiki to fix something).
  const [chosenMode, setMode] = useCampaignPanelState<PrepMode | null>("cast.prepMode", null);
  const mode: PrepMode = chosenMode ?? (summary.attention.length > 0 ? "attention" : "all");

  // Both reads must land before grading: showing "0 facts in reach" for every NPC
  // while the readiness read is still in flight would be a screen full of false
  // alarms on the exact screen the GM consults to trust the roster. They are one
  // load to the GM, so they are one boundary: one skeleton, one error, one retry.
  const world: QueryLike<true> = {
    data: worldKnown ? true : undefined,
    error: graphQuery.error ?? readyQuery.error,
    isPending: graphQuery.isPending || readyQuery.isPending,
    isError: graphQuery.isError || readyQuery.isError,
    isFetching: graphQuery.isFetching || readyQuery.isFetching,
    fetchStatus:
      graphQuery.fetchStatus === "fetching" || readyQuery.fetchStatus === "fetching"
        ? "fetching"
        : graphQuery.fetchStatus === "paused" || readyQuery.fetchStatus === "paused"
          ? "paused"
          : "idle",
    refetch: () => {
      if (graphQuery.data === undefined) void graphQuery.refetch();
      if (readyQuery.data === undefined) void readyQuery.refetch();
    },
  };

  // The "last spoke" prep signal is context, not a check. Rendered here (not in
  // the pure helper) because the date format and copy follow the display
  // language, which only the React layer knows.
  const spokeLabel = (r?: Readiness) =>
    r?.lastSpokeAt
      ? t("campaign.prepLastSpoke", { date: r.lastSpokeAt.toLocaleDateString(lang) })
      : t("campaign.prepNeverSpoke");

  const row = (e: RosterEntry, key: string, onlyFailing: boolean) => (
    <PrepRow
      key={key}
      entry={e}
      onlyFailing={onlyFailing}
      spoke={spokeLabel(e.readiness)}
      onSelectAgent={onSelectAgent}
      onOpenNode={onOpenNode}
      onOpenKnowledge={onOpenKnowledge}
    />
  );

  return (
    <QueryBoundary
      query={world}
      errorTitle={t("cast.prepLoadErrorTitle")}
      skeleton={<Skeleton.Row count={4} avatar={false} />}
    >
      {summary.total === 0 ? (
        <EmptyState
          icon={<Users size={22} />}
          title={t("cast.prepEmptyTitle")}
          description={t("cast.prepEmptyDesc")}
          action={
            onAddNpc && (
              <Button iconStart={<Plus size={15} />} onClick={onAddNpc}>
                {t("campaign.addNpc")}
              </Button>
            )
          }
        />
      ) : (
        <section className="gx-prep" aria-label={t("campaign.prepAria")}>
          <div className="gx-cast-prep__head">
            <p className="gx-cast-prep__count" role="status">
              {summary.total === 1
                ? t("cast.prepReadyCountOne", { ready: summary.ready })
                : t("cast.prepReadyCount", { ready: summary.ready, total: summary.total })}
            </p>
            <SegmentedControl
              size="sm"
              label={t("cast.prepModeAria")}
              value={mode}
              onChange={setMode}
              options={[
                {
                  value: "attention",
                  label: t("cast.prepModeAttention"),
                  count: summary.attention.length > 0 ? summary.attention.length : undefined,
                },
                { value: "all", label: t("cast.prepModeAll") },
              ]}
            />
          </div>

          {mode === "attention" ? (
            summary.attention.length === 0 ? (
              <EmptyState
                size="inline"
                icon={<CircleCheck size={16} />}
                title={t("cast.prepAllReadyTitle")}
                description={t("cast.prepAllReadyDesc")}
                action={
                  <Button variant="secondary" size="sm" onClick={() => setMode("all")}>
                    {t("cast.prepShowAll")}
                  </Button>
                }
              />
            ) : (
              <div className="gx-prep__group" role="group" aria-label={t("cast.prepAttentionAria")}>
                {summary.attention.map((e) => row(e, e.agent.id, true))}
              </div>
            )
          ) : (
            groups.map((g) => {
              // Named groups carry a Node name (data); only the unplaced catch-all is
              // ours to translate — the helper hands back its sentinel key.
              const title = g.title === UNPLACED_TITLE ? t("campaign.prepUnplacedTitle") : g.title;
              return (
                <section key={g.key} className="gx-prep__group" aria-label={title}>
                  <h4 className="gx-prep__title">{title}</h4>
                  {g.entries.map((e) => row(e, `${g.key}-${e.agent.id}`, false))}
                </section>
              );
            })
          )}
        </section>
      )}
    </QueryBoundary>
  );
}

// PrepRow is one roster member's line: a ready/not-ready sign, the name (which
// opens the editor), the checks, and when the NPC last spoke.
function PrepRow({
  entry: e,
  onlyFailing,
  spoke,
  onSelectAgent,
  onOpenNode,
  onOpenKnowledge,
}: {
  entry: RosterEntry;
  /** List only the checks that fail (the to-do list). */
  onlyFailing: boolean;
  spoke: string;
  onSelectAgent: (id: string) => void;
  onOpenNode: (id: string) => void;
  onOpenKnowledge: () => void;
}) {
  const { t } = useI18n();
  const checks = onlyFailing ? e.checks.filter((c) => !c.ok) : e.checks;
  const trimmed = e.readiness?.truncated ? e.readiness : null;

  // Each fix goes where it can ACTUALLY be made: personality and voice in the
  // Agent editor; everything about the entry in the World wiki, ON the entry
  // when there is one to open. An NPC with no linked entry opens the unlinked
  // entry that carries its name (its relations card holds the "Voiced by"
  // control) and only falls back to the bare wiki when no such entry exists.
  const fix = (c: ReadinessCheck) => {
    if (c.fix === "agent") onSelectAgent(e.agent.id);
    else if (c.fix === "entry" && e.node) onOpenNode(e.node.id);
    else if (e.linkCandidate) onOpenNode(e.linkCandidate.id);
    else onOpenKnowledge();
  };

  return (
    <div className="gx-prep__row" data-ready={e.ready ? "true" : "false"}>
      <span className="gx-cast-prep__sign" aria-hidden="true">
        {e.ready ? <CircleCheck size={15} /> : <TriangleAlert size={15} />}
      </span>
      <button type="button" className="gx-prep__name" onClick={() => onSelectAgent(e.agent.id)}>
        {e.agent.name}
      </button>
      {e.exempt ? (
        // Listed, not graded — see rosterPrep's Butler note.
        <span className="gx-prep__exempt">{t("campaign.prepButlerExempt")}</span>
      ) : (
        <>
          <div className="gx-prep__checks">
            {checks.map((c) => (
              <ReadinessMark key={c.key} check={c} agentName={e.agent.name} onFix={() => fix(c)} />
            ))}
            {trimmed && (
              // Not a failing check (the NPC still speaks), but the GM should
              // know its knowledge was cut to fit: the figures are the budget
              // the readiness read already carries.
              <Tooltip content={t("cast.trimmedHint")}>
                <button
                  type="button"
                  className="gx-prep__mark gx-cast-prep__trimmed"
                  aria-label={`${e.agent.name}: ${t("cast.trimmed", { chars: trimmed.chars, maxChars: trimmed.maxChars })}`}
                  onClick={() => (e.node ? onOpenNode(e.node.id) : onOpenKnowledge())}
                >
                  <Scissors size={12} aria-hidden />{" "}
                  {t("cast.trimmed", { chars: trimmed.chars, maxChars: trimmed.maxChars })}
                </button>
              </Tooltip>
            )}
          </div>
          <span className="gx-prep__spoke">{spoke}</span>
        </>
      )}
    </div>
  );
}

// ReadinessMark is one satisfied/unsatisfied mark. An UNSATISFIED one is a button
// that goes to the thing that fixes it — a checklist you cannot act on from is
// just a nag.
function ReadinessMark({
  check,
  agentName,
  onFix,
}: {
  check: ReadinessCheck;
  agentName: string;
  onFix: () => void;
}) {
  const { t } = useI18n();
  // The helper hands back a message key + params; the string exists only here.
  const label = t(check.labelKey, check.labelParams);
  if (check.ok) {
    return (
      <span
        className="gx-prep__mark"
        data-ok
        aria-label={t("campaign.checkDoneAria", { agent: agentName, check: label })}
      >
        <Check size={12} aria-hidden /> {label}
      </span>
    );
  }
  return (
    <button
      type="button"
      className="gx-prep__mark"
      aria-label={t("campaign.checkMissingAria", { agent: agentName, check: label })}
      onClick={onFix}
    >
      <X size={12} aria-hidden /> {label}
    </button>
  );
}
