import { useEffect, useMemo, useRef, useState } from "react";
import { useMutation } from "@connectrpc/connect-query";
import { toast } from "sonner";
import { Sparkles, Upload } from "lucide-react";

import { CampaignService, NodeType } from "@gen/glyphoxa/management/v1/management_pb";
import type { Map as PbMap, Node as PbNode } from "@gen/glyphoxa/management/v1/management_pb";
import { Alert } from "@/components/ui/Alert";
import { Button } from "@/components/ui/Button";
import { Dialog, DialogClose } from "@/components/ui/Dialog";
import { Field } from "@/components/ui/Field";
import { Input } from "@/components/ui/Input";
import { SegmentedControl } from "@/components/ui/SegmentedControl";
import { Switch } from "@/components/ui/Switch";
import { Textarea } from "@/components/ui/Textarea";
import { apiErrorMessage, errorMessage } from "@/lib/connectError";
import { useI18n } from "@/i18n";
import {
  MAP_IMAGE_ACCEPT,
  MAP_IMAGE_MAX_MB,
  imageDimensions,
  imageFileProblem,
  nameFromFile,
  type ImageBytes,
} from "./mapImage";

// The two dialogs that write a Map's own fields: "Add a map" (CreateMap, from an
// upload or a generated draft) and "Map settings" (UpdateMap). Until the second
// one existed a map was write-once: a typo in its name, the wrong parent or a
// forgotten "Depicts" entry could only be fixed by deleting the map and every
// pin on it.

/** The server's name cap (internal/rpc/campaign_map.go maxMapNameRunes). */
const MAP_NAME_MAX = 200;

export type AddMapMode = "upload" | "generate";

/**
 * AddMapDialog adds a Map, from an upload or from a generated draft (#538, #541).
 *
 * The image's real pixel dimensions are measured here, client-side, because only
 * the browser has decoded it — the server stores what it is told and uses it for
 * aspect ratio only.
 *
 * Generate mode is a DRAFT-REVIEW flow, deliberately identical in shape to the
 * Knowledge generator: prompt → preview → save or discard. Nothing reaches
 * campaign_map until the GM presses Add map, and discarding costs one closed
 * dialog and zero RPCs. The bytes only ever existed here.
 *
 * Mounted only while open (the caller renders it conditionally), so every field
 * starts fresh and an unsaved draft's object URL is revoked when it closes.
 */
