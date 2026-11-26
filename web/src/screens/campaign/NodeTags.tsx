import { useEffect, useMemo, useRef, useState } from "react";
import { useMutation, useQuery } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { Plus } from "lucide-react";

import { CampaignService } from "@gen/glyphoxa/management/v1/management_pb";
import { useI18n } from "@/i18n";
import { ChipInput } from "@/components/ui/ChipInput";
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

/** How many of the campaign's other tags are offered as one-click chips. */
const MAX_SUGGESTIONS = 8;

/** invalidateTags drops the one campaign-wide tag read every surface derives from. */
function invalidateTags(queryClient: ReturnType<typeof useQueryClient>): void {
  void invalidateMethodQueries(queryClient, CampaignService.method.getCampaignTags);
}

// Tags compare without case: "Act two" and "act two" are one tag, and the
// first spelling wins.
function dedupe(tags: readonly string[]): string[] {
  const seen = new Set<string>();
  const out: string[] = [];
  for (const tag of tags) {
    const key = tag.trim().toLowerCase();
    if (key === "" || seen.has(key)) continue;
    seen.add(key);
    out.push(tag.trim());
  }
  return out;
}

const sameList = (a: readonly string[], b: readonly string[]) =>
  a.length === b.length && a.every((v, i) => v === b[i]);

export function NodeTags({ nodeID }: { nodeID: string }) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const tagsQuery = useQuery(CampaignService.method.getCampaignTags, {});
  // SetNodeTags is replace-in-full, so a payload is only as good as the list it
  // was built from. `shown` is the list the GM is looking at: every edit paints
  // it at once and the next edit builds on it, never on a cache that has not
  // caught up with the last save.
  const [shown, setShown] = useState<string[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  // What the server last CONFIRMED on this visit (null = nothing saved yet, the
  // cache is the truth). A rejected save rolls the chips back to it: leaving
  // the refused tag up showed it as saved, and the next save then resent it.
  const confirmed = useRef<string[] | null>(null);
  // Saves go out ONE AT A TIME, and the field is never locked while one is in
  // flight. Two replace-in-full writes racing each other can land in either
  // order, so the loser's list would win; instead, edits made during a save
  // collapse into `queued` and go out as one write when it returns.
  const inFlight = useRef(false);
  const queued = useRef<string[] | null>(null);
  useEffect(() => {
    confirmed.current = null;
    queued.current = null;
    setShown(null);
    setError(null);
  }, [nodeID]);

  const all = useMemo(() => tagsQuery.data?.entries ?? [], [tagsQuery.data]);
  const fromServer = useMemo(
    () => all.filter((e) => e.nodeId === nodeID).map((e) => e.tag),
    [all, nodeID],
  );
  // A new server list while no save of ours is in flight is the truth again.
  // Every edit here is sent at once, so `shown` never holds anything unsaved;
  // left in place it would hide a change made elsewhere (a rename or merge in
  // "Manage tags"), and the next chip edit, replace-in-full, would write the
  // old spelling back onto this entry.
  useEffect(() => {
    if (inFlight.current) return;
    confirmed.current = null;
    setShown(null);
  }, [fromServer]);
  const mine = shown ?? fromServer;

  // The campaign's other tags, most used first: one click instead of retyping,
  // which is also what keeps "harbor" and "harbour" from both existing.
  const suggestions = useMemo(() => {
    const have = new Set(mine.map((tag) => tag.toLowerCase()));
    const usage = new Map<string, { tag: string; n: number }>();
    for (const e of all) {
      const key = e.tag.toLowerCase();
      if (have.has(key)) continue;
      const seen = usage.get(key);
      if (seen) seen.n++;
      else usage.set(key, { tag: e.tag, n: 1 });
    }
    return [...usage.values()]
      .sort((a, b) => b.n - a.n || a.tag.localeCompare(b.tag))
      .slice(0, MAX_SUGGESTIONS)
      .map((u) => u.tag);
  }, [all, mine]);

  const save = useMutation(CampaignService.method.setNodeTags);

  const run = async (first: string[]) => {
    inFlight.current = true;
    let next: string[] | null = first;
    while (next) {
      queued.current = null;
      try {
        const res = await save.mutateAsync({ nodeId: nodeID, tags: next });
        // The response carries what actually landed (normalized, deduped).
        confirmed.current = res.tags;
        if (queued.current === null) setShown(res.tags);
      } catch (err) {
        queued.current = null;
        setShown(confirmed.current);
        setError(t("knowledge.saveTagsError", { message: errorMessage(err) }));
      }
      next = queued.current;
    }
    inFlight.current = false;
    invalidateTags(queryClient);
  };

  const setTags = (tags: string[]) => {
    const next = dedupe(tags);
    if (sameList(next, mine)) return;
    setShown(next);
    setError(null);
    if (inFlight.current) queued.current = next;
    else void run(next);
  };

  return (
    <div className="gx-kg-tags">
      <ChipInput
        label={t("knowledge.tagsLabel")}
        hint={t("knowledge.tagsHint")}
        placeholder={t("knowledge.tagsPlaceholder")}
        values={mine}
        onChange={setTags}
      />
      {suggestions.length > 0 && (
        <ul className="gx-wiki-facts__suggest" aria-label={t("knowledge.tagSuggestionsAria")}>
          {suggestions.map((tag) => (
            <li key={tag}>
              <button
                type="button"
                className="gx-wiki-suggest"
                data-tag
                aria-label={t("knowledge.addExistingTagAria", { tag })}
                onClick={() => setTags([...mine, tag])}
              >
                <Plus size={11} aria-hidden />
                {tag}
              </button>
            </li>
          ))}
        </ul>
      )}
      {error && (
        <span className="gx-editor__status gx-editor__status--error" role="alert">
          {error}
        </span>
      )}
    </div>
  );
}
