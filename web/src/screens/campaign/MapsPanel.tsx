import { useEffect, useMemo, useState } from "react";
import { useQuery } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { EyeOff, Map as MapIcon, Plus, Sparkles, Upload } from "lucide-react";

import { CampaignService, NodeType } from "@gen/glyphoxa/management/v1/management_pb";
import { useCan } from "@/app/capabilities";
import { useScreenNav } from "@/app/screenNav";
import { Button } from "@/components/ui/Button";
import { EmptyState } from "@/components/ui/EmptyState";
import { QueryBoundary } from "@/components/ui/QueryBoundary";
import { SegmentedControl } from "@/components/ui/SegmentedControl";
import { Skeleton } from "@/components/ui/Skeleton";
import { invalidateMethodQueries } from "@/lib/queryClient";
import { useI18n } from "@/i18n";
import { useCampaignPanelState } from "./campaignContext";
import { AddMapDialog, type AddMapMode } from "./MapDialogs";
import { MapView } from "./MapView";

import "./maps.css";

// The Maps view (#538, ADR-0060): world Maps with Knowledge Graph Nodes pinned
// onto them. `resides_in` is a pointer; this is a position.
//
// This file is the panel's frame — which maps exist, which one is open, and how
// a new one is added. One map's toolbar, pins and tray live in MapView; the
// dialogs in MapDialogs.
//
// WHICH MAP IS OPEN is the route's ?map=<id> (through useScreenNav, so the
// panel stays router-free). It used to be component state, which the panel
// lost every time it unmounted: open a pin's entry in the wiki, press Back,
// and the Maps view was on the first map again. With the id in the URL, Back
// returns to the map the GM left, and a map has an address that can be
// bookmarked. Underneath sits a per-campaign in-memory fallback (the host's
// panel store) for the visit that arrives without the parameter — a sidebar
// click on "Maps" — so that, too, reopens the map last looked at.

export function MapsPanel({ onOpenNode }: { onOpenNode?: (nodeID: string) => void }) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const { search, setSearch } = useScreenNav();
  const listQuery = useQuery(CampaignService.method.listMaps, {});
  // The campaign's entries: the Location ones a map can depict (and a generated
  // map is drawn from, #541), and every one for the pin cards' excerpts.
  // What this principal may do with maps (ADR-0065). Creating, generating,
  // replacing and deleting maps are the GM's; pins come with maps.edit; and
  // the entries behind the pins are the wiki's, which is its own right.
  const can = useCan();
  const nodesQuery = useQuery(CampaignService.method.listNodes, {}, { enabled: can.wikiRead });
  const nodes = useMemo(() => nodesQuery.data?.nodes ?? [], [nodesQuery.data]);
  const locations = useMemo(() => nodes.filter((n) => n.nodeType === NodeType.LOCATION), [nodes]);

  const [remembered, setRemembered] = useCampaignPanelState<string | null>("maps.openId", null);
  const [adding, setAdding] = useState<AddMapMode | null>(null);
  // Maps deleted in this visit, hidden at once: until the list refetch lands
  // the cached list still names them, and opening one reads a row that is gone.
  const [deleted, setDeleted] = useState<ReadonlySet<string>>(() => new Set());

  const maps = useMemo(
    () => (listQuery.data?.maps ?? []).filter((m) => !deleted.has(m.id)),
    [listQuery.data, deleted],
  );
  // A map created a moment ago: the cached list does not have it yet, so it
  // cannot be opened — and must not be "corrected" away — until the refetch does.
  const [wanted, setWanted] = useState<string | null>(null);
  const known = (id: string | null | undefined): id is string => Boolean(id) && maps.some((m) => m.id === id);
  // A just-created map first; then the URL; then the map last opened in this
  // campaign; then the first.
  const currentID = known(wanted)
    ? wanted
    : known(search.map)
      ? search.map
      : known(remembered)
        ? remembered
        : (maps[0]?.id ?? null);

  // Keep the URL naming the map that is actually on screen: a visit without the
  // parameter (or with the id of a map that no longer exists) is corrected in
  // place — replace, so it adds no history entry — and from then on every map
  // the GM opens is a step Back can return to.
  const loaded = listQuery.data !== undefined;
  const waiting = wanted !== null && wanted !== currentID;
  useEffect(() => {
    if (!loaded || waiting) return;
    if (currentID !== remembered) setRemembered(currentID);
    if ((search.map ?? null) !== currentID) {
      // The created map is a place the GM went to (Back returns from it);
      // everything else here is a correction and adds no history entry.
      setSearch({ map: currentID ?? undefined }, { replace: wanted === null });
    }
    if (wanted !== null) setWanted(null);
    // setRemembered / setSearch are the seam's own; the effect is keyed on the
    // values it reconciles.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [loaded, waiting, currentID, remembered, search.map, wanted]);

  const open = (id: string) => {
    setRemembered(id);
    if (id !== search.map) setSearch({ map: id });
  };

  // Both reads of the same data: the map list and any open map view.
  const refresh = () =>
    invalidateMethodQueries(queryClient, CampaignService.method.listMaps, CampaignService.method.getMapView);

  const addButtons = (
    <>
      <Button variant="primary" iconStart={<Upload size={14} />} onClick={() => setAdding("upload")}>
        {t("maps.emptyUpload")}
      </Button>
      <Button variant="secondary" iconStart={<Sparkles size={14} />} onClick={() => setAdding("generate")}>
        {t("maps.emptyGenerate")}
      </Button>
    </>
  );

  return (
    <div className="gx-maps">
      <QueryBoundary
        query={listQuery}
        skeleton={<Skeleton.Block ratio="16 / 9" label={t("maps.loadingMaps")} />}
        errorTitle={t("maps.loadErrorTitle")}
      >
        {maps.length === 0 ? (
          <EmptyState
            icon={<MapIcon size={22} />}
            title={t("maps.emptyTitle")}
            description={can.manage ? t("maps.emptyDesc") : t("access.mapsEmptyGuest")}
            titleAs="h2"
            action={can.manage && addButtons}
          />
        ) : (
          <>
            <div className="gx-maps__bar">
              <div className="gx-maps__strip">
                <SegmentedControl
                  label={t("maps.stripAria")}
                  value={currentID ?? ""}
                  onChange={open}
                  options={maps.map((m) => ({
                    value: m.id,
                    label: (
                      <>
                        {m.name}
                        {m.gmPrivate && <EyeOff size={12} aria-label={t("campaign.mapGmPrivateAria")} />}
                      </>
                    ),
                  }))}
                />
              </div>
              {can.manage && (
                <Button variant="secondary" size="sm" iconStart={<Plus size={13} />} onClick={() => setAdding("upload")}>
                  {t("campaign.addMap")}
                </Button>
              )}
            </div>

            {currentID && (
              // key={currentID}: a suggestion answers "does this belong on THIS
              // map", so its highlight — and every half-finished gesture — must
              // not survive navigating to another one.
              <MapView
                key={currentID}
                mapID={currentID}
                maps={maps}
                nodes={nodes}
                onNavigate={open}
                onChanged={refresh}
                onDeleted={(id) => setDeleted((d) => new Set(d).add(id))}
                onOpenNode={can.wikiRead ? onOpenNode : undefined}
              />
            )}
          </>
        )}
      </QueryBoundary>

      {adding && (
        <AddMapDialog
          initialMode={adding}
          maps={maps}
          locations={locations}
          onClose={() => setAdding(null)}
          onCreated={(id) => {
            // Opened by the reconciling effect once the refetched list has it.
            setWanted(id);
            void refresh();
          }}
        />
      )}
    </div>
  );
}
