import { useState, type ReactNode } from "react";
import { timestampDate } from "@bufbuild/protobuf/wkt";
import { ExternalLink, RefreshCw, Trash2 } from "lucide-react";

import { HealthStatus, type ProviderCredential, type ProviderHealth } from "@gen/glyphoxa/management/v1/management_pb";
import { Card } from "@/components/ui/Card";
import { Badge } from "@/components/ui/Badge";
import { Input } from "@/components/ui/Input";
import { Combobox } from "@/components/ui/Combobox";
import { Button } from "@/components/ui/Button";
import { ConfirmDialog } from "@/components/ui/ConfirmDialog";
import { errorMessage } from "@/lib/connectError";
import { useI18n, type MessageKey } from "@/i18n";

// SecretRow renders one write-only credential (ADR-0004): what the service
// does, where a key comes from, and one of three states.
//
//   - a saved key: "•••• 1234 · saved 3 Nov 2026" and Replace (plus Remove key
//     on a voice LLM slot, ADR-0004 amendment 2026-11-26);
//   - a slot the table's plan provides: "Included with your plan" and a
//     "Use my own key" link that opens the key field, never an empty password
//     field the GM might think they must fill;
//   - otherwise the key field and Save.
//
// The status badge starts from key presence and upgrades once the health
// probe answers (#70). While the probe is out a running slot reads
// "Checking…", never a verdict it has not got.

