import { useId, useState, type FormEvent } from "react";
import { useMutation } from "@connectrpc/connect-query";
import { toast } from "sonner";
import { Link2, Plus } from "lucide-react";

import { AdminService, type Invitation } from "@gen/glyphoxa/management/v1/management_pb";
import {
  InvitationList,
  LinkCreatedDialog,
  LinkLabelField,
  LinkPinField,
  LinkUseFields,
  useLinkLimits,
} from "@/components/access/invitationParts";
import { Alert } from "@/components/ui/Alert";
import { Badge } from "@/components/ui/Badge";
import { Button } from "@/components/ui/Button";
import { Dialog, DialogClose } from "@/components/ui/Dialog";
import { EmptyState } from "@/components/ui/EmptyState";
import { Switch } from "@/components/ui/Switch";
import { useI18n } from "@/i18n";
import { errorMessage } from "@/lib/connectError";
import { invitationLink } from "@/lib/invitationLink";
import { useRefreshOverview } from "./refreshOverview";

// Access invitation links (ADR-0065): a `/join/<token>` link a Platform Admin
// hands to someone whose Discord ID they do not know. Whoever opens it and
// signs in gets an Access Grant — and so a table of their own.
//
// The token is a secret the server keeps only as a hash. It is readable exactly
// once, in the CreateAccessInvitation response, so the create dialog shows the
// full link right there, with a copy button and the plain statement that it
// will not be shown again. The list below never has it: a link is identified by
// its label, its limits and its state.
//
// The list, the limit fields and the one showing of the link are shared with
// the campaign invitations of a Campaign's Sharing view
// (components/access/invitationParts.tsx); what is this screen's own is the
// AdminService calls and the Platform Keys choice.

export function InvitationLinks({ invitations, open }: { invitations: Invitation[]; open: boolean }) {
  const { t } = useI18n();
  const titleId = useId();
  const refresh = useRefreshOverview();
  const [creating, setCreating] = useState(false);

  const revoke = useMutation(AdminService.method.revokeAccessInvitation, {
    onSuccess: () => {
      toast.success(t("access.toastLinkRevoked"));
      void refresh();
    },
    onError: (err) => toast.error(t("access.actionFailed", { message: errorMessage(err) })),
  });

  const newLink = (
    <Button variant="secondary" size="sm" iconStart={<Plus size={14} />} onClick={() => setCreating(true)}>
      {t("access.newLink")}
    </Button>
  );

  return (
    <section className="gx-admin__section" aria-labelledby={titleId}>
      <div className="gx-admin__section-head">
        <h2 id={titleId} className="gx-section-title">
          {t("access.linksTitle")}
        </h2>
        {invitations.length > 0 && newLink}
      </div>
      <p className="gx-admin__lede">{t("access.linksLede")}</p>

      {invitations.length === 0 ? (
        <EmptyState
          icon={<Link2 size={22} />}
          title={t("access.linksEmptyTitle")}
          description={t("access.linksEmptyDesc")}
          action={newLink}
        />
      ) : (
        <InvitationList
          invitations={invitations}
          grants={(inv) =>
            inv.platformKeys && (
              <Badge variant="gold" size="sm">
                {t("access.linkWithKeys")}
              </Badge>
            )
          }
          revokeDescription={t("access.revokeLinkDesc")}
          revoking={revoke.isPending}
          onRevoke={(inv) => revoke.mutate({ id: inv.id })}
        />
      )}

      {/* Mounted only while open: closing it unmounts the one place the full
          link was held, which is what "shown once" means. */}
      {creating && <CreateLinkDialog open={open} onClose={() => setCreating(false)} />}
    </section>
  );
}

function CreateLinkDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const { t } = useI18n();
  const formId = useId();
  const keysHintId = useId();
  const refresh = useRefreshOverview();

  const limits = useLinkLimits();
  const [platformKeys, setPlatformKeys] = useState(false);
  // The full link, held in this component's state and nowhere else.
  const [link, setLink] = useState<string | null>(null);

  const create = useMutation(AdminService.method.createAccessInvitation, {
    onSuccess: (res) => {
      setLink(invitationLink(res.token));
      void refresh();
    },
  });

  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (!limits.valid || create.isPending) return;
    create.mutate({ ...limits.request, platformKeys });
  };

  const onOpenChange = (next: boolean) => {
    if (!next) onClose();
  };

  if (link !== null) return <LinkCreatedDialog link={link} onOpenChange={onOpenChange} />;

  return (
    <Dialog
      open
      onOpenChange={onOpenChange}
      title={t("access.newLink")}
      closeOnOutsideClick={false}
      footer={
        <>
          <DialogClose>
            <Button variant="ghost">{t("common.cancel")}</Button>
          </DialogClose>
          <Button type="submit" form={formId} variant="primary" disabled={!limits.valid} loading={create.isPending}>
            {t("access.createLink")}
          </Button>
        </>
      }
    >
      <form id={formId} className="gx-admin__form" onSubmit={submit} noValidate>
        <LinkLabelField limits={limits} />
        <LinkUseFields limits={limits} />
        <div className="gx-admin__switch">
          <Switch
            label={t("access.mayUseKeys")}
            aria-describedby={keysHintId}
            checked={platformKeys}
            disabled={open}
            onCheckedChange={setPlatformKeys}
          />
          <span className="gx-admin__hint" id={keysHintId}>
            {t(open ? "access.keysOpenMode" : "access.mayUseKeysHint")}
          </span>
        </div>
        <LinkPinField limits={limits} />
        {create.isError && (
          <Alert tone="danger" title={t("access.couldntCreateLink")}>
            {errorMessage(create.error)}
          </Alert>
        )}
      </form>
    </Dialog>
  );
}
