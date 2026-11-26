import { useId } from "react";
import { useQuery } from "@connectrpc/connect-query";
import { ArrowLeft, ArrowRight, EyeOff, Link as LinkIcon, Pencil, X } from "lucide-react";

import { CampaignService } from "@gen/glyphoxa/management/v1/management_pb";
import type { Edge, Node } from "@gen/glyphoxa/management/v1/management_pb";
import { useI18n } from "@/i18n";
import { errorMessage } from "@/lib/connectError";
import { Badge } from "@/components/ui/Badge";
import { Button } from "@/components/ui/Button";
import { EmptyState } from "@/components/ui/EmptyState";
import { ErrorState } from "@/components/ui/ErrorState";
import { IconButton } from "@/components/ui/IconButton";
import { Markdown } from "@/components/ui/Markdown";
import { Skeleton } from "@/components/ui/Skeleton";
import { useCampaignScreen } from "./campaignContext";
import { relativeTime, updatedAt } from "./entryFormat";
import { NodeAppearances } from "./NodeAppearances";
import { portraitURL } from "./NodePortrait";
import { DISPOSITION_LABEL, alphaBg, edgeLabel, metaOf } from "./knowledgeVocab";

// The entry page: what the wiki shows when the GM opens an entry.
//
// The wiki used to open straight into a form, so reading an entry at the table
// meant reading it through input boxes, and following a thought from an NPC to
// the town they live in meant closing one form and hunting for the next entry
// in the list. This is the read side: the entry as prose, its facts as a
// definition list with the secret ones marked, and its connections as links to
// the entries they point at. "Edit" swaps the same pane to the editor.
//
// It is read-only on purpose. Nothing here can be changed by accident while
// the GM is mid-scene with one hand on the dice.

