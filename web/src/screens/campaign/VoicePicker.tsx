import { useEffect, useMemo, useState } from "react";
import { AudioLines, Play, Search, Square } from "lucide-react";

import type { ListVoicesResponse, Voice } from "@gen/glyphoxa/management/v1/management_pb";
import { useScreenNav } from "@/app/screenNav";
import { Alert } from "@/components/ui/Alert";
import { Badge } from "@/components/ui/Badge";
import { Button } from "@/components/ui/Button";
import { Dialog, DialogClose } from "@/components/ui/Dialog";
import { EmptyState } from "@/components/ui/EmptyState";
import { IconButton } from "@/components/ui/IconButton";
import { Input } from "@/components/ui/Input";
import { QueryBoundary, type QueryLike } from "@/components/ui/QueryBoundary";
import { Skeleton } from "@/components/ui/Skeleton";
import { Spinner } from "@/components/ui/Spinner";
import { useI18n } from "@/i18n";
import { MAX_PREVIEW_CHARS, useVoicePreview, type VoicePreview } from "./voicePreview";

import "./cast.css";

// The voice picker: the whole catalog as a list the GM can LISTEN to. The
// Combobox in the editor names a voice; it cannot tell you what "Marcus" sounds
// like, and previewing meant picking each voice in turn. Here every row has its
// own play button, with a sample line the GM can type (an NPC's own greeting
// says more than a stock sentence).
//
// Three rules, all about money and noise:
//   - nothing plays by itself. A preview is paid text-to-speech, so a sample
//     starts on a button press and at no other time (not on hover, not on
//     selecting a row, not on opening the dialog);
//   - one sample at a time: starting another stops the one playing;
//   - closing the dialog stops the sample.
//
// Listening does not choose: the play button never changes the selection. A
// row is chosen by pressing it, and the choice only reaches the form on "Use
// this voice" — so the GM can audition five voices and still cancel.

/** The catalog read (VoiceService.ListVoices) as a query result. */
export type VoicesQuery = QueryLike<ListVoicesResponse>;

// "en-US" and "en" are the same language for the purpose of this filter.
function primarySubtag(code: string): string {
  return code.trim().toLowerCase().split(/[-_]/)[0] ?? "";
}

// The voice's language as a word in the GM's display language ("English",
// "Englisch"); the bare code when the runtime does not know it.
function languageName(code: string, displayLang: string): string {
  const tag = code.trim();
  if (tag === "") return "";
  try {
    return new Intl.DisplayNames([displayLang], { type: "language" }).of(tag) ?? tag;
  } catch {
    return tag;
  }
}

export function voiceName(v: Voice): string {
  return v.name || v.label || v.voiceId;
}

export function VoicePicker({
  open,
  onOpenChange,
  voicesQuery,
  value,
  onSelect,
  campaignLanguage,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  voicesQuery: VoicesQuery;
  /** The voice id the form currently holds ("" for none). */
  value: string;
  /** Called with the chosen voice id when the GM confirms. */
  onSelect: (voiceId: string) => void;
  /** The Active Campaign's language (BCP-47), for the language filter chip. */
  campaignLanguage?: string;
}) {
  const { t } = useI18n();
  const nav = useScreenNav();
  const preview = useVoicePreview();

  const [chosen, setChosen] = useState(value);
  const [query, setQuery] = useState("");
  const [languageOnly, setLanguageOnly] = useState(false);
  // The sample line is kept between openings: a GM comparing voices for
  // several NPCs keeps auditioning with the same sentence.
  const [sample, setSample] = useState("");

  // Each opening starts from the form's current voice with no filter. Adjusted
  // during render (not in an effect) so the dialog never paints the selection
  // of its previous opening.
  const [wasOpen, setWasOpen] = useState(open);
  if (open !== wasOpen) {
    setWasOpen(open);
    if (open) {
      setChosen(value);
      setQuery("");
      setLanguageOnly(false);
    }
  }

  // Closing (Escape, Cancel, the backdrop, "Use this voice") ends the sample,
  // and a failure from this opening is not carried into the next.
  const { reset } = preview;
  useEffect(() => {
    if (!open) reset();
  }, [open, reset]);

  const confirm = () => {
    if (chosen && chosen !== value) onSelect(chosen);
    onOpenChange(false);
  };

  return (
    <Dialog
      open={open}
      onOpenChange={onOpenChange}
      title={t("cast.voicePickerTitle")}
      description={t("cast.voicePickerDesc")}
      className="gx-cast-voices"
      footer={
        <>
          <DialogClose>
            <Button type="button" variant="ghost">{t("common.cancel")}</Button>
          </DialogClose>
          <Button type="button" onClick={confirm} disabled={!chosen || chosen === value}>
            {t("cast.voiceUse")}
          </Button>
        </>
      }
    >
      <QueryBoundary
        query={voicesQuery}
        skeleton={<Skeleton.Row count={5} avatar={false} />}
        errorTitle={t("cast.voiceCatalogErrorTitle")}
        isEmpty={(data) => data.voices.length === 0}
        empty={
          <EmptyState
            icon={<AudioLines size={22} />}
            title={t("cast.voiceCatalogEmptyTitle")}
            description={t("cast.voiceCatalogEmptyDesc")}
            action={
              <Button
                type="button"
                variant="secondary"
                size="sm"
                onClick={() => {
                  onOpenChange(false);
                  nav.goScreen("configuration");
                }}
              >
                {t("cast.openSetup")}
              </Button>
            }
          />
        }
      >
        {(data) => (
          <VoiceList
            voices={data.voices}
            value={value}
            chosen={chosen}
            onChoose={setChosen}
            query={query}
            onQuery={setQuery}
            languageOnly={languageOnly}
            onLanguageOnly={setLanguageOnly}
            sample={sample}
            onSample={setSample}
            campaignLanguage={campaignLanguage}
            preview={preview}
          />
        )}
      </QueryBoundary>
    </Dialog>
  );
}