export function SecretRow({
  icon,
  kind,
  name,
  description,
  getKeyUrl,
  placeholder,
  credential,
  health,
  healthPending = false,
  models,
  optional = false,
  voiceLlm,
  anchor,
  onSave,
  onRemove,
}: {
  icon: ReactNode;
  kind: string;
  name: string;
  /** One line under the name: what the service does at the table. */
  description?: string;
  /** Where a key for this service is created (opens in a new tab). */
  getKeyUrl?: string;
  placeholder: string;
  credential?: ProviderCredential;
  health?: ProviderHealth;
  /** The health probe has not answered yet. */
  healthPending?: boolean;
  models?: string[];
  /** An unsaved key reads "Optional" rather than "Key needed". */
  optional?: boolean;
  /**
   * Set on a voice-LLM slot (#702): whether this is the LLM in use. The row
   * then says so, and a slot that is NOT in use but has a key to run on — the
   * tenant's saved one, or the platform's — offers "Use for voice".
   */
  voiceLlm?: { inEffect: boolean };
  /** The row's DOM id, the target of a Setup checklist link. */
  anchor?: string;
  /**
   * activate (voice-LLM slots only) makes the slot the LLM in use without a
   * new key: the server keeps the stored key, or rides the platform's.
   */
  onSave: (secret: string, model?: string, activate?: boolean) => Promise<unknown>;
  /** Removes the saved key (voice LLM slots); offered behind a confirmation. */
  onRemove?: () => Promise<unknown>;
}) {
  const { t, lang } = useI18n();
  const [editing, setEditing] = useState(false);
  const [value, setValue] = useState("");
  const [busy, setBusy] = useState(false);
  // A failed write, with the sentence it is told in (a save or a removal).
  const [saveError, setSaveError] = useState<{ key: MessageKey; message: string } | null>(null);
  const [model, setModel] = useState<string | undefined>(undefined);
  const [confirmRemove, setConfirmRemove] = useState(false);

  const saved = Boolean(credential?.showMasked);
  const platform = Boolean(credential?.providedByPlatform);
  const masked = saved && !editing;
  // An included slot shows the plan's line until the GM asks for the key field.
  const included = !saved && platform && !editing;
  // The combobox shows the operator's pick, the saved model, or the catalog
  // default (first), in that order.
  const selectedModel = model ?? (credential?.model || undefined) ?? models?.[0];
  // A voice-LLM slot the platform provides has a model to choose too, with no
  // key of the tenant's own behind it.
  const canRun = saved || (Boolean(voiceLlm) && platform);
  // "Use for voice" (#702): this slot could run the tenant's sessions and does
  // not. It carries the picked model along, so it replaces "Save model" here.
  const canUse = Boolean(voiceLlm) && !voiceLlm?.inEffect && canRun;
  // "Save model" appears only when the operator actively picked something
  // different from the stored model (#227) — never for the passive
  // catalog-default display, which stores nothing.
  const modelDirty =
    canRun && !canUse && model !== undefined && model !== (credential?.model || undefined);

  async function run(fn: () => Promise<unknown>, after?: () => void, failKey: MessageKey = "common.couldntSave") {
    if (busy) return;
    setBusy(true);
    setSaveError(null);
    try {
      await fn();
      after?.();
    } catch (err) {
      // A rejected save must leave visible evidence — the key was NOT stored (#154).
      setSaveError({ key: failKey, message: errorMessage(err) });
    } finally {
      setBusy(false);
    }
  }

  // Switch the voice LLM to this slot (#702): no secret on the wire, the
  // stored (or platform) key stays; an actively picked or stored model rides
  // along, the passive catalog default does not.
  const handleUse = () =>
    run(
      () => onSave("", model ?? (credential?.model || ""), true),
      () => setModel(undefined),
    );

  // Model-only save (#227): empty secret tells the server to keep the sealed
  // key verbatim and update just the model. A platform-provided voice-LLM slot
  // has no row to update until the tenant makes a choice: activate creates it.
  const handleSaveModel = () => {
    if (!selectedModel) return;
    void run(
      () => (voiceLlm && !saved ? onSave("", selectedModel, true) : onSave("", selectedModel)),
      () => setModel(undefined),
    );
  };

  const handleSave = () => {
    if (!value) return;
    // Only an actively picked or already stored model rides along: the
    // combobox's passive first entry is a display default, and storing it
    // would pin the row to today's default (#702).
    void run(
      () => onSave(value, models ? (model ?? (credential?.model || "")) : undefined),
      () => {
        setValue("");
        setEditing(false);
      },
    );
  };

  const cancelEdit = () => {
    setEditing(false);
    setValue("");
    // Back to the masked or included state — a stale "Couldn't save" alert
    // would contradict it (#154).
    setSaveError(null);
  };

  const savedOn = credential?.updatedAt
    ? timestampDate(credential.updatedAt).toLocaleDateString(lang, {
        day: "numeric",
        month: "short",
        year: "numeric",
      })
    : undefined;

  return (
    <Card id={anchor} tabIndex={anchor ? -1 : undefined} className="gx-provider-card">
      <div className="gx-provider-row">
        <span className="gx-provider-row__icon" aria-hidden="true">
          {icon}
        </span>
        <div className="gx-provider-row__meta">
          <div className="gx-overline">{kind}</div>
          <div className="gx-provider-row__name">
            {name}
            {voiceLlm?.inEffect && (
              <Badge variant="info" size="sm">
                {t("config.badgeInUse")}
              </Badge>
            )}
          </div>
          {description && <div className="gx-provider-row__desc">{description}</div>}
          {getKeyUrl && (
            <a className="gx-provider-row__getkey" href={getKeyUrl} target="_blank" rel="noopener noreferrer">
              {t("config.getKey", { name })}
              <ExternalLink size={12} aria-hidden="true" />
              <span className="gx-visually-hidden">{t("shell.opensInNewTab")}</span>
            </a>
          )}
          {health?.botTag && (
            <div className="gx-provider-row__tag">{t("config.connectedAs", { tag: health.botTag })}</div>
          )}
        </div>

        <HealthBadge
          saved={saved}
          health={health}
          pending={healthPending}
          optional={optional}
          platform={platform}
        />
      </div>

      {/* Rendered whenever this slot HAS a model concept, even with an empty
          catalog — a hard transport failure must not take free-text entry
          down with it (#227): allowCustom still accepts any id. */}
      {models && (
        <div className="gx-provider-row__model">
          <Combobox
            aria-label={t("config.modelAria", { name })}
            options={models.map((m) => ({ value: m, label: m }))}
            value={selectedModel}
            onValueChange={setModel}
            placeholder={t("config.modelPlaceholder")}
            searchPlaceholder={t("config.modelSearchPlaceholder")}
            allowCustom
          />
          {modelDirty && (
            <Button variant="secondary" size="sm" disabled={busy} onClick={handleSaveModel}>
              {t("config.saveModel")}
            </Button>
          )}
          {canUse && (
            <Button variant="secondary" size="sm" disabled={busy} onClick={() => void handleUse()}>
              {t("config.useForVoice")}
            </Button>
          )}
        </div>
      )}

      <div className="gx-secret">
        {masked ? (
          <div className="gx-secret__saved">
            <span className="gx-secret__mask" aria-label={t("config.secretSavedAria", { name })}>
              {credential?.last4 ? `•••• ${credential.last4}` : "••••••••"}
            </span>
            {savedOn && <span className="gx-secret__date">{t("config.savedOn", { date: savedOn })}</span>}
            <span className="gx-secret__actions">
              <Button
                variant="secondary"
                size="sm"
                iconStart={<RefreshCw size={13} />}
                onClick={() => {
                  setEditing(true);
                  setValue("");
                  setSaveError(null); // fresh edit starts without a stale cue
                }}
              >
                {t("config.replace")}
              </Button>
              {onRemove && (
                <Button
                  variant="ghost"
                  size="sm"
                  iconStart={<Trash2 size={13} />}
                  disabled={busy}
                  onClick={() => setConfirmRemove(true)}
                >
                  {t("config.removeKey")}
                </Button>
              )}
            </span>
          </div>
        ) : included ? (
          <div className="gx-secret__included">
            <span>{t("config.includedWithPlan")}</span>
            <Button variant="ghost" size="sm" onClick={() => setEditing(true)}>
              {t("config.useOwnKey")}
            </Button>
          </div>
        ) : (
          <div className="gx-secret__edit">
            <Input
              type="password"
              placeholder={placeholder}
              aria-label={t("config.secretKeyAria", { name })}
              value={value}
              autoFocus={editing}
              onChange={(e) => setValue(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter" && value && !busy) {
                  e.preventDefault();
                  handleSave();
                }
              }}
            />
            <Button variant="primary" size="sm" onClick={handleSave} disabled={!value || busy}>
              {t("common.save")}
            </Button>
            {editing && (saved || platform) && (
              <Button variant="ghost" size="sm" onClick={cancelEdit}>
                {t("common.cancel")}
              </Button>
            )}
          </div>
        )}
        {/* A key saved into the voice-LLM slot NOT in use makes it the newest
            LLM row, i.e. the voice model: say so before the save (#702). */}
        {voiceLlm && !voiceLlm.inEffect && !masked && !included && (
          <span className="gx-secret__hint">{t("config.keySwitchesVoice", { name })}</span>
        )}
        {saveError && (
          <span className="gx-secret__error" role="alert">
            {t(saveError.key, { message: saveError.message })}
          </span>
        )}
      </div>

      {onRemove && (
        <ConfirmDialog
          open={confirmRemove}
          onOpenChange={setConfirmRemove}
          title={t("config.removeKeyTitle", { name })}
          description={t("config.removeKeyDesc", { name })}
          confirmLabel={t("config.removeKey")}
          onConfirm={() => {
            setConfirmRemove(false);
            void run(onRemove, undefined, "config.couldntRemove");
          }}
        />
      )}
    </Card>
  );
}