export function EntryView({
  node,
  tags,
  onEdit,
  onClose,
  onOpenNode,
  onOpenLine,
}: {
  node: Node;
  /** The entry's free-form tags, already resolved by the panel. */
  tags: readonly string[];
  onEdit: () => void;
  onClose: () => void;
  /** Open another entry (a connection's name). */
  onOpenNode: (nodeID: string) => void;
  /** Jump to one Transcript Line on the Session screen (mentions, #545). */
  onOpenLine?: (sessionID: string, lineID: string) => void;
}) {
  const { t, lang } = useI18n();
  const { roster, openAgent } = useCampaignScreen();
  const meta = metaOf(node.nodeType);
  const titleId = useId();
  const factsId = useId();
  const tagsId = useId();

  const voicedBy = node.agentId !== "" ? roster.find((a) => a.id === node.agentId) : undefined;
  const changed = updatedAt(node);
  const hasProse = node.body.trim() !== "";
  const privateFacts = node.aspects.filter((a) => a.gmPrivate).length;

  return (
    <article className="gx-wiki-pane gx-wiki-view" aria-labelledby={titleId} data-testid="entry-view">
      <header className="gx-wiki-pane__header">
        <div className="gx-wiki-view__identity">
          {node.hasPortrait ? (
            <img
              className="gx-wiki-view__portrait"
              src={portraitURL(node)}
              alt={t("knowledge.portraitAlt", { name: node.name })}
            />
          ) : (
            <span
              className="gx-wiki-view__icon"
              style={{ color: meta.color, background: alphaBg(meta.color) }}
              aria-hidden
            >
              <meta.Icon size={22} />
            </span>
          )}
          <div className="gx-wiki-pane__heading">
            <h2 className="gx-wiki-pane__title" id={titleId}>
              {node.name}
            </h2>
            <div className="gx-wiki-view__badges">
              <Badge size="sm" style={{ color: meta.color, background: alphaBg(meta.color) }}>
                {t(meta.labelKey)}
              </Badge>
              {node.gmPrivate && (
                <Badge variant="neutral" size="sm">
                  <EyeOff size={11} aria-hidden /> {t("knowledge.gmOnlyBadge")}
                </Badge>
              )}
              {node.agentId !== "" &&
                (voicedBy ? (
                  // The NPC that speaks for this entry is one click away: its
                  // voice and persona live on Cast, not here.
                  <button
                    type="button"
                    className="gx-wiki-voiced"
                    onClick={() => openAgent(voicedBy.id)}
                  >
                    <LinkIcon size={11} aria-hidden />
                    {t("knowledge.voicedByNamed", { name: voicedBy.name })}
                  </button>
                ) : (
                  <Badge variant="gold" size="sm">
                    <LinkIcon size={11} aria-hidden /> {t("knowledge.voicedBadge")}
                  </Badge>
                ))}
            </div>
          </div>
        </div>
        <div className="gx-wiki-pane__actions">
          <Button variant="secondary" size="sm" iconStart={<Pencil size={14} />} onClick={onEdit}>
            {t("knowledge.editEntry")}
          </Button>
          <IconButton size="sm" label={t("knowledge.closeEntry")} onClick={onClose}>
            <X size={16} />
          </IconButton>
        </div>
      </header>

      <div className="gx-wiki-pane__body">
        {node.gmPrivate && <p className="gx-wiki-view__private-note">{t("knowledge.gmOnlyEntryNote")}</p>}

        {node.aspects.length > 0 && (
          <section className="gx-wiki-section" aria-labelledby={factsId}>
            <h3 className="gx-wiki-section__title" id={factsId}>
              {t("knowledge.factsLabel")}
              {privateFacts > 0 && (
                <span className="gx-wiki-section__note">
                  {privateFacts === 1
                    ? t("knowledge.privateFactsOne")
                    : t("knowledge.privateFactsMany", { n: privateFacts })}
                </span>
              )}
            </h3>
            <dl className="gx-wiki-dl">
              {node.aspects.map((a, i) => (
                <div
                  key={a.id || `${a.key}-${i}`}
                  className="gx-wiki-dl__row"
                  data-private={a.gmPrivate || undefined}
                >
                  <dt>{a.key}</dt>
                  <dd>
                    {a.value}
                    {a.gmPrivate && (
                      <span className="gx-wiki-private">
                        <EyeOff size={11} aria-hidden /> {t("knowledge.visibilityGmOnly")}
                      </span>
                    )}
                  </dd>
                </div>
              ))}
            </dl>
          </section>
        )}

        {hasProse && <Markdown text={node.body} className="gx-wiki-view__prose" />}

        {!hasProse && node.aspects.length === 0 && (
          <EmptyState
            size="inline"
            title={t("knowledge.entryBlankTitle")}
            description={t("knowledge.entryBlankDesc")}
            action={
              <Button variant="secondary" size="sm" onClick={onEdit}>
                {t("knowledge.entryBlankAction")}
              </Button>
            }
          />
        )}

        {tags.length > 0 && (
          <section className="gx-wiki-section" aria-labelledby={tagsId}>
            <h3 className="gx-wiki-section__title" id={tagsId}>
              {t("knowledge.tagsLabel")}
            </h3>
            <ul className="gx-wiki-taglist">
              {tags.map((tag) => (
                <li key={tag} className="gx-wiki-tag">
                  {tag}
                </li>
              ))}
            </ul>
          </section>
        )}

        <EntryConnections node={node} onOpenNode={onOpenNode} />

        <NodeAppearances nodeID={node.id} onOpenLine={onOpenLine} />

        {changed && (
          <p className="gx-wiki-view__stamp">
            {t("knowledge.updatedAgo", { when: relativeTime(changed, lang) })}
          </p>
        )}
      </div>
    </article>
  );
}

