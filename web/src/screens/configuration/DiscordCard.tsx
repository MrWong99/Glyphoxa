import { useId, useState, type ReactNode } from "react";
import { useMutation } from "@connectrpc/connect-query";
import { toast } from "sonner";
import { MessagesSquare, Server, ChevronRight } from "lucide-react";

import {
  ProviderService,
  type ListProviderConfigsResponse,
  type ProviderCredential,
  type ProviderHealth,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Card } from "@/components/ui/Card";
import { AdvancedCard } from "@/components/ui/AdvancedCard";
import { Alert } from "@/components/ui/Alert";
import { Badge } from "@/components/ui/Badge";
import { Button } from "@/components/ui/Button";
import { ConfirmDialog } from "@/components/ui/ConfirmDialog";
import { Input } from "@/components/ui/Input";
import { errorMessage } from "@/lib/connectError";
import { useI18n } from "@/i18n";
import { AddBotLink } from "./AddBotLink";
import { DiscordLinkAutofill } from "./DiscordLinkAutofill";
import { SecretRow } from "./SecretRow";

// The Discord connection, in the order a GM does it (#695): step 1 adds
// Glyphoxa to the server (the bot-authorization link), step 2 links the
// server — paste an invite or channel link, then press "Link {server}". There
// is no auto-save: linking is a first-registrar claim behind a guild-admin
// proof (ADR-0058), so it happens on an explicit press, and a refusal stays
// visible on the form. The numeric Server ID sits behind a disclosure for the
// GM who has one; a table's own bot token is folded away entirely, because a
// central-Bot table (ADR-0057) never needs one.
//
// Once a server is linked the form gives way to a summary naming it (stored
// at link time, ADR-0004 amendment 2026-11-26), with Change and Unlink.

/** The server the form would link: from a pasted link, or typed by hand. */
type Candidate = { id: string; name?: string; from: "paste" | "manual" };

export function DiscordCard({
  config,
  botCredential,
  botHealth,
  healthPending,
  onChanged,
}: {
  config: ListProviderConfigsResponse;
  botCredential?: ProviderCredential;
  botHealth?: ProviderHealth;
  healthPending: boolean;
  /** Drops the reads a Discord change affects (the list, health, channels). */
  onChanged: () => void;
}) {
  const { t } = useI18n();
  const linked = config.guildId !== "";
  const linkedName = config.guildName || "";
  const [changing, setChanging] = useState(false);
  const [confirmUnlink, setConfirmUnlink] = useState(false);

  const saveBot = useMutation(ProviderService.method.saveDiscordSettings, { onSuccess: onChanged });
  const release = useMutation(ProviderService.method.releaseDiscordGuild, {
    onSuccess: () => {
      toast.success(t("config.toastUnlinked", { server: linkedName || config.guildId }));
      setChanging(false);
      onChanged();
    },
    onError: (err) => toast.error(t("config.couldntUnlink", { message: errorMessage(err) })),
  });

  return (
    <div className="gx-discord-card">
      <Card>
        {linked && !changing ? (
          <div className="gx-discord-linked">
            <span className="gx-provider-row__icon" aria-hidden="true">
              <Server size={19} />
            </span>
            <div className="gx-discord-linked__meta">
              <span className="gx-overline">{t("config.linkedServer")}</span>
              <span className="gx-discord-linked__name" data-testid="linked-server">
                {linkedName || t("config.serverIdValue", { id: config.guildId })}
              </span>
              {linkedName && (
                <span className="gx-discord-linked__id">{t("config.serverIdValue", { id: config.guildId })}</span>
              )}
              <IntegrationBadge state={config.integrationState} detail={config.integrationDetail} />
            </div>
            <div className="gx-discord-linked__actions">
              <Button variant="secondary" size="sm" onClick={() => setChanging(true)}>
                {t("config.changeServer")}
              </Button>
              <Button variant="ghost" size="sm" loading={release.isPending} onClick={() => setConfirmUnlink(true)}>
                {t("config.unlinkServer")}
              </Button>
            </div>
          </div>
        ) : (
          <LinkForm
            status={<IntegrationBadge state={config.integrationState} detail={config.integrationDetail} />}
            config={config}
            onCancel={linked ? () => setChanging(false) : undefined}
            onLinked={() => {
              setChanging(false);
              onChanged();
            }}
          />
        )}
      </Card>

      {/* A table running its own bot (ADR-0057's BYOK Bot) — the one case
          that needs a token. Open from the start once a token is saved, so
          the masked key and its health are not hidden. */}
      <AdvancedCard
        title={t("config.ownBotTitle")}
        hint={t("config.ownBotHint")}
        defaultOpen={Boolean(botCredential?.showMasked)}
      >
        <SecretRow
          icon={<MessagesSquare size={19} />}
          kind={t("config.kindBot")}
          name={t("config.botTokenName")}
          description={t("config.botTokenHint")}
          getKeyUrl="https://discord.com/developers/applications"
          optional
          placeholder={t("config.pasteBotToken")}
          credential={botCredential}
          health={botHealth}
          healthPending={healthPending}
          // Token-only save: the ID fields stay OFF the wire (they have proto3
          // presence), so replacing the token can never clobber the stored IDs (#142).
          onSave={(secret) => saveBot.mutateAsync({ botToken: secret })}
        />
      </AdvancedCard>

      <ConfirmDialog
        open={confirmUnlink}
        onOpenChange={setConfirmUnlink}
        title={t("config.unlinkTitle", { server: linkedName || config.guildId })}
        description={t("config.unlinkNote")}
        confirmLabel={t("config.unlinkServer")}
        onConfirm={() => {
          setConfirmUnlink(false);
          // The request echoes the bound guild_id: the server compare-and-clears.
          release.mutate({ guildId: config.guildId });
        }}
      />
    </div>
  );
}

