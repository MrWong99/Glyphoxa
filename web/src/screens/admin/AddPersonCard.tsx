import { useId, useState, type FormEvent } from "react";
import { useMutation } from "@connectrpc/connect-query";
import { toast } from "sonner";

import { AdminService } from "@gen/glyphoxa/management/v1/management_pb";
import { Alert } from "@/components/ui/Alert";
import { Button } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { Input } from "@/components/ui/Input";
import { Switch } from "@/components/ui/Switch";
import { useI18n, type MessageKey } from "@/i18n";
import { errorMessage } from "@/lib/connectError";
import { snowflakeProblem, type SnowflakeProblem } from "@/lib/invitationLink";
import { useRefreshOverview } from "./refreshOverview";

// "Add a person": an Access Grant by Discord User ID (ADR-0065). The grant is
// keyed by the snowflake, so it can be written before the person has ever
// signed in — which is exactly when an admin wants to write it.
//
// "May use the platform's API keys" starts OFF: an admitted person brings their
// own provider keys unless the admin decides the instance pays for them. The
// sentence under the switch says what turning it on costs.

export const SNOWFLAKE_ERRORS: Record<SnowflakeProblem, MessageKey> = {
  digits: "access.discordUserIdDigits",
  length: "access.discordUserIdLength",
};

export function AddPersonCard({ open }: { open: boolean }) {
  const { t } = useI18n();
  const titleId = useId();
  const keysHintId = useId();
  const refresh = useRefreshOverview();

  const [discordUserId, setDiscordUserId] = useState("");
  const [note, setNote] = useState("");
  const [platformKeys, setPlatformKeys] = useState(false);

  const grant = useMutation(AdminService.method.grantAccess, {
    onSuccess: (_res, req) => {
      toast.success(t("access.toastGranted", { name: req.discordUserId ?? "" }));
      setDiscordUserId("");
      setNote("");
      setPlatformKeys(false);
      void refresh();
    },
  });

  const id = discordUserId.trim();
  const problem = snowflakeProblem(id);
  const canSubmit = id !== "" && problem === null;

  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (!canSubmit || grant.isPending) return;
    grant.mutate({ discordUserId: id, note: note.trim(), platformKeys });
  };

  return (
    <section className="gx-admin__section" aria-labelledby={titleId}>
      <h2 id={titleId} className="gx-section-title">
        {t("access.addTitle")}
      </h2>
      <p className="gx-admin__lede">{t("access.addLede")}</p>
      <Card className="gx-admin__card">
        <form className="gx-admin__form" onSubmit={submit} noValidate>
          <Input
            label={t("access.discordUserId")}
            hint={t("access.discordUserIdHint")}
            error={problem ? t(SNOWFLAKE_ERRORS[problem]) : null}
            inputMode="numeric"
            autoComplete="off"
            value={discordUserId}
            onChange={(e) => {
              grant.reset();
              setDiscordUserId(e.target.value);
            }}
          />
          <Input
            label={t("access.note")}
            hint={t("access.noteHint")}
            autoComplete="off"
            maxLength={200}
            value={note}
            onChange={(e) => setNote(e.target.value)}
          />
          <div className="gx-admin__switch">
            <Switch
              label={t("access.mayUseKeys")}
              aria-describedby={keysHintId}
              checked={platformKeys}
              // With open sign-up the plan decides who spends the Platform
              // Keys; a grant's choice would be recorded and never used.
              disabled={open}
              onCheckedChange={setPlatformKeys}
            />
            <span className="gx-admin__hint" id={keysHintId}>
              {t(open ? "access.keysOpenMode" : "access.mayUseKeysHint")}
            </span>
          </div>
          {grant.isError && (
            <Alert tone="danger" title={t("access.couldntGrant")}>
              {errorMessage(grant.error)}
            </Alert>
          )}
          <div className="gx-admin__submit">
            <Button type="submit" variant="primary" size="sm" disabled={!canSubmit} loading={grant.isPending}>
              {t("access.grant")}
            </Button>
          </div>
        </form>
      </Card>
    </section>
  );
}