// EntryConnections lists the entry's relations as a way to MOVE: every other
// end is a link that opens that entry. Edges are one-way (ADR-0008), so the two
// directions are separate lists and each row reads as the sentence it asserts.
function EntryConnections({ node, onOpenNode }: { node: Node; onOpenNode: (nodeID: string) => void }) {
  const { t } = useI18n();
  const headingId = useId();
  const edges = useQuery(CampaignService.method.listNodeEdges, { nodeId: node.id });
  const outgoing = edges.data?.outgoing ?? [];
  const incoming = edges.data?.incoming ?? [];

  return (
    <section className="gx-wiki-section" aria-labelledby={headingId}>
      <h3 className="gx-wiki-section__title" id={headingId}>
        {edges.data
          ? t("knowledge.connectionsHeading", { n: outgoing.length + incoming.length })
          : t("knowledge.connectionsTitle")}
      </h3>
      {edges.isPending ? (
        <Skeleton lines={2} />
      ) : edges.isError ? (
        <ErrorState
          size="inline"
          title={t("knowledge.connectionsLoadError")}
          description={null}
          message={errorMessage(edges.error)}
          onRetry={() => void edges.refetch()}
          retrying={edges.isFetching}
        />
      ) : outgoing.length + incoming.length === 0 ? (
        <p className="gx-wiki-section__empty">{t("knowledge.noConnections")}</p>
      ) : (
        <>
          {outgoing.length > 0 && (
            <ul className="gx-wiki-links" aria-label={t("knowledge.outgoingConnectionsAria")}>
              {outgoing.map((e) => (
                <ConnectionRow key={e.id} edge={e} direction="out" self={node.name} onOpenNode={onOpenNode} />
              ))}
            </ul>
          )}
          {incoming.length > 0 && (
            <ul className="gx-wiki-links" aria-label={t("knowledge.incomingConnectionsAria")}>
              {incoming.map((e) => (
                <ConnectionRow key={e.id} edge={e} direction="in" self={node.name} onOpenNode={onOpenNode} />
              ))}
            </ul>
          )}
        </>
      )}
    </section>
  );
}

function ConnectionRow({
  edge,
  direction,
  self,
  onOpenNode,
}: {
  edge: Edge;
  direction: "out" | "in";
  /** The name of the entry being read: the end of the relation that is not a link. */
  self: string;
  onOpenNode: (nodeID: string) => void;
}) {
  const { t } = useI18n();
  const out = direction === "out";
  const otherID = out ? edge.toNodeId : edge.fromNodeId;
  const otherName = out ? edge.toNodeName : edge.fromNodeName;
  const meta = metaOf(out ? edge.toNodeType : edge.fromNodeType);
  const feeling = edge.disposition !== 0 ? DISPOSITION_LABEL.get(edge.disposition) : undefined;
  const link = (
    <button type="button" className="gx-wiki-link" onClick={() => onOpenNode(otherID)}>
      {otherName}
    </button>
  );
  const relation = <span className="gx-wiki-links__relation">{edgeLabel(t, edge.edgeType)}</span>;
  return (
    <li className="gx-wiki-links__row">
      {out ? (
        <ArrowRight size={13} className="gx-wiki-links__dir" aria-hidden />
      ) : (
        <ArrowLeft size={13} className="gx-wiki-links__dir" aria-hidden />
      )}
      {/* Outgoing reads "lives in Saltmarsh"; incoming, on Saltmarsh's page,
          reads "Bart lives in Saltmarsh". Neither direction has to be decoded
          from the arrow, and a name needs no grammar in any language. */}
      {out ? (
        <>
          {relation} {link}
        </>
      ) : (
        <>
          {link} {relation} <span className="gx-wiki-links__self">{self}</span>
        </>
      )}
      <Badge size="sm" style={{ color: meta.color, background: alphaBg(meta.color) }}>
        {t(meta.labelKey)}
      </Badge>
      {feeling && <span className="gx-wiki-links__feeling" data-disposition={edge.disposition}>{t(feeling)}</span>}
      {edge.note !== "" && <span className="gx-wiki-links__note">{edge.note}</span>}
    </li>
  );
}