// LinkForm is the two numbered steps. Linking sends guild_id alone: the
// server proves the GM administers the server, stores its name and keeps the
// Default Voice Channel only when the server did not change.
function LinkForm({
  status,
  config,
  onCancel,
  onLinked,
}: {
  /** The standing bot's health, shown above the steps (a rejected own token). */
  status: ReactNode;
  config: ListProviderConfigsResponse;
  /** Set while changing an already linked server: offers Cancel. */
  onCancel?: () => void;
  onLinked: () => void;
}) {
  const { t } = useI18n();
  const manualId = useId();
  const [candidate, setCandidate] = useState<Candidate | null>(null);
  const [manualOpen, setManualOpen] = useState(false);
  const [manual, setManual] = useState("");

  const link = useMutation(ProviderService.method.saveDiscordSettings, {
    onSuccess: (_res, req) => {
      const name = candidate?.id === req.guildId ? candidate?.name : undefined;
      toast.success(t("config.toastLinked", { server: name || req.guildId || "" }));
      setCandidate(null);
      setManual("");
      onLinked();
    },
  });

  const id = candidate?.id ?? "";
  // Nothing to link, or the server that is already linked: Link stays off.
  const unchanged = id === "" || id === config.guildId;
  const submit = () => {
    if (unchanged || link.isPending) return;
    link.mutate({ guildId: id });
  };

  return (
    <div className="gx-discord">
      {status && <div className="gx-discord__status">{status}</div>}
      <ol className="gx-discord-steps">
        <li className="gx-discord-step">
          <span className="gx-discord-step__num" aria-hidden="true">
            1
          </span>
          <div className="gx-discord-step__body">
            <h3 className="gx-discord-step__title">{t("config.discordStep1Title")}</h3>
            <AddBotLink applicationId={config.discordApplicationId} variant="primary" />
          </div>
        </li>
        <li className="gx-discord-step">
          <span className="gx-discord-step__num" aria-hidden="true">
            2
          </span>
          <div className="gx-discord-step__body">
            <h3 className="gx-discord-step__title">{t("config.discordStep2Title")}</h3>
            <p className="gx-discord-step__copy">{t("config.discordStep2Copy")}</p>
            <DiscordLinkAutofill
              onFill={(guildId, guildName) => {
                link.reset();
                setCandidate({ id: guildId, name: guildName, from: "paste" });
              }}
              // A typed Server ID stands; only a pasted server goes with its paste.
              onClear={() => setCandidate((c) => (c?.from === "paste" ? null : c))}
            />

            <button
              type="button"
              className="gx-disclosure"
              aria-expanded={manualOpen}
              aria-controls={manualId}
              onClick={() => setManualOpen((o) => !o)}
            >
              <ChevronRight size={14} className="gx-disclosure__chevron" data-open={manualOpen || undefined} aria-hidden="true" />
              {t("config.enterServerId")}
            </button>
            {manualOpen && (
              <div id={manualId} className="gx-discord__ids">
                <Input
                  // The wire field stays guild_id; users only ever see "Server ID".
                  label={t("config.serverIdLabel")}
                  placeholder={t("config.serverIdPlaceholder")}
                  hint={t("config.serverIdHint")}
                  value={manual}
                  // A snowflake is digits — the numeric keyboard on touch.
                  inputMode="numeric"
                  autoComplete="off"
                  onChange={(e) => {
                    const v = e.target.value.trim();
                    setManual(e.target.value);
                    link.reset();
                    setCandidate(v === "" ? null : { id: v, from: "manual" });
                  }}
                  onKeyDown={(e) => {
                    if (e.key === "Enter") {
                      e.preventDefault();
                      submit();
                    }
                  }}
                />
              </div>
            )}

            {link.isError && (
              <Alert tone="danger" title={t("config.couldntLinkTitle")}>
                {errorMessage(link.error)}
              </Alert>
            )}

            <div className="gx-discord__save">
              {onCancel && (
                <Button variant="ghost" size="sm" onClick={onCancel} disabled={link.isPending}>
                  {t("common.cancel")}
                </Button>
              )}
              <Button variant="primary" size="sm" onClick={submit} disabled={unchanged} loading={link.isPending}>
                {candidate?.name
                  ? t("config.linkServerNamed", { server: candidate.name })
                  : t("config.linkServer")}
              </Button>
            </div>
            <p className="gx-discord-step__note">{t("config.linkNeedsManageServer")}</p>
          </div>
        </li>
      </ol>
    </div>
  );
}

// IntegrationBadge renders THIS tenant's standing Discord client health (#489):
// "ok" (client up), "waiting" (no Bot token yet), "reconnecting" (#642) or
// "failed" (the token was rejected terminally), the last two with a detail.
// Empty state (web-only mode) and any state this build does not know render
// nothing — only an explicit "ok" may claim the Bot is connected.
function IntegrationBadge({ state, detail }: { state: string; detail: string }) {
  const { t } = useI18n();
  if (state === "reconnecting" || state === "failed") {
    return (
      <span className="gx-health">
        <Badge variant={state === "failed" ? "danger" : "warning"} dot size="sm">
          {t(state === "failed" ? "config.discordIntegrationFailed" : "config.discordIntegrationReconnecting")}
        </Badge>
        {detail && <span className="gx-health__detail">{detail}</span>}
      </span>
    );
  }
  if (state !== "ok") return null;
  return (
    <Badge variant="success" dot size="sm">
      {t("config.botConnected")}
    </Badge>
  );
}
