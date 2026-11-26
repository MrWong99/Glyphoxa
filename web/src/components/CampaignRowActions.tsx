import { Archive, ArchiveRestore, Copy, Download, MoreHorizontal, Settings, Trash2 } from "lucide-react";

import type { Campaign } from "@gen/glyphoxa/management/v1/management_pb";
import { IconButton } from "@/components/ui/IconButton";
import { Menu, MenuItem, MenuSeparator } from "@/components/ui/Menu";
import type { CampaignLifecycle } from "@/components/useCampaignLifecycle";
import { useI18n } from "@/i18n";

// CampaignRowActions — the menu on a campaign's card: Settings, Export,
// Duplicate, Archive or Unarchive, Delete. Every action is offered on every
// campaign, archived or not:
//
//   - a bundle is what a GM wants of an archived campaign before deleting it;
//   - Delete is reachable without a detour through Archive (it used to be
//     offered only on an already archived campaign, behind a "Show archived"
//     toggle — a GM looking for Delete found nothing);
//   - Settings works on a campaign that is not the active one.
//
// The actions themselves — the dialogs, the RPCs, what is said afterwards —
// belong to the page's useCampaignLifecycle; this is only the menu.
//
// It is the shared Menu primitive: opens on Enter, Space or ArrowDown, arrows
// and first letters move, Escape closes and returns focus to the trigger, and a
// dialog an item opens hands focus back to the trigger when it closes.
export function CampaignRowActions({
  campaign,
  lifecycle,
  isActive = false,
}: {
  campaign: Campaign;
  lifecycle: CampaignLifecycle;
  /** This is the Active Campaign: archiving it asks first. */
  isActive?: boolean;
}) {
  const { t } = useI18n();
  const archived = campaign.archivedAt !== undefined;
  // A flow already running on this campaign (archive, delete, duplicate): the
  // actions that would collide with it wait. Settings and Export do not touch
  // what it is doing.
  const busy = lifecycle.busy.has(campaign.id);
  // Archive and Delete also wait for the Active Campaign to be known: whether
  // they ask first, and what they say becomes active, depends on it.
  const settling = !lifecycle.ready;

  return (
    <Menu
      trigger={
        // The label names the campaign: a page of cards is a page of these
        // buttons, and a screen reader must hear which one this is.
        <IconButton label={t("components.campaignActionsFor", { name: campaign.name })}>
          <MoreHorizontal size={18} />
        </IconButton>
      }
    >
      <MenuItem icon={<Settings />} onSelect={() => lifecycle.openSettings(campaign)}>
        {t("campaigns.menuSettings")}
      </MenuItem>
      <MenuItem icon={<Download />} onSelect={() => lifecycle.exportBundle(campaign)}>
        {t("campaigns.menuExport")}
      </MenuItem>
      <MenuItem icon={<Copy />} disabled={busy} onSelect={() => lifecycle.duplicate(campaign)}>
        {t("campaigns.menuDuplicate")}
      </MenuItem>
      {archived ? (
        <MenuItem icon={<ArchiveRestore />} disabled={busy} onSelect={() => lifecycle.unarchive(campaign)}>
          {t("components.unarchive")}
        </MenuItem>
      ) : (
        <MenuItem icon={<Archive />} disabled={busy || settling} onSelect={() => lifecycle.archive(campaign)}>
          {/* The ellipsis is the "this asks first" convention: archiving the
              Active Campaign does, archiving any other is immediate (and
              undoable). */}
          {isActive ? t("campaigns.menuArchiveAsk") : t("components.archive")}
        </MenuItem>
      )}
      <MenuSeparator />
      <MenuItem tone="danger" icon={<Trash2 />} disabled={busy || settling} onSelect={() => lifecycle.remove(campaign)}>
        {t("components.deleteEllipsis")}
      </MenuItem>
    </Menu>
  );
}
