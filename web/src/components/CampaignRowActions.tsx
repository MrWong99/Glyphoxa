import { useLayoutEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { useMutation, createConnectQueryKey } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { Archive, ArchiveRestore, MoreHorizontal, Trash2 } from "lucide-react";

import { CampaignService } from "@gen/glyphoxa/management/v1/management_pb";
import { invalidateActiveCampaignScopedQueries } from "@/lib/campaignCache";
import { usePopoverDismiss } from "@/components/ui/usePopoverDismiss";
import { ConfirmDialog } from "@/components/ui/ConfirmDialog";

import "./campaignRowActions.css";

// CampaignRowActions — the per-row lifecycle menu in the campaign switcher's
// archive-management view (#269, placement #266). An ACTIVE row offers "Archive";
// an ARCHIVED row offers "Unarchive" and "Delete…". Archive is the primary flow;
// hard delete is only reachable on an already-archived campaign and is gated
// behind a re-typed campaign name in a ConfirmDialog (irreversible — it cascades
// to the campaign's Agents, Knowledge Graph, transcripts, and Voice Sessions).
//
// Every mutation, on success, refreshes the campaign list (all include_archived
// inputs) so the row moves between the active/archived groups, and runs the
// Active-Campaign sweep so a screen scoped to a just-archived/deleted campaign
// follows resolution without a reload. Failures surface as a toast; the live
// Voice Session's campaign is refused SERVER-SIDE (CodeFailedPrecondition), shown
// via that toast.

type CampaignRow = { id: string; name: string; archived: boolean };

// MenuPos anchors the portalled menu to the trigger in the viewport. right/left
// pin its horizontal edge to the trigger's right edge; the menu opens below by
// default and flips above when it would overflow the viewport bottom (#338).
type MenuPos = { top: number; right: number; flipUp: boolean };

// A conservative menu-height estimate for the flip decision, taken before the
// menu has laid out. The tallest variant (archived: Unarchive + Delete…) is ~2
// rows plus padding; overestimating only flips slightly earlier, which is safe.
const MENU_HEIGHT_ESTIMATE = 96;

export function CampaignRowActions({ campaign }: { campaign: CampaignRow }) {
  const queryClient = useQueryClient();
  const [menuOpen, setMenuOpen] = useState(false);
  const [confirmOpen, setConfirmOpen] = useState(false);
  const [pos, setPos] = useState<MenuPos | null>(null);
  const wrapRef = useRef<HTMLDivElement>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const menuRef = useRef<HTMLDivElement>(null);
  // The menu portals to document.body to escape the switcher list's overflow, so
  // it lives outside wrapRef — menuRef is passed as the extra "inside" element so
  // clicking a menu item doesn't read as an outside click (#338).
  usePopoverDismiss(wrapRef, menuOpen, () => setMenuOpen(false), menuRef);

  // Anchor the portalled menu to the trigger each time it opens: right-align its
  // edge to the trigger's, drop below, and flip above near the viewport bottom so
  // a row low in the list can't push the menu off-screen (#338). useLayoutEffect
  // so the position is set before paint — no first-frame flash at (0,0).
  useLayoutEffect(() => {
    if (!menuOpen) return;
    const anchor = triggerRef.current;
    if (!anchor) return;
    const measure = () => {
      const rect = anchor.getBoundingClientRect();
      const flipUp = rect.bottom + MENU_HEIGHT_ESTIMATE > window.innerHeight;
      setPos({
        top: flipUp ? rect.top - 2 : rect.bottom + 2,
        right: window.innerWidth - rect.right,
        flipUp,
      });
    };
    measure();
    // Keep the menu pinned if the viewport scrolls or resizes while it's open.
    window.addEventListener("scroll", measure, true);
    window.addEventListener("resize", measure);
    return () => {
      window.removeEventListener("scroll", measure, true);
      window.removeEventListener("resize", measure);
    };
  }, [menuOpen]);

  // Invalidate the campaign list across every include_archived input (the key is
  // omitted so React Query matches all listCampaigns entries by prefix), plus the
  // Active-Campaign scoped sweep so a screen on the affected campaign re-resolves.
  const refreshAfterChange = () => {
    void queryClient.invalidateQueries({
      queryKey: createConnectQueryKey({
        schema: CampaignService.method.listCampaigns,
        cardinality: "finite",
      }),
    });
    void invalidateActiveCampaignScopedQueries(queryClient);
  };

  const archive = useMutation(CampaignService.method.archiveCampaign, {
    onSuccess: () => {
      setMenuOpen(false);
      refreshAfterChange();
    },
    onError: (err) => toast.error(`Couldn't archive campaign: ${err.message}`),
  });

  const unarchive = useMutation(CampaignService.method.unarchiveCampaign, {
    onSuccess: () => {
      setMenuOpen(false);
      refreshAfterChange();
    },
    onError: (err) => toast.error(`Couldn't unarchive campaign: ${err.message}`),
  });

  const del = useMutation(CampaignService.method.deleteCampaign, {
    onSuccess: () => {
      setConfirmOpen(false);
      setMenuOpen(false);
      refreshAfterChange();
    },
    onError: (err) => toast.error(`Couldn't delete campaign: ${err.message}`),
  });

  return (
    <div className="gx-campaign-row-actions" ref={wrapRef}>
      <button
        ref={triggerRef}
        type="button"
        className="gx-campaign-row-actions__trigger"
        aria-haspopup="menu"
        aria-expanded={menuOpen}
        // The label is name-free so it never collides with the sibling switch
        // button's accessible name (which carries the campaign name); the campaign
        // is identified by the row it sits in, and by the menu items + confirm
        // dialog once opened.
        aria-label="Campaign actions"
        title={`Actions for ${campaign.name}`}
        onClick={() => setMenuOpen((o) => !o)}
      >
        <MoreHorizontal size={15} />
      </button>

      {menuOpen &&
        createPortal(
          <div
            ref={menuRef}
            className="gx-select__content gx-campaign-row-actions__menu"
            role="menu"
            data-flip-up={pos?.flipUp || undefined}
            style={
              pos
                ? {
                    top: pos.top,
                    right: pos.right,
                    // Anchor the flipped menu by its bottom edge so it grows
                    // upward from the trigger rather than overlapping it.
                    transform: pos.flipUp ? "translateY(-100%)" : undefined,
                  }
                : undefined
            }
          >
          {campaign.archived ? (
            <>
              <button
                type="button"
                role="menuitem"
                className="gx-select__item"
                disabled={unarchive.isPending}
                onClick={() => unarchive.mutate({ id: campaign.id })}
              >
                <ArchiveRestore size={14} /> Unarchive
              </button>
              <button
                type="button"
                role="menuitem"
                className="gx-select__item gx-select__item--danger"
                onClick={() => {
                  setMenuOpen(false);
                  setConfirmOpen(true);
                }}
              >
                <Trash2 size={14} /> Delete…
              </button>
            </>
          ) : (
            <button
              type="button"
              role="menuitem"
              className="gx-select__item"
              disabled={archive.isPending}
              onClick={() => archive.mutate({ id: campaign.id })}
            >
              <Archive size={14} /> Archive
            </button>
          )}
          </div>,
          document.body,
        )}

      <ConfirmDialog
        open={confirmOpen}
        onOpenChange={setConfirmOpen}
        title={`Delete “${campaign.name}”?`}
        description="This permanently deletes the campaign and all of its Agents, Knowledge Graph, transcripts, and Voice Sessions. This cannot be undone."
        confirmLabel="Delete campaign"
        confirmText={campaign.name}
        confirmTextLabel={`Type the campaign name to confirm`}
        confirmDisabled={del.isPending}
        onConfirm={() => del.mutate({ id: campaign.id })}
      />
    </div>
  );
}
