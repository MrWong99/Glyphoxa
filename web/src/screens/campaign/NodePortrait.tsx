import { useEffect, useRef, useState } from "react";
import { useMutation } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { ImagePlus, Sparkles, Trash2 } from "lucide-react";

import { CampaignService } from "@gen/glyphoxa/management/v1/management_pb";
import type { Node } from "@gen/glyphoxa/management/v1/management_pb";
import { useI18n } from "@/i18n";
import { Button } from "@/components/ui/Button";
import { invalidateKnowledgeReads } from "./knowledgeCache";

// Node portraits (#590): a picture on an entry, generated from its own prose or
// uploaded — the Maps draft-review flow applied to the wiki. A generated DRAFT
// exists only in this browser (an object URL over the response bytes) until the
// GM applies it through SetNodePortrait, the same door an upload enters by; the
// dashed-gold frame is the graph's proposal-ghost grammar — a preview must never
// read as saved.

/** portraitURL builds the plain-mount image URL. updatedAt is the server's cache
 * validator: an applied portrait bumps it, so the browser refetches exactly when
 * the picture actually changed. */
export const portraitURL = (node: Node) =>
  `/api/v1/knowledge/nodes/${node.id}/portrait?v=${node.updatedAt ? Number(node.updatedAt.seconds) : 0}`;

export function NodePortrait({ node }: { node: Node }) {
  const { t } = useI18n();
  const queryClient = useQueryClient();

  const [prompt, setPrompt] = useState("");
  const [draft, setDraft] = useState<{ bytes: Uint8Array; contentType: string; url: string } | null>(
    null,
  );
  const [error, setError] = useState<string | null>(null);
  // The object URL is mirrored in a ref so the unmount cleanup can revoke it
  // (state is gone by then) — the MapsPanel draft's lifecycle.
  const draftURL = useRef<string | null>(null);
  useEffect(
    () => () => {
      if (draftURL.current) URL.revokeObjectURL(draftURL.current);
    },
    [],
  );
  const dropDraft = () => {
    if (draftURL.current) URL.revokeObjectURL(draftURL.current);
    draftURL.current = null;
    setDraft(null);
  };

  const fileInput = useRef<HTMLInputElement | null>(null);

  const generate = useMutation(CampaignService.method.generateNodePortrait);
  const apply = useMutation(CampaignService.method.setNodePortrait);
  const clear = useMutation(CampaignService.method.clearNodePortrait);
  const pending = generate.isPending || apply.isPending || clear.isPending;

  // A portrait write changes the Node row (has_portrait + updated_at), so the
  // full knowledge read set is dropped — per-surface subsets are the bug
  // knowledgeCache.ts exists to prevent.
  const invalidate = () => invalidateKnowledgeReads(queryClient);

  const runGenerate = async () => {
    setError(null);
    try {
      const res = await generate.mutateAsync({ nodeId: node.id, prompt: prompt.trim() });
      dropDraft();
      const blob = new Blob([res.imageBytes as BlobPart], { type: res.contentType });
      const url = URL.createObjectURL(blob);
      draftURL.current = url;
      setDraft({ bytes: res.imageBytes, contentType: res.contentType, url });
    } catch (e) {
      setError(t("knowledge.generatePortraitError", { message: (e as Error).message }));
    }
  };

  const applyBytes = async (bytes: Uint8Array, contentType: string) => {
    setError(null);
    try {
      await apply.mutateAsync({ nodeId: node.id, imageBytes: bytes, contentType });
      dropDraft();
      setPrompt("");
      invalidate();
    } catch (e) {
      setError(t("knowledge.portraitError", { message: (e as Error).message }));
    }
  };

  const onUpload = async (file: File) => {
    await applyBytes(new Uint8Array(await file.arrayBuffer()), file.type);
  };

  const remove = async () => {
    setError(null);
    try {
      await clear.mutateAsync({ nodeId: node.id });
      invalidate();
    } catch (e) {
      setError(t("knowledge.portraitError", { message: (e as Error).message }));
    }
  };

  return (
    <div className="gx-field gx-portrait">
      <span className="gx-field__label">{t("knowledge.portraitLabel")}</span>
      <span className="gx-field__hint">
        {node.gmPrivate ? t("knowledge.portraitPrivateHint") : t("knowledge.portraitHint")}
      </span>

      {node.hasPortrait && !draft && (
        <img
          className="gx-portrait__img"
          src={portraitURL(node)}
          alt={t("knowledge.portraitAlt", { name: node.name })}
        />
      )}

      {draft && (
        <div className="gx-portrait__draft">
          <img className="gx-portrait__draft-img" src={draft.url} alt={t("knowledge.portraitDraftAlt")} />
          <div className="gx-portrait__actions">
            <Button
              variant="primary"
              disabled={pending}
              onClick={() => void applyBytes(draft.bytes, draft.contentType)}
            >
              {apply.isPending ? t("common.saving") : t("knowledge.applyPortrait")}
            </Button>
            <Button variant="ghost" disabled={pending} onClick={dropDraft}>
              {t("knowledge.discardPortraitDraft")}
            </Button>
          </div>
        </div>
      )}

      {/* The prompt is optional extra direction — the entry's own prose is the
          seed. A GM-only entry has no public prose to seed from (the server
          refuses), so generation is disabled rather than offered-then-denied. */}
      {!node.gmPrivate && (
        <input
          className="gx-input"
          aria-label={t("knowledge.portraitPromptLabel")}
          placeholder={t("knowledge.portraitPromptPlaceholder")}
          value={prompt}
          disabled={pending}
          onChange={(e) => setPrompt(e.target.value)}
        />
      )}

      <div className="gx-portrait__actions">
        {!node.gmPrivate && (
          <Button
            variant="secondary"
            iconStart={<Sparkles size={14} />}
            disabled={pending}
            onClick={() => void runGenerate()}
          >
            {generate.isPending
              ? t("knowledge.generatePortraitPending")
              : draft || node.hasPortrait
                ? t("knowledge.regeneratePortrait")
                : t("knowledge.generatePortrait")}
          </Button>
        )}
        <Button
          variant="ghost"
          iconStart={<ImagePlus size={14} />}
          disabled={pending}
          onClick={() => fileInput.current?.click()}
        >
          {t("knowledge.uploadPortrait")}
        </Button>
        {node.hasPortrait && (
          <Button
            variant="ghost"
            iconStart={<Trash2 size={14} />}
            disabled={pending}
            onClick={() => void remove()}
          >
            {t("knowledge.removePortrait")}
          </Button>
        )}
      </div>
      <input
        ref={fileInput}
        type="file"
        accept="image/*"
        hidden
        aria-label={t("knowledge.uploadPortraitAria")}
        onChange={(e) => {
          const file = e.target.files?.[0];
          e.target.value = "";
          if (file) void onUpload(file);
        }}
      />

      {generate.isPending && (
        <span className="gx-field__hint">{t("knowledge.portraitPromptHint")}</span>
      )}
      {error && (
        <span className="gx-editor__status gx-editor__status--error" role="alert">
          {error}
        </span>
      )}
    </div>
  );
}
