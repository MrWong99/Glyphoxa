import { useId, useState, type FormEvent } from "react";
import { useMutation } from "@connectrpc/connect-query";

import { CampaignService } from "@gen/glyphoxa/management/v1/management_pb";
import type { Agent } from "@gen/glyphoxa/management/v1/management_pb";
import { Alert } from "@/components/ui/Alert";
import { Button } from "@/components/ui/Button";
import { Dialog, DialogClose } from "@/components/ui/Dialog";
import { Input } from "@/components/ui/Input";
import { useI18n } from "@/i18n";
import { apiErrorMessage } from "@/lib/connectError";
import { VoiceField } from "./VoiceField";
import type { VoicesQuery } from "./VoicePicker";

import "./cast.css";

// The New NPC dialog. "Add NPC" used to create a row called "New NPC" on the
// spot — one click, and the cast had a placeholder the GM then had to rename,
// whose wiki entry was already named "New NPC" too (ADR-0008 second amendment
// auto-creates it). Now nothing exists until the GM has said who the NPC is:
// a name (the one required answer), optionally a role, a one-line concept and
// a voice.
//
// Submitting creates the Agent with those real values and hands it — and the
// concept — to the caller, which selects it; with a concept, the editor then
// drafts a personality from it (GeneratePersona) and shows the draft UNSAVED,
// for the GM to review. The dialog itself never generates: the NPC exists the
// moment Create returns, whatever the draft does afterwards.
export function NewNpcDialog({
  open,
  onOpenChange,
  voicesQuery,
  campaignLanguage,
  onCreated,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  voicesQuery: VoicesQuery;
  campaignLanguage?: string;
  /** The Agent as the server created it, and the (trimmed) concept, "" for none. */
  onCreated: (agent: Agent, concept: string) => void;
}) {
  const { t } = useI18n();
  const formId = useId();
  const nameId = useId();
  // Input does not forward a ref; the field is reached by its id.
  const focusName = () => document.getElementById(nameId)?.focus();

  const [name, setName] = useState("");
  const [title, setTitle] = useState("");
  const [concept, setConcept] = useState("");
  const [voice, setVoice] = useState("");
  // The "name is required" line appears once the GM has tried to submit, not
  // while they are still on their way to the field.
  const [attempted, setAttempted] = useState(false);

  const create = useMutation(CampaignService.method.createAgent);
  const { reset } = create;

  // Every opening is a fresh form. Adjusted during render so the dialog never
  // paints the last NPC's answers.
  const [wasOpen, setWasOpen] = useState(open);
  if (open !== wasOpen) {
    setWasOpen(open);
    if (open) {
      setName("");
      setTitle("");
      setConcept("");
      setVoice("");
      setAttempted(false);
      reset();
    }
  }

  const nameMissing = name.trim() === "";

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (create.isPending) return;
    if (nameMissing) {
      setAttempted(true);
      focusName();
      return;
    }
    try {
      const res = await create.mutateAsync({
        name: name.trim(),
        title: title.trim(),
        persona: "",
        voice,
        addressOnly: false,
        aliases: [],
      });
      if (res.agent) onCreated(res.agent, concept.trim());
    } catch {
      // The failure is rendered from the mutation below; the busy button was
      // disabled meanwhile, which takes keyboard focus off it, so put the GM
      // back in the form.
      focusName();
    }
  };

  return (
    <Dialog
      open={open}
      onOpenChange={onOpenChange}
      title={t("cast.newNpcTitle")}
      description={t("cast.newNpcDesc")}
      // A stray click on the backdrop must not throw the typing away.
      closeOnOutsideClick={false}
      className="gx-cast-new"
      footer={
        <>
          <DialogClose>
            <Button type="button" variant="ghost">{t("common.cancel")}</Button>
          </DialogClose>
          <Button type="submit" form={formId} loading={create.isPending}>
            {t("cast.newNpcCreate")}
          </Button>
        </>
      }
    >
      <form id={formId} className="gx-cast-new__form" onSubmit={(e) => void submit(e)} noValidate>
        <Input
          id={nameId}
          label={t("campaign.nameLabel")}
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder={t("cast.newNpcNamePlaceholder")}
          error={attempted && nameMissing ? t("cast.newNpcNameRequired") : null}
          aria-required="true"
          autoComplete="off"
          autoFocus
        />
        <Input
          label={t("campaign.titleLabel")}
          value={title}
          onChange={(e) => setTitle(e.target.value)}
          placeholder={t("campaign.titlePlaceholder")}
          autoComplete="off"
        />
        <Input
          label={t("cast.newNpcConceptLabel")}
          value={concept}
          onChange={(e) => setConcept(e.target.value)}
          placeholder={t("cast.newNpcConceptPlaceholder")}
          hint={t("cast.newNpcConceptHint")}
          autoComplete="off"
        />
        <VoiceField
          label={t("cast.newNpcVoiceLabel")}
          voicesQuery={voicesQuery}
          value={voice}
          onChange={setVoice}
          campaignLanguage={campaignLanguage}
        />
        {create.isError && (
          <Alert tone="danger" title={t("cast.newNpcErrorTitle")}>
            {apiErrorMessage(create.error, t)}
          </Alert>
        )}
      </form>
    </Dialog>
  );
}
