import { useId, useState, type FormEvent } from "react";
import { useMutation } from "@connectrpc/connect-query";
import { toast } from "sonner";

import { AccessStatus, AdminService, type AccessPerson } from "@gen/glyphoxa/management/v1/management_pb";
import { SNOWFLAKE_ERRORS } from "@/components/access/invitationParts";
import { Alert } from "@/components/ui/Alert";
import { Button } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { Input } from "@/components/ui/Input";
import { Switch } from "@/components/ui/Switch";
import { useI18n } from "@/i18n";
import { errorMessage } from "@/lib/connectError";
import { snowflakeProblem } from "@/lib/invitationLink";
import { useRefreshOverview } from "./refreshOverview";

// "Add a person": an Access Grant by Discord User ID (ADR-0065). The grant is
// keyed by the snowflake, so it can be written before the person has ever
// signed in — which is exactly when an admin wants to write it.
//
// "May use the platform's API keys" starts OFF: an admitted person brings their
// own provider keys unless the admin decides the instance pays for them. The
// sentence under the switch says what turning it on costs.
//
// The form only ever ADDS. GrantAccess is an upsert: sent for someone who
// already holds a grant it would replace their note and set Platform Keys to
// whatever this form shows (OFF by default) on their live table, under a plain
// "access granted" toast. So an ID that is already in the People list as
// granted — or as a Platform Admin, whom a grant cannot change — is refused
// here with a sentence pointing at their row, where those things are changed
// on purpose.

/** Why this Discord User ID cannot be added: who already holds it, or null. */
function alreadyIn(people: readonly AccessPerson[], id: string): { person: AccessPerson; admin: boolean } | null {
  if (id === "") return null;
  const rows = people.filter((p) => p.discordUserId === id);
  const admin = rows.find((p) => p.status === AccessStatus.PLATFORM_ADMIN);
  if (admin) return { person: admin, admin: true };
  const granted = rows.find((p) => p.status === AccessStatus.GRANTED);
  return granted ? { person: granted, admin: false } : null;
}

export function AddPersonCard({ open, people }: { open: boolean; people: readonly AccessPerson[] }) {
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
  const existing = problem === null ? alreadyIn(people, id) : null;
  const existingMessage = existing
    ? t(existing.admin ? "access.alreadyAdmin" : "access.alreadyGranted", {
        name: existing.person.name || existing.person.discordUserId,
      })
    : null;
  const canSubmit = id !== "" && problem === null && existing === null;

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
            error={problem ? t(SNOWFLAKE_ERRORS[problem]) : existingMessage}
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