export function AddMapDialog({
  initialMode,
  maps,
  locations,
  onClose,
  onCreated,
}: {
  initialMode: AddMapMode;
  maps: PbMap[];
  /** Location entries: what a map can depict, and what a generated one is drawn from. */
  locations: PbNode[];
  onClose: () => void;
  onCreated: (id: string) => void;
}) {
  const { t } = useI18n();
  const [mode, setMode] = useState<AddMapMode>(initialMode);
  const [name, setName] = useState("");
  // The name came from a filename, not from the GM: the next file may replace it.
  const [nameFromPick, setNameFromPick] = useState(false);
  const [parentID, setParentID] = useState("");
  const [anchorID, setAnchorID] = useState("");
  const [gmPrivate, setGmPrivate] = useState(false);
  const [file, setFile] = useState<File | null>(null);
  const [error, setError] = useState<string | null>(null);
  const create = useMutation(CampaignService.method.createMap);

  // Generate mode (#541). `draft` is the unsaved image: bytes plus the object URL
  // the preview renders. It is the ONLY place those bytes exist.
  const [prompt, setPrompt] = useState("");
  const [draft, setDraft] = useState<(ImageBytes & { url: string }) | null>(null);
  const generate = useMutation(CampaignService.method.generateMapImage);

  // Object URLs are a leak if they outlive their draft, and a draft is discarded
  // far more often than it is saved — that is the point of a review step.
  //
  // The live URL is mirrored in a REF because the unmount cleanup must revoke it
  // without touching state: React discards updates to an unmounted component, so
  // revoking inside a setState updater relies on that updater being run at all.
  const draftURL = useRef<string | null>(null);
  const dropDraft = () => {
    if (draftURL.current) {
      URL.revokeObjectURL(draftURL.current);
      draftURL.current = null;
    }
    setDraft(null);
  };
  useEffect(
    () => () => {
      if (draftURL.current) URL.revokeObjectURL(draftURL.current);
    },
    [],
  );

  const problem = file ? imageFileProblem(file) : null;
  const fileError =
    problem === "type"
      ? t("maps.imageWrongType")
      : problem === "size"
        ? t("maps.imageTooLarge", { mb: MAP_IMAGE_MAX_MB })
        : null;

  const pickFile = (picked: File | null) => {
    setFile(picked);
    setError(null);
    // A scan is usually already named after the place it shows. Only fill a
    // name the GM has not typed themselves.
    if (picked && (name.trim() === "" || nameFromPick)) {
      const guess = nameFromFile(picked.name).slice(0, MAP_NAME_MAX);
      if (guess !== "") {
        setName(guess);
        setNameFromPick(true);
      }
    }
  };

  const runGenerate = async () => {
    setError(null);
    if (prompt.trim() === "") return;
    try {
      const res = await generate.mutateAsync({ prompt: prompt.trim(), anchorNodeId: anchorID });
      dropDraft();
      const blob = new Blob([res.imageBytes as BlobPart], { type: res.contentType });
      const url = URL.createObjectURL(blob);
      draftURL.current = url;
      setDraft({ bytes: res.imageBytes, contentType: res.contentType, url });
    } catch (err) {
      setError(apiErrorMessage(err, t));
    }
  };

  const ready = name.trim() !== "" && (mode === "generate" ? draft !== null : file !== null && problem === null);

  const submit = async () => {
    setError(null);
    if (!ready || create.isPending) return;
    // Both doors land in the SAME CreateMap call. A generated map is not a
    // different kind of map — it is a map whose bytes came from a model.
    //
    // The bytes key off MODE, and switching mode drops the other source. Keying
    // them off "is there a draft" gave two conditions that could disagree: a GM
    // who generated, switched back to Upload and picked a file saved the
    // GENERATED bytes under the file's name.
    try {
      // Inside the try: reading the file can fail (it changed or vanished on
      // disk since it was picked), and submit() runs unawaited — outside, that
      // was an unhandled rejection with nothing said in the dialog.
      const source: ImageBytes | null =
        mode === "generate"
          ? draft
            ? { bytes: draft.bytes, contentType: draft.contentType }
            : null
          : file
            ? { bytes: new Uint8Array(await file.arrayBuffer()), contentType: file.type }
            : null;
      if (!source) return;
      const dims = await imageDimensions(
        new Blob([source.bytes as BlobPart], { type: source.contentType }),
        t("campaign.mapImageUnreadable"),
      );
      const res = await create.mutateAsync({
        name: name.trim(),
        imageBytes: source.bytes,
        contentType: source.contentType,
        widthPx: dims.width,
        heightPx: dims.height,
        parentMapId: parentID,
        // The Location this map depicts, whichever door the picture came through.
        // Without it the map lands with a NULL anchor and SuggestMapPins has no
        // prose to read — which used to make the feature unreachable for every
        // UPLOADED map, since only generate mode offered the picker.
        anchorNodeId: anchorID,
        gmPrivate,
      });
      if (res.map) onCreated(res.map.id);
      onClose();
    } catch (err) {
      setError(errorMessage(err));
    }
  };

  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !create.isPending) onClose();
      }}
      title={t("maps.addTitle")}
      description={t("maps.addDesc")}
      // A stray click on the backdrop must not throw away a typed prompt or a
      // generated draft that cost an image generation.
      closeOnOutsideClick={false}
      className="gx-maps-dialog"
      footer={
        <>
          <DialogClose>
            <Button variant="ghost" disabled={create.isPending}>
              {t("common.cancel")}
            </Button>
          </DialogClose>
          <Button variant="primary" disabled={!ready} loading={create.isPending} onClick={() => void submit()}>
            {t("campaign.addMap")}
          </Button>
        </>
      }
    >
      <div className="gx-maps-form">
        <SegmentedControl<AddMapMode>
          label={t("campaign.imageSourceAria")}
          value={mode}
          block
          onChange={(next) => {
            setMode(next);
            setError(null);
            // Switching source drops the other one, so the bytes that get saved
            // are always the ones on screen.
            if (next === "upload") dropDraft();
            else setFile(null);
          }}
          options={[
            { value: "upload", label: t("campaign.uploadAria"), icon: <Upload size={14} /> },
            { value: "generate", label: t("campaign.generateAria"), icon: <Sparkles size={14} /> },
          ]}
        />

        {mode === "upload" ? (
          <Field
            label={t("campaign.imageLabel")}
            hint={t("maps.imageHint", { mb: MAP_IMAGE_MAX_MB })}
            error={fileError}
          >
            {(control) => (
              <input
                {...control}
                className="gx-maps-form__file"
                type="file"
                accept={MAP_IMAGE_ACCEPT}
                onChange={(e) => pickFile(e.target.files?.[0] ?? null)}
              />
            )}
          </Field>
        ) : (
          <>
            <Textarea
              label={t("campaign.promptLabel")}
              hint={t("campaign.promptHint")}
              rows={3}
              value={prompt}
              placeholder={t("campaign.promptPlaceholder")}
              onChange={(e) => setPrompt(e.target.value)}
            />
            <div className="gx-maps-form__row">
              <Button
                variant="secondary"
                size="sm"
                iconStart={<Sparkles size={13} />}
                disabled={prompt.trim() === ""}
                loading={generate.isPending}
                onClick={() => void runGenerate()}
              >
                {draft ? t("campaign.regenerate") : t("campaign.generateChip")}
              </Button>
              {draft && (
                <Button variant="ghost" size="sm" onClick={dropDraft} disabled={generate.isPending}>
                  {t("campaign.discardDraft")}
                </Button>
              )}
            </div>
            {draft && (
              <div className="gx-maps-form__draft">
                <img className="gx-maps-form__draft-img" src={draft.url} alt={t("campaign.draftAlt")} />
              </div>
            )}
          </>
        )}

        <Input
          label={t("campaign.nameLabel")}
          value={name}
          maxLength={MAP_NAME_MAX}
          onChange={(e) => {
            setName(e.target.value);
            setNameFromPick(false);
          }}
          placeholder={t("campaign.mapNamePlaceholder")}
        />

        <DepictsField
          value={anchorID}
          onChange={setAnchorID}
          locations={locations}
          hint={mode === "generate" ? t("maps.depictsHintGenerate") : t("maps.depictsHint")}
        />

        {maps.length > 0 && (
          <Field label={t("campaign.parentLabel")} hint={t("maps.parentHint")}>
            {(control) => (
              <select
                {...control}
                className="gx-input"
                value={parentID}
                onChange={(e) => setParentID(e.target.value)}
              >
                <option value="">{t("campaign.parentNone")}</option>
                {maps.map((m) => (
                  <option key={m.id} value={m.id}>
                    {m.name}
                  </option>
                ))}
              </select>
            )}
          </Field>
        )}

        <Switch label={t("campaign.mapGmPrivateLabel")} checked={gmPrivate} onCheckedChange={setGmPrivate} />

        {error && <Alert tone="danger">{error}</Alert>}
      </div>
    </Dialog>
  );
}

