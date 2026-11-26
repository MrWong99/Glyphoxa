import { useMemo, useState } from "react";
import { Search, Sparkles } from "lucide-react";

import type { Node as PbNode, NodeType } from "@gen/glyphoxa/management/v1/management_pb";
import { Button } from "@/components/ui/Button";
import { EmptyState } from "@/components/ui/EmptyState";
import { Input } from "@/components/ui/Input";
import { useI18n } from "@/i18n";
import { TYPE_ORDER, alphaBg, metaOf } from "./knowledgeVocab";

// The unpinned tray: the campaign's placeable entries that are not on this map
// yet. Placing the world is a gesture, not a form — arm an entry here, then
// click the map (or place it at the centre from the keyboard).
//
// It used to be one wrapping row of chips under the map, which stops working at
// about thirty entries: the entry you want is somewhere in a paragraph of
// chips, below the fold of the very map you are placing it on. So it is a
// panel beside the map on a wide screen, with a search box and a type filter,
// and whatever "Suggest placements" marked floats to the top.
//
// Suggestions (#541) MARK entries already in this tray rather than opening a
// second list — the model narrows what to look at, and the placing gesture is
// untouched, which is what keeps "suggest, never place" true.

export function MapTray({
  unpinned,
  suggested,
  placingID,
  onToggle,
  canSuggest,
  suggesting,
  suggestError,
  onSuggest,
  onLinkEntry,
}: {
  unpinned: PbNode[];
  suggested: ReadonlySet<string>;
  /** The armed entry, if any. */
  placingID: string | null;
  /** `viaKeyboard`: the press came from Enter/Space (a click with no pointer). */
  onToggle: (node: PbNode, viaKeyboard: boolean) => void;
  /** The map depicts a Location entry, so there is prose to suggest from. */
  canSuggest: boolean;
  suggesting: boolean;
  suggestError: string | null;
  onSuggest: () => void;
  /**
   * Opens Map settings, where the "Depicts" entry is chosen. Absent for
   * someone who may place pins but not change the map (ADR-0065): they are
   * neither offered suggestions nor told how to turn them on.
   */
  onLinkEntry?: () => void;
}) {
  const { t } = useI18n();
  const [query, setQuery] = useState("");
  const [type, setType] = useState<NodeType | null>(null);

  // Only the types that actually occur: a chip that filters to nothing is noise.
  const types = useMemo(() => {
    const present = new Set(unpinned.map((n) => n.nodeType));
    return TYPE_ORDER.filter((ty) => present.has(ty));
  }, [unpinned]);
  // A filter whose type has just emptied out (its last entry was placed) lifts itself.
  const activeType = type !== null && types.includes(type) ? type : null;

  const shown = useMemo(() => {
    const needle = query.trim().toLocaleLowerCase();
    return unpinned
      .filter((n) => (activeType === null || n.nodeType === activeType) && n.name.toLocaleLowerCase().includes(needle))
      .sort((a, b) => {
        // Suggested first; otherwise by name, so the list does not reshuffle
        // each time an entry is placed.
        const sa = suggested.has(a.id) ? 0 : 1;
        const sb = suggested.has(b.id) ? 0 : 1;
        return sa - sb || a.name.localeCompare(b.name);
      });
  }, [unpinned, query, activeType, suggested]);

  return (
    <aside className="gx-maps__tray" aria-label={t("campaign.trayTitle")}>
      <div className="gx-maps__tray-head">
        <h2 className="gx-maps__tray-title">
          {t("campaign.trayTitle")} <span className="gx-maps__tray-count">{unpinned.length}</span>
        </h2>
        {unpinned.length > 0 && canSuggest && (
          <Button
            variant="ghost"
            size="sm"
            iconStart={<Sparkles size={12} />}
            loading={suggesting}
            onClick={onSuggest}
          >
            {t("campaign.suggestPlacements")}
          </Button>
        )}
      </div>

      {/* Without a "Depicts" entry there is no prose to read, so the button
          above is absent — and nothing used to say why, or what would bring it
          back. */}
      {unpinned.length > 0 && !canSuggest && onLinkEntry && (
        <p className="gx-maps__tray-note">
          {t("maps.suggestNeedsEntry")}{" "}
          <button type="button" className="gx-maps__link" onClick={onLinkEntry}>
            {t("maps.suggestLinkEntry")}
          </button>
        </p>
      )}
      {suggestError && (
        <span className="gx-field__hint gx-field__hint--error" role="alert">
          {suggestError}
        </span>
      )}
      {suggested.size > 0 && <p className="gx-maps__tray-note">{t("campaign.suggestHint")}</p>}

      {unpinned.length === 0 ? (
        <p className="gx-maps__tray-note">{t("campaign.trayAllPlaced")}</p>
      ) : (
        <>
          <Input
            type="search"
            aria-label={t("maps.traySearchAria")}
            placeholder={t("maps.traySearchPlaceholder")}
            icon={<Search size={14} />}
            value={query}
            onChange={(e) => setQuery(e.target.value)}
          />
          {types.length > 1 && (
            <div className="gx-maps__tray-types" role="group" aria-label={t("maps.trayTypeFilterAria")}>
              <button
                type="button"
                className="gx-maps__chip"
                aria-pressed={activeType === null}
                onClick={() => setType(null)}
              >
                {t("maps.trayTypeAll")}
              </button>
              {types.map((ty) => {
                const meta = metaOf(ty);
                return (
                  <button
                    key={ty}
                    type="button"
                    className="gx-maps__chip"
                    aria-pressed={activeType === ty}
                    style={{ color: meta.color }}
                    onClick={() => setType(activeType === ty ? null : ty)}
                  >
                    {t(meta.labelKey)}
                  </button>
                );
              })}
            </div>
          )}

          {shown.length === 0 ? (
            <EmptyState
              size="inline"
              title={t("maps.trayNoMatches")}
              action={
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => {
                    setQuery("");
                    setType(null);
                  }}
                >
                  {t("maps.trayClearFilters")}
                </Button>
              }
            />
          ) : (
            <ul className="gx-maps__tray-list">
              {shown.map((n) => {
                const meta = metaOf(n.nodeType);
                const Icon = meta.Icon;
                const isSuggested = suggested.has(n.id);
                return (
                  <li key={n.id}>
                    <button
                      type="button"
                      className="gx-maps__tray-item"
                      aria-pressed={placingID === n.id}
                      data-suggested={isSuggested || undefined}
                      aria-label={isSuggested ? t("campaign.suggestedAria", { name: n.name }) : undefined}
                      // A click the keyboard made has no pointer press behind it.
                      onClick={(ev) => onToggle(n, ev.detail === 0)}
                    >
                      <span
                        className="gx-maps__tray-icon"
                        style={{ color: meta.color, background: alphaBg(meta.color) }}
                        aria-hidden="true"
                      >
                        <Icon size={12} />
                      </span>
                      <span className="gx-maps__tray-name">{n.name}</span>
                      {isSuggested && <Sparkles size={12} className="gx-maps__tray-spark" aria-hidden />}
                    </button>
                  </li>
                );
              })}
            </ul>
          )}
        </>
      )}
    </aside>
  );
}