function VoiceList({
  voices,
  value,
  chosen,
  onChoose,
  query,
  onQuery,
  languageOnly,
  onLanguageOnly,
  sample,
  onSample,
  campaignLanguage,
  preview,
}: {
  voices: Voice[];
  value: string;
  chosen: string;
  onChoose: (voiceId: string) => void;
  query: string;
  onQuery: (query: string) => void;
  languageOnly: boolean;
  onLanguageOnly: (on: boolean) => void;
  sample: string;
  onSample: (text: string) => void;
  campaignLanguage?: string;
  preview: VoicePreview;
}) {
  const { t, lang } = useI18n();

  const campaignPrimary = primarySubtag(campaignLanguage ?? "");
  // The chip is only offered when it can do something: the campaign has a
  // language and the catalog says what language its voices are.
  const chipOffered = campaignPrimary !== "" && voices.some((v) => v.language.trim() !== "");
  const filterByLanguage = chipOffered && languageOnly;

  const rows = useMemo(() => {
    const tokens = query.trim().toLocaleLowerCase().split(/\s+/).filter(Boolean);
    const matching = voices
      .map((v) => ({ voice: v, name: voiceName(v), language: languageName(v.language, lang) }))
      .filter((row) => {
        if (filterByLanguage && primarySubtag(row.voice.language) !== campaignPrimary) return false;
        if (tokens.length === 0) return true;
        const haystack = `${row.name} ${row.voice.label} ${row.language}`.toLocaleLowerCase();
        return tokens.every((token) => haystack.includes(token));
      });
    // The form's current voice leads, so the GM sees where they are starting
    // from; everything else keeps the catalog's own order.
    return [...matching.filter((r) => r.voice.voiceId === value), ...matching.filter((r) => r.voice.voiceId !== value)];
  }, [voices, query, filterByLanguage, campaignPrimary, value, lang]);

  return (
    <div className="gx-cast-voices__body">
      <div className="gx-cast-voices__controls">
        <Input
          type="search"
          label={t("cast.voiceSearchLabel")}
          icon={<Search />}
          value={query}
          onChange={(e) => onQuery(e.target.value)}
          placeholder={t("campaign.voiceSearch")}
          autoComplete="off"
        />
        <Input
          label={t("cast.voiceSampleLabel")}
          value={sample}
          onChange={(e) => onSample(e.target.value)}
          placeholder={t("cast.voiceSamplePlaceholder")}
          hint={t("cast.voiceSampleHint", { max: MAX_PREVIEW_CHARS })}
          maxLength={MAX_PREVIEW_CHARS}
          autoComplete="off"
        />
        {chipOffered && (
          <button
            type="button"
            className="gx-cast-chip"
            aria-pressed={languageOnly}
            onClick={() => onLanguageOnly(!languageOnly)}
          >
            {t("cast.voiceLangChip", { language: languageName(campaignLanguage ?? "", lang) })}
          </button>
        )}
      </div>

      {preview.error && (
        <Alert tone="danger">{t("campaign.voicePreviewError", { message: preview.error })}</Alert>
      )}

      {rows.length === 0 ? (
        <EmptyState
          size="inline"
          title={t("cast.voiceNoMatch")}
          action={
            <Button
              type="button"
              variant="secondary"
              size="sm"
              onClick={() => {
                onQuery("");
                onLanguageOnly(false);
              }}
            >
              {t("cast.voiceClearSearch")}
            </Button>
          }
        />
      ) : (
        <ul className="gx-cast-voices__list" role="list" aria-label={t("cast.voiceListAria")}>
          {rows.map(({ voice, name, language }) => {
            const active = preview.activeId === voice.voiceId;
            const loading = active && preview.phase === "loading";
            return (
              <li
                key={voice.voiceId}
                className="gx-cast-voices__row"
                data-chosen={chosen === voice.voiceId ? "true" : undefined}
              >
                <button
                  type="button"
                  className="gx-cast-voices__pick"
                  aria-pressed={chosen === voice.voiceId}
                  onClick={() => onChoose(voice.voiceId)}
                >
                  {/* The spaces are for the accessible name ("Marcus English",
                      not "MarcusEnglish"); the flex gap does the visual spacing. */}
                  <span className="gx-cast-voices__name">{name}</span>
                  {language && (
                    <>
                      {" "}
                      <span className="gx-cast-voices__lang">{language}</span>
                    </>
                  )}
                  {voice.voiceId === value && (
                    <>
                      {" "}
                      <Badge variant="arcane" size="sm">
                        {t("cast.voiceCurrent")}
                      </Badge>
                    </>
                  )}
                </button>
                {/* Never `loading` (which disables the button and drops keyboard
                    focus to <body>): while the sample is being synthesized the
                    same button stays pressable and cancels it. */}
                <IconButton
                  label={active ? t("cast.voiceStopNamed", { name }) : t("cast.voicePlay", { name })}
                  tooltipSide="left"
                  aria-busy={loading || undefined}
                  onClick={() => preview.toggle(voice.voiceId, sample.trim())}
                >
                  {loading ? <Spinner /> : active ? <Square size={15} /> : <Play size={16} />}
                </IconButton>
              </li>
            );
          })}
        </ul>
      )}
    </div>
  );
}