// DepictsField picks the Location entry a map shows (its anchor, ADR-0060). One
// field for both dialogs, so the same choice reads the same way everywhere.
function DepictsField({
  value,
  onChange,
  locations,
  current,
  hint,
}: {
  value: string;
  onChange: (id: string) => void;
  locations: PbNode[];
  /**
   * The saved anchor when it is not among `locations` (the entry list has not
   * loaded, or the entry is not a Location): it must stay selectable, or saving
   * the dialog would silently clear it.
   */
  current?: { id: string; name: string };
  hint: string;
}) {
  const { t } = useI18n();
  const orphan = current && current.id !== "" && !locations.some((n) => n.id === current.id) ? current : null;
  return (
    <Field
      label={t("maps.depictsLabel")}
      hint={locations.length === 0 && !orphan ? t("maps.depictsNoLocations") : hint}
    >
      {(control) => (
        <select {...control} className="gx-input" value={value} onChange={(e) => onChange(e.target.value)}>
          <option value="">{t("maps.depictsNone")}</option>
          {orphan && <option value={orphan.id}>{orphan.name}</option>}
          {locations.map((n) => (
            <option key={n.id} value={n.id}>
              {n.name}
            </option>
          ))}
        </select>
      )}
    </Field>
  );
}

/** A map and everything nested inside it: what it must not be moved into. */
function selfAndDescendants(id: string, maps: PbMap[]): Set<string> {
  const out = new Set([id]);
  for (let grew = true; grew; ) {
    grew = false;
    for (const m of maps) {
      if (m.parentMapId !== "" && out.has(m.parentMapId) && !out.has(m.id)) {
        out.add(m.id);
        grew = true;
      }
    }
  }
  return out;
}

