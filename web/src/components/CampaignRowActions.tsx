import { useRef, useState } from "react";
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

export function CampaignRowActions({ campaign }: { campaign: CampaignRow }) {
  const queryClient = useQueryClient();
  const [menuOpen, setMenuOpen] = useState(false);
  const [confirmOpen, setConfirmOpen] = useState(false);
  const wrapRef = useRef<HTMLDivElement>(null);
  usePopoverDismiss(wrapRef, menuOpen, () => setMenuOpen(false));

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

      {menuOpen && (
        <div className="gx-select__content gx-campaign-row-actions__menu" role="menu">
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
        </div>
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
