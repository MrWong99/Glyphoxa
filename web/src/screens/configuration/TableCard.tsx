import { useState, type FormEvent } from "react";
import { useMutation, useQuery } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { Users } from "lucide-react";

import { AuthService } from "@gen/glyphoxa/management/v1/management_pb";
import { Card } from "@/components/ui/Card";
import { Alert } from "@/components/ui/Alert";
import { Button } from "@/components/ui/Button";
import { ErrorState } from "@/components/ui/ErrorState";
import { Input } from "@/components/ui/Input";
import { Skeleton } from "@/components/ui/Skeleton";
import { errorMessage } from "@/lib/connectError";
import { invalidateMethodQueries } from "@/lib/queryClient";
import { useI18n } from "@/i18n";
import { OPERATOR, isPlaceholder } from "@/screens/legal/operator";

// "Your table": the Tenant's name (RenameTenant, ADR-0055) and the way to
// delete it. Deleting a table is not self-service — it needs a cascade over
// every campaign, an end to its sessions and a decision on what the usage
// ledger keeps — so the card says whom to ask, with the operator's address
// from the Impressum.

export function TableCard() {
  const { t } = useI18n();
  const me = useQuery(AuthService.method.getCurrentUser, {});

  return (
    <Card className="gx-table-card">
      <div className="gx-plan-card__head">
        <span className="gx-provider-row__icon" aria-hidden="true">
          <Users size={19} />
        </span>
        <div>
          <span className="gx-overline">{t("config.tableOverline")}</span>
          <h2 className="gx-plan-card__name">{t("config.tableTitle")}</h2>
        </div>
      </div>
      {me.isPending ? (
        <Skeleton lines={2} />
      ) : me.isError ? (
        <ErrorState
          size="inline"
          message={errorMessage(me.error)}
          onRetry={() => void me.refetch()}
          retrying={me.isFetching}
        />
      ) : (
        // Keyed on the stored name, so a rename elsewhere reseeds the field.
        <RenameForm key={me.data.tenantName} current={me.data.tenantName} />
      )}
      <p className="gx-table-card__delete">
        {t("config.tableDeletePre")}
        <a href={deleteHref()}>{t("config.tableDeleteLink")}</a>
        {t("config.tableDeletePost")}
      </p>
    </Card>
  );
}

// The operator's address when the deployment has filled it in, else the
// Impressum page, which names the operator however it can be reached.
function deleteHref(): string {
  if (!OPERATOR.email || isPlaceholder(OPERATOR.email)) return "/imprint";
  return `mailto:${OPERATOR.email}?subject=${encodeURIComponent("Glyphoxa: delete my table")}`;
}

function RenameForm({ current }: { current: string }) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const [name, setName] = useState(current);
  const rename = useMutation(AuthService.method.renameTenant, {
    onSuccess: (res) => {
      toast.success(t("config.toastTableRenamed", { name: res.tenantName || name.trim() }));
      void invalidateMethodQueries(queryClient, AuthService.method.getCurrentUser);
    },
  });
  const trimmed = name.trim();
  const unchanged = trimmed === "" || trimmed === current;

  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (unchanged || rename.isPending) return;
    rename.mutate({ name: trimmed });
  };

  return (
    <form className="gx-table-card__form" onSubmit={submit}>
      <Input
        label={t("config.tableNameLabel")}
        hint={t("config.tableNameHint")}
        value={name}
        maxLength={200}
        onChange={(e) => {
          rename.reset();
          setName(e.target.value);
        }}
      />
      {rename.isError && (
        <Alert tone="danger" title={t("config.couldntRenameTable")}>
          {errorMessage(rename.error)}
        </Alert>
      )}
      <div className="gx-spendcaps__save">
        <Button type="submit" variant="primary" size="sm" disabled={unchanged} loading={rename.isPending}>
          {t("config.renameTable")}
        </Button>
      </div>
    </form>
  );
}