/**
 * MapSettingsDialog edits a Map's own fields — name, where it sits, the entry it
 * depicts, and whether players may see it — and saves all four in ONE UpdateMap
 * call. The RPC is a full-field save: a dialog that sent only what changed would
 * clear the rest.
 */
export function MapSettingsDialog({
  map,
  maps,
  nodes,
  onClose,
  onSaved,
}: {
  map: PbMap;
  maps: PbMap[];
  /** Every loaded entry; the Location ones are offered, the rest name a saved anchor. */
  nodes: PbNode[];
  onClose: () => void;
  onSaved: () => void;
}) {
  const { t } = useI18n();
  const [name, setName] = useState(map.name);
  const [parentID, setParentID] = useState(map.parentMapId);
  const [anchorID, setAnchorID] = useState(map.anchorNodeId);
  const [gmPrivate, setGmPrivate] = useState(map.gmPrivate);

  const update = useMutation(CampaignService.method.updateMap, {
    onSuccess: () => {
      onSaved();
      toast.success(t("maps.settingsSaved"));
      onClose();
    },
  });

  // A map cannot sit inside itself or inside one of its own children; the
  // server refuses it too ("a map cannot contain itself"), but offering the
  // choice only to reject it is a trap.
  const parents = useMemo(() => {
    const blocked = selfAndDescendants(map.id, maps);
    return maps.filter((m) => !blocked.has(m.id));
  }, [map.id, maps]);
  const locations = useMemo(() => nodes.filter((n) => n.nodeType === NodeType.LOCATION), [nodes]);
  const savedAnchor =
    map.anchorNodeId === ""
      ? undefined
      : {
          id: map.anchorNodeId,
          name: nodes.find((n) => n.id === map.anchorNodeId)?.name ?? t("maps.depictsCurrent"),
        };
  // A saved parent the list no longer offers (it has not loaded) stays selectable too.
  const orphanParent =
    map.parentMapId !== "" && !parents.some((m) => m.id === map.parentMapId) ? map.parentMapId : null;

  const canSave = name.trim() !== "";
  const save = () => {
    if (!canSave || update.isPending) return;
    update.mutate({
      id: map.id,
      name: name.trim(),
      parentMapId: parentID,
      anchorNodeId: anchorID,
      gmPrivate,
    });
  };

  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !update.isPending) onClose();
      }}
      title={t("maps.settingsTitle")}
      description={t("maps.settingsDesc")}
      closeOnOutsideClick={false}
      className="gx-maps-dialog"
      footer={
        <>
          <DialogClose>
            <Button variant="ghost" disabled={update.isPending}>
              {t("common.cancel")}
            </Button>
          </DialogClose>
          <Button variant="primary" disabled={!canSave} loading={update.isPending} onClick={save}>
            {t("common.saveChanges")}
          </Button>
        </>
      }
    >
      <div className="gx-maps-form">
        <Input
          label={t("campaign.nameLabel")}
          value={name}
          maxLength={MAP_NAME_MAX}
          onChange={(e) => setName(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              e.preventDefault();
              save();
            }
          }}
        />

        <Field label={t("campaign.parentLabel")} hint={t("maps.parentHint")}>
          {(control) => (
            <select
              {...control}
              className="gx-input"
              value={parentID}
              onChange={(e) => setParentID(e.target.value)}
            >
              <option value="">{t("campaign.parentNone")}</option>
              {orphanParent && <option value={orphanParent}>{t("maps.parentCurrent")}</option>}
              {parents.map((m) => (
                <option key={m.id} value={m.id}>
                  {m.name}
                </option>
              ))}
            </select>
          )}
        </Field>

        <DepictsField
          value={anchorID}
          onChange={setAnchorID}
          locations={locations}
          current={savedAnchor}
          hint={t("maps.depictsHint")}
        />

        <Switch label={t("campaign.mapGmPrivateLabel")} checked={gmPrivate} onCheckedChange={setGmPrivate} />

        {update.isError && (
          <Alert tone="danger">{t("common.couldntSave", { message: errorMessage(update.error) })}</Alert>
        )}
      </div>
    </Dialog>
  );
}