// HealthBadge renders the status dot. An unsaved slot is "Key needed"
// (presence), or a neutral "Optional" when the slot isn't required to run a
// session. A saved slot reads "Checking…" while the probe is out, then
// "Working" or "Having trouble" once GetProviderHealth reports the test-call
// (#70); "Saved" when no verdict came back. An unsaved slot the deployment
// provides for this tenant (W9) reads "Included": the probe runs on the
// platform's key there too, so a failed probe still downgrades it.
function HealthBadge({
  saved,
  health,
  pending,
  optional,
  platform,
}: {
  saved: boolean;
  health?: ProviderHealth;
  pending: boolean;
  optional: boolean;
  platform: boolean;
}) {
  const { t } = useI18n();
  if (!saved && !platform) {
    return optional ? (
      <Badge variant="neutral" dot size="sm">
        {t("config.badgeOptional")}
      </Badge>
    ) : (
      <Badge variant="warning" dot size="sm">
        {t("config.badgeKeyNeeded")}
      </Badge>
    );
  }
  if (pending && !health) {
    return (
      <Badge variant="neutral" dot size="sm">
        {t("config.badgeChecking")}
      </Badge>
    );
  }
  if (health?.status === HealthStatus.DEGRADED) {
    // The failure reason renders VISIBLY under the badge — a title-attr
    // tooltip is unreachable by keyboard and on touch.
    const detail = healthDetail(health, t);
    return (
      <span className="gx-health">
        <Badge variant="danger" dot size="sm">
          {t("config.badgeDegraded")}
        </Badge>
        {detail && <span className="gx-health__detail">{detail}</span>}
      </span>
    );
  }
  if (!saved) {
    return (
      <Badge variant="info" dot size="sm">
        {t("config.badgeIncluded")}
      </Badge>
    );
  }
  if (health?.status !== HealthStatus.HEALTHY) {
    return (
      <Badge variant="neutral" dot size="sm">
        {t("config.badgeSaved")}
      </Badge>
    );
  }
  return (
    <Badge variant="success" dot size="sm">
      {t("config.badgeWorking")}
    </Badge>
  );
}

// The stable failure classes GetProviderHealth reports (ProviderHealth.code),
// each with its own translated sentence. An unknown code (a newer server)
// falls back to the server's English detail.
const HEALTH_CODE_KEYS: Record<string, MessageKey> = {
  missing_key: "config.healthMissingKey",
  key_rejected: "config.healthKeyRejected",
  quota_exhausted: "config.healthQuotaExhausted",
  unreachable: "config.healthUnreachable",
  plan_excludes_platform_keys: "config.healthNoPlatformKeys",
  check_failed: "config.healthCheckFailed",
};

function healthDetail(health: ProviderHealth, t: (key: MessageKey) => string): string {
  const key = HEALTH_CODE_KEYS[health.code];
  return key ? t(key) : health.detail;
}
