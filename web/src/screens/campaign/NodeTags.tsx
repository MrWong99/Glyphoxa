import { useEffect, useMemo, useState } from "react";
import { useMutation, useQuery } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { X } from "lucide-react";

import { CampaignService } from "@gen/glyphoxa/management/v1/management_pb";
import { useI18n } from "@/i18n";
import { Input } from "@/components/ui/Input";
import { invalidateMethodQueries } from "@/lib/queryClient";
import { errorMessage } from "@/lib/connectError";

// Free-form tags on an entry (#543).
//
// They are ORGANIZATION, not schema: the seven Node types carry edge-validity
// rules and prompt semantics and stay closed, but a GM inventing "seafaring",
// "act two" or "needs a voice" should not require a migration.
//
// Tags never enter a prompt. That is what keeps the fact budget untouched and
// stops them quietly becoming a second, unvalidated type system inside NPC
// context.

/** invalidateTags drops the one campaign-wide tag read every surface derives from. */
function invalidateTags(queryClient: ReturnType<typeof useQueryClient>): void {
  void invalidateMethodQueries(queryClient, CampaignService.method.getCampaignTags);
}

export function NodeTags({ nodeID }: { nodeID: string }) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const tagsQuery = useQuery(CampaignService.method.getCampaignTags, {});
  const [draft, setDraft] = useState("");
  // SetNodeTags is replace-in-full, so the payload is only as good as the list it
  // was built from. Reading that list straight out of the cache loses tags to the
  // GM's own typing speed: add A, then add B before A's refetch lands, and B's
  // payload — built from the pre-A cache — deletes A. `settled` holds the
  // authoritative list the SERVER last confirmed, so each save builds on the last
  // one rather than on whatever the cache happens to show.
  const [settled, setSettled] = useState<string[] | null>(null);
  useEffect(() => setSettled(null), [nodeID]);

  const all = useMemo(() => tagsQuery.data?.entries ?? [], [tagsQuery.data]);
  const fromServer = useMemo(
    () => all.filter((e) => e.nodeId === nodeID).map((e) => e.tag),
    [all, nodeID],
  );
  const mine = settled ?? fromServer;
  // The campaign's vocabulary, for autocomplete — deduped case-insensitively so
  // "Act two" and "act two" offer once.
  const vocabulary = useMemo(() => {
    const seen = new Map<string, string>();
    for (const e of all) {
      const key = e.tag.toLowerCase();
      if (!seen.has(key)) seen.set(key, e.tag);
    }
    return [...seen.values()].sort((a, b) => a.localeCompare(b));
  }, [all]);

  const save = useMutation(CampaignService.method.setNodeTags, {
    // The response carries what actually landed (normalized, deduped) — that, not
    // the request, is what the next save must build on.
    onSuccess: (res) => {
      setSettled(res.tags);
      invalidateTags(queryClient);
    },
  });

  const setTags = (tags: string[]) => {
    setSettled(tags);
    save.mutate({ nodeId: nodeID, tags });
  };
  const add = () => {
    const t = draft.trim();
    if (t === "") return;
    // Case-insensitive: adding "act two" to an entry that has "Act Two" is a no-op
    // rather than a near-duplicate the GM has to notice.
    if (!mine.some((x) => x.toLowerCase() === t.toLowerCase())) setTags([...mine, t]);
    setDraft("");
  };

  return (
    <div className="gx-field gx-kg-tags">
      <span className="gx-field__label">{t("knowledge.tagsLabel")}</span>
      <span className="gx-field__hint">{t("knowledge.tagsHint")}</span>

      {mine.length > 0 && (
        <ul className="gx-kg-tags__list">
          {mine.map((tag) => (
            <li key={tag}>
              <span className="gx-kg-chip" data-tag>
                {tag}
                <button
                  type="button"
                  className="gx-kg-tags__remove"
                  aria-label={t("knowledge.removeTagAria", { tag })}
                  disabled={save.isPending}
                  onClick={() => setTags(mine.filter((t) => t !== tag))}
                >
                  <X size={11} />
                </button>
              </span>
            </li>
          ))}
        </ul>
      )}

      <Input
        aria-label={t("knowledge.addTagAria")}
        placeholder={t("knowledge.tagsPlaceholder")}
        list="gx-kg-tag-vocabulary"
        value={draft}
        disabled={save.isPending}
        onChange={(e) => setDraft(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Enter") {
            e.preventDefault();
            add();
          }
        }}
        onBlur={add}
      />
      {/* Autocomplete from what the campaign already uses, so the vocabulary
          converges instead of sprouting near-duplicates. */}
      <datalist id="gx-kg-tag-vocabulary">
        {vocabulary.map((t) => (
          <option key={t} value={t} />
        ))}
      </datalist>

      {save.isError && (
        <span className="gx-editor__status gx-editor__status--error" role="alert">
          {t("knowledge.saveTagsError", { message: errorMessage(save.error) })}
        </span>
      )}
    </div>
  );
}
