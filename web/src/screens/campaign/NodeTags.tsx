import { useMemo, useState } from "react";
import { useMutation, useQuery, createConnectQueryKey } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { X } from "lucide-react";

import { CampaignService } from "@gen/glyphoxa/management/v1/management_pb";
import { Input } from "@/components/ui/Input";

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
export function invalidateTags(queryClient: ReturnType<typeof useQueryClient>): void {
  void queryClient.invalidateQueries({
    queryKey: createConnectQueryKey({
      schema: CampaignService.method.getCampaignTags,
      cardinality: "finite",
    }),
  });
}

export function NodeTags({ nodeID }: { nodeID: string }) {
  const queryClient = useQueryClient();
  const tagsQuery = useQuery(CampaignService.method.getCampaignTags, {});
  const [draft, setDraft] = useState("");

  const all = useMemo(() => tagsQuery.data?.entries ?? [], [tagsQuery.data]);
  const mine = useMemo(
    () => all.filter((e) => e.nodeId === nodeID).map((e) => e.tag),
    [all, nodeID],
  );
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
    onSuccess: () => invalidateTags(queryClient),
  });

  const setTags = (tags: string[]) => save.mutate({ nodeId: nodeID, tags });
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
      <span className="gx-field__label">Tags</span>
      <span className="gx-field__hint">
        Your own labels for finding things. They never reach an NPC's prompt.
      </span>

      {mine.length > 0 && (
        <ul className="gx-kg-tags__list">
          {mine.map((tag) => (
            <li key={tag}>
              <span className="gx-kg-chip" data-tag>
                {tag}
                <button
                  type="button"
                  className="gx-kg-tags__remove"
                  aria-label={`Remove tag ${tag}`}
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
        aria-label="Add a tag"
        placeholder="seafaring, act two, needs a voice…"
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
          Couldn't save tags: {save.error.message}
        </span>
      )}
    </div>
  );
}
