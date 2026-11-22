import { useEffect, useState } from "react";
import { useQuery } from "@connectrpc/connect-query";
import { MessageCircle } from "lucide-react";

import {
  VoiceService,
  ChatLlmKeySource,
  ChatLlmSource,
  type EffectiveChatLlm,
  type EffectiveLlm,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Card } from "@/components/ui/Card";
import { Select } from "@/components/ui/Select";
import { Combobox } from "@/components/ui/Combobox";
import { Input } from "@/components/ui/Input";
import { Button } from "@/components/ui/Button";
import { errorMessage } from "@/lib/connectError";
import { useI18n, type MessageKey } from "@/i18n";
import { CHAT_MODEL_ANCHOR } from "./chatModelAnchor";
import { llmProviderLabel } from "./llmProviders";

// ChatModelCard is the Planning chat model choice on the Setup screen
// (ADR-0062 amendment): the chat's model is a CHOICE — "same as voice", or a
// provider and a model of its own — not a key slot. It reuses keys the table
// already has (its own key for that provider, or the plan's), so nobody pastes
// a key twice; a key field appears only when the chosen provider has none, or
// behind "use a separate key".
//
// Everything the card says about what answers comes from the server's
// effective_chat_llm, the same resolution the chat engine runs. The pending
// selection never rewrites that line: until Save, it still names what is
// actually stored.

/** The select value for "no chat choice": the chat follows the voice model. */
const SAME_AS_VOICE = "voice";

const OPTION_KEYS: Record<number, MessageKey> = {
  [ChatLlmKeySource.CHAT_KEY]: "chat.configOptionOwnKey",
  [ChatLlmKeySource.PROVIDER_KEY]: "chat.configOptionOwnKey",
  [ChatLlmKeySource.PLATFORM]: "chat.configOptionIncluded",
  [ChatLlmKeySource.NONE]: "chat.configOptionNeedsKey",
};

