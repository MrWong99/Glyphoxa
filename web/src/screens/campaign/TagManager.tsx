import { useState } from "react";
import { useMutation } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { Pencil, Tags } from "lucide-react";

import { CampaignService } from "@gen/glyphoxa/management/v1/management_pb";
import { useI18n } from "@/i18n";
import { errorMessage } from "@/lib/connectError";
import { invalidateMethodQueries } from "@/lib/queryClient";
import { Button } from "@/components/ui/Button";
import { ConfirmDialog } from "@/components/ui/ConfirmDialog";
import { Dialog } from "@/components/ui/Dialog";
import { EmptyState } from "@/components/ui/EmptyState";
import { IconButton } from "@/components/ui/IconButton";
import { Input } from "@/components/ui/Input";

// "Manage tags" (#695): the campaign's tag vocabulary in one place, with how
// many entries carry each tag and a rename.
//
// Tags are free-form, so they drift: "harbor" on three entries and "harbour"
// on five is two filter chips that each show half the answer. Until now the
// only repair was opening every entry and retyping. RenameTag does it in one
// write, and renaming a tag ONTO one that already exists is the merge: every
// entry that carried the old spelling carries the surviving one afterwards. A
// merge cannot be undone (the server no longer knows which entries had which
// spelling), so it asks first; a plain rename does not.

export type TagUsage = { tag: string; count: number };

export function TagManager({
  open,
  onOpenChange,
  tags,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** The campaign's tags with their usage, in display order. */
  tags: readonly TagUsage[];
}) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  // The tag whose name is being edited, and what has been typed for it.
  const [editing, setEditing] = useState<{ tag: string; draft: string } | null>(null);
  // A rename whose target already exists, waiting for the merge question.
  const [merge, setMerge] = useState<{ from: string; into: string } | null>(null);

  const rename = useMutation(CampaignService.method.renameTag);

  const run = (from: string, to: string, merged: boolean) =>
    rename.mutate(
      { from, to },
      {
        onSuccess: () => {
          setEditing(null);
          void invalidateMethodQueries(queryClient, CampaignService.method.getCampaignTags);
          toast.success(
            merged ? t("knowledge.tagMerged", { from, to }) : t("knowledge.tagRenamed", { from, to }),
          );
        },
      },
    );

  const submit = () => {
    if (!editing || rename.isPending) return;
    const to = editing.draft.trim();
    if (to === "" || to === editing.tag) {
      setEditing(null);
      return;
    }
    // Tags compare without case. Another tag with this name is a merge; the
    // same tag in a different case ("harbor" to "Harbor") is just a rename.
    const existing = tags.find(
      (u) => u.tag.toLowerCase() === to.toLowerCase() && u.tag.toLowerCase() !== editing.tag.toLowerCase(),
    );
    if (existing) setMerge({ from: editing.tag, into: existing.tag });
    else run(editing.tag, to, false);
  };

  const startEditing = (tag: string) => {
    rename.reset();
    setEditing({ tag, draft: tag });
  };

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (!next) {
          setEditing(null);
          rename.reset();
        }
        onOpenChange(next);
      }}
      title={t("knowledge.manageTagsTitle")}
      description={t("knowledge.manageTagsDesc")}
      size="sm"
    >
      {tags.length === 0 ? (
        <EmptyState
          size="inline"
          icon={<Tags size={16} />}
          title={t("knowledge.manageTagsEmptyTitle")}
          description={t("knowledge.manageTagsEmptyDesc")}
        />
      ) : (
        <ul className="gx-wiki-tagmgr">
          {tags.map((u) => {
            const isEditing = editing?.tag === u.tag;
            return (
              <li key={u.tag} className="gx-wiki-tagmgr__row">
                {isEditing ? (
                  <form
                    className="gx-wiki-tagmgr__form"
                    onSubmit={(e) => {
                      e.preventDefault();
                      submit();
                    }}
                  >
                    <Input
                      aria-label={t("knowledge.renameTagFieldAria", { tag: u.tag })}
                      value={editing.draft}
                      autoFocus
                      onChange={(e) => setEditing({ tag: u.tag, draft: e.target.value })}
                      error={
                        rename.isError
                          ? t("knowledge.renameTagError", { message: errorMessage(rename.error) })
                          : null
                      }
                    />
                    <div className="gx-wiki-tagmgr__actions">
                      <Button type="submit" variant="primary" size="sm" disabled={rename.isPending}>
                        {rename.isPending ? t("common.saving") : t("knowledge.renameTagSave")}
                      </Button>
                      <Button
                        type="button"
                        variant="ghost"
                        size="sm"
                        disabled={rename.isPending}
                        onClick={() => setEditing(null)}
                      >
                        {t("common.cancel")}
                      </Button>
                    </div>
                  </form>
                ) : (
                  <>
                    <span className="gx-wiki-tag">{u.tag}</span>
                    <span className="gx-wiki-tagmgr__count">
                      {u.count === 1
                        ? t("knowledge.tagUsageOne")
                        : t("knowledge.tagUsageMany", { n: u.count })}
                    </span>
                    <IconButton
                      size="sm"
                      label={t("knowledge.renameTagAria", { tag: u.tag })}
                      onClick={() => startEditing(u.tag)}
                    >
                      <Pencil size={13} />
                    </IconButton>
                  </>
                )}
              </li>
            );
          })}
        </ul>
      )}

      {merge && (
        <ConfirmDialog
          open
          onOpenChange={(next) => {
            if (!next) setMerge(null);
          }}
          title={t("knowledge.mergeTagsTitle", { from: merge.from, into: merge.into })}
          description={t("knowledge.mergeTagsBody", { from: merge.from, into: merge.into })}
          confirmLabel={t("knowledge.mergeTagsConfirm")}
          onConfirm={() => {
            run(merge.from, merge.into, true);
            setMerge(null);
          }}
        />
      )}
    </Dialog>
  );
}