export function ChatModelCard({
  effective,
  voice,
  loadError,
  onSave,
  onReset,
}: {
  /** The Planning chat LLM in effect; undefined while the Setup read is pending. */
  effective?: EffectiveChatLlm;
  /** The voice LLM in effect, named in the "Same as voice" option. */
  voice?: EffectiveLlm;
  /** The Setup read failed: the card says so instead of showing a skeleton forever. */
  loadError?: unknown;
  /** Stores the choice. An empty model is the provider's chat default; an empty secret stores no key. */
  onSave: (provider: string, model: string, secret: string) => Promise<unknown>;
  /** Removes the choice: back to "same as voice". */
  onReset: () => Promise<unknown>;
}) {
  const { t } = useI18n();
  // The pending edit. Each is undefined until the GM touches the control, so a
  // refetch after a save shows the stored state without an effect to reseed it.
  const [choice, setChoice] = useState<string | undefined>(undefined);
  const [model, setModel] = useState<string | undefined>(undefined);
  const [secret, setSecret] = useState("");
  const [separateKey, setSeparateKey] = useState(false);
  const [busy, setBusy] = useState(false);
  const [saveError, setSaveError] = useState<string | null>(null);

  // Arriving through the Planning panel's "Change" link (Setup#planning-chat):
  // the screen is a lazily loaded chunk, so the browser's own jump to the
  // fragment ran before this card existed. Bring it into view once it does.
  useEffect(() => {
    if (window.location.hash === `#${CHAT_MODEL_ANCHOR}`) {
      document.getElementById(CHAT_MODEL_ANCHOR)?.scrollIntoView({ block: "center" });
    }
  }, []);

  const stored = effective?.source === ChatLlmSource.CHOICE ? effective.provider : SAME_AS_VOICE;
  const selected = choice ?? stored;
  const provider = selected === SAME_AS_VOICE ? "" : selected;
  const option = effective?.providers.find((p) => p.provider === provider);
  const onStored = provider !== "" && provider === stored;

  // The chat catalog of the selected provider: fetched with the key a chat
  // choice of it would run on, the chat default first, priced models only on
  // the plan's key. The server never fails this read (it degrades to a short
  // priced list), and the combobox takes free text either way.
  const models = useQuery(
    VoiceService.method.listModels,
    { provider, slot: "chat" },
    { enabled: provider !== "" },
  );
  const catalog = models.data?.models ?? [];

  const storedModel = onStored ? (effective?.storedModel ?? "") : "";
  // What the combobox shows: the GM's pick, else the stored model, else the
  // provider's chat default — a display default that is never sent.
  const shownModel = model ?? (storedModel || undefined) ?? (option?.defaultModel || catalog[0]);
  // What a save sends: an active pick or the stored model. "" asks the server
  // for the provider's chat default and keeps the choice on it as it changes.
  const modelToSend = model ?? storedModel;

  const needsKey = provider !== "" && option?.keySource === ChatLlmKeySource.NONE;
  const showKeyField = provider !== "" && (needsKey || separateKey);
  // A chat-only key lives on the chat row: leaving that provider removes it.
  const dropsChatKey = effective?.keySource === ChatLlmKeySource.CHAT_KEY && selected !== stored;

  const dirty =
    selected !== stored || (provider !== "" && modelToSend !== storedModel) || (showKeyField && secret !== "");
  // A provider with no key cannot be switched TO without one; a choice already
  // on it stays editable (the model can still be changed while the key is fixed).
  const blocked = needsKey && !onStored && secret === "";

  function selectChoice(next: string) {
    setChoice(next);
    // The model and the key belong to the provider they were entered for.
    setModel(undefined);
    setSecret("");
    setSeparateKey(false);
    setSaveError(null);
  }

  async function handleSave() {
    if (busy || !dirty || blocked) return;
    setBusy(true);
    setSaveError(null);
    try {
      if (provider === "") await onReset();
      else await onSave(provider, modelToSend, showKeyField ? secret : "");
      // The refetched read now carries the saved state: drop the pending edit.
      setChoice(undefined);
      setModel(undefined);
      setSecret("");
      setSeparateKey(false);
    } catch (err) {
      // A refused save must leave visible evidence that nothing was stored.
      setSaveError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  const header = (
    <div className="gx-chat-model__head">
      <span className="gx-provider-row__icon">
        <MessageCircle size={19} />
      </span>
      <div className="gx-chat-model__meta">
        <div className="gx-overline">{t("config.kindPlanningChat")}</div>
        <div className="gx-provider-row__name">{t("chat.configSlotName")}</div>
        <div className="gx-provider-row__desc">{t("chat.configSlotDesc")}</div>
      </div>
    </div>
  );

  if (!effective) {
    return (
      <Card id={CHAT_MODEL_ANCHOR}>
        <div className="gx-chat-model">
          {header}
          {loadError ? (
            <p className="gx-chat-model__error" role="alert">
              {t("chat.configLoadError", { message: errorMessage(loadError) })}
            </p>
          ) : (
            <span className="gx-skeleton" data-testid="chat-model-loading" />
          )}
        </div>
      </Card>
    );
  }

  const providerLabel = llmProviderLabel(effective.provider);
  const options = [
    {
      value: SAME_AS_VOICE,
      label: voice
        ? t("chat.configSameAsVoice", { provider: llmProviderLabel(voice.provider), model: voice.model })
        : t("chat.configSameAsVoiceBare"),
    },
    ...effective.providers.map((p) => ({
      value: p.provider,
      label: t(OPTION_KEYS[p.keySource] ?? "chat.configOptionNeedsKey", { provider: llmProviderLabel(p.provider) }),
    })),
  ];

  return (
    <Card id={CHAT_MODEL_ANCHOR}>
      <div className="gx-chat-model">
        {header}

        {/* What answers right now, from the server — never the pending edit. */}
        <div className="gx-chat-model__status" data-testid="chat-model-status">
          <p className="gx-chat-model__answers">
            {t("chat.configAnswersWith", { provider: providerLabel, model: effective.model })}
          </p>
          {effective.keySource === ChatLlmKeySource.NONE ? (
            <p className="gx-chat-model__warning" role="status">
              {t("chat.configKeyNone", { provider: providerLabel })}
            </p>
          ) : (
            <p className="gx-chat-model__note">
              {effective.keySource === ChatLlmKeySource.CHAT_KEY
                ? t("chat.configKeyChat", { last4: effective.keyLast4 })
                : effective.keySource === ChatLlmKeySource.PROVIDER_KEY
                  ? t("chat.configKeyProvider", { provider: providerLabel, last4: effective.keyLast4 })
                  : t("chat.configKeyPlatform")}
            </p>
          )}
          {effective.source === ChatLlmSource.VOICE && (
            <p className="gx-chat-model__note">{t("chat.configChainVoice")}</p>
          )}
          {effective.source === ChatLlmSource.PLATFORM_DEFAULT && (
            <p className="gx-chat-model__note">{t("chat.configChainDefault")}</p>
          )}
        </div>

        <div className="gx-chat-model__controls">
          <Select
            aria-label={t("chat.configProviderAria")}
            options={options}
            value={selected}
            onValueChange={selectChoice}
            disabled={busy}
          />
          {provider !== "" && (
            <Combobox
              aria-label={t("config.modelAria", { name: t("chat.configSlotName") })}
              options={catalog.map((m) => ({ value: m, label: m }))}
              value={shownModel}
              onValueChange={setModel}
              placeholder={t("config.modelPlaceholder")}
              searchPlaceholder={t("config.modelSearchPlaceholder")}
              allowCustom
              disabled={busy}
            />
          )}
        </div>

        {showKeyField && (
          <div className="gx-chat-model__key">
            <Input
              type="password"
              autoComplete="off"
              aria-label={t("chat.configKeyAria", { provider: llmProviderLabel(provider) })}
              placeholder={t("chat.configPasteKey", { provider: llmProviderLabel(provider) })}
              hint={t("chat.configKeyHint")}
              value={secret}
              onChange={(e) => setSecret(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter") {
                  e.preventDefault();
                  void handleSave();
                }
              }}
            />
          </div>
        )}

        <div className="gx-chat-model__actions">
          {provider !== "" && !needsKey && !separateKey && (
            <Button variant="ghost" size="sm" disabled={busy} onClick={() => setSeparateKey(true)}>
              {t("chat.configSeparateKey")}
            </Button>
          )}
          {separateKey && !needsKey && (
            <Button
              variant="ghost"
              size="sm"
              disabled={busy}
              onClick={() => {
                setSeparateKey(false);
                setSecret("");
              }}
            >
              {t("common.cancel")}
            </Button>
          )}
          {dirty && dropsChatKey && <span className="gx-chat-model__note">{t("chat.configResetDropsKey")}</span>}
          {dirty && !dropsChatKey && <span className="gx-chat-model__note">{t("chat.configUnsaved")}</span>}
          <Button variant="primary" size="sm" disabled={busy || !dirty || blocked} onClick={handleSave}>
            {t("chat.configSave")}
          </Button>
        </div>
        {saveError && (
          <p className="gx-chat-model__error" role="alert">
            {t("common.couldntSave", { message: saveError })}
          </p>
        )}
      </div>
    </Card>
  );
}
