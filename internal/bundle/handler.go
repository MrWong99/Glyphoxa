package bundle

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/auth"
	"github.com/MrWong99/Glyphoxa/internal/blob"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// Handler serves the campaign-bundle HTTP transport (ADR-0053 §7): plain
// net/http endpoints mounted beside the SSE relay, NOT Connect RPCs (ADR-0015) —
// the bundle is a file a human inspects, and a streamed gzip download / multipart
// upload do not fit the Connect message-size model. The operator-only auth
// posture (ADR-0041) is applied at the mount via the guarded mount table
// (auth.MustGuardMounts, #446); this type invents no auth of its own.
//
// #290 owns ServeExport (the GET download); #291 adds ServeImport (the POST
// upload) to this same type.
type Handler struct {
	Store *storage.Store
	// Blobs is the seam a Map's image bytes ride on an images-included export
	// (#547, ADR-0048). Nil leaves maps exporting without their pictures, which is
	// what a composition with no blob backend can honestly offer.
	Blobs blob.Store
	Log   *slog.Logger
}

// pg is the export/import adapter for this handler's store and blob seam.
func (h *Handler) pg() PGStore { return PGStore{Store: h.Store, Blobs: h.Blobs} }

// ServeExport streams a campaign bundle download: GET
// /api/v1/campaigns/{id}/export. The {id} path value must parse as a UUID (400
// otherwise); an unknown campaign is 404. ?include_history=true nests the
// transcript payload (ADR-0053 §1, default off) and ?include_images=true embeds
// map image bytes (#547, default off — the blob cap is 32 MiB per image). Archived campaigns are
// exportable — a backup must still capture a campaign after it is archived.
//
// Tenant posture (#439): the mount declares TenantRequired (session AND
// tenant, the post-#408 discipline), and this handler 404s a campaign
// outside the injected tenant BEFORE any bytes are
// written — foreign and nonexistent campaigns are indistinguishable, matching
// the Highlight mounts' don't-reveal-existence posture. A missing context
// tenant is a miswired mount and rejects 401, fail-closed.
//
// The bundle JSON is [Encode]d STRAIGHT to the ResponseWriter — it never lands
// in object storage. (Map image BYTES do come FROM the blob seam on an
// images-included export, and go back through it on import; it is the bundle
// itself that never round-trips through blob storage.) The
// filename is the canonical [Filename] for the campaign name.
func (h *Handler) ServeExport(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.Error(w, "invalid campaign id", http.StatusBadRequest)
		return
	}
	tenantID, ok := auth.TenantID(r.Context())
	if !ok {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}

	opts := ExportOptions{
		IncludeHistory: r.URL.Query().Get("include_history") == "true",
		// Images are a separate opt-in from history: a GM may well want their maps
		// in a share bundle without the transcripts, or the transcripts without the
		// megabytes (#547).
		IncludeImages: r.URL.Query().Get("include_images") == "true",
	}

	// GetCampaign resolves name (for the filename), existence AND tenant
	// ownership (both 404, #439) before any bytes are written, so a missing or
	// foreign campaign is a clean 404 rather than a half-streamed body. Archived
	// campaigns resolve fine here (backup path).
	campaign, err := h.Store.GetCampaign(r.Context(), id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			http.Error(w, "campaign not found", http.StatusNotFound)
			return
		}
		h.logError("get campaign", err)
		http.Error(w, "export failed", http.StatusInternalServerError)
		return
	}
	if campaign.TenantID != tenantID {
		http.Error(w, "campaign not found", http.StatusNotFound)
		return
	}

	b, err := Export(r.Context(), h.pg(), id, opts)
	if err != nil {
		h.logError("build bundle", err)
		http.Error(w, "export failed", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename=%q`, Filename(campaign.Name)))
	if err := Encode(w, b); err != nil {
		// Headers (and likely body bytes) are already committed, so this can only be
		// logged — the client sees a truncated download and retries.
		h.logError("encode bundle", err)
	}
}

// importResponse is the ServeImport 200 body: the minted campaign identity plus
// the per-section counts the UI surfaces ("Imported <name>"). A history-less
// bundle reports zero sessions/lines/chunks. DroppedParticipantRefs is ALWAYS
// present so the response shape is stable for clients — a chunk participant ref
// that mapped to no imported Agent was dropped (not fatal), and a nonzero count
// also lands as a slog.Warn in the importer (#381).
type importResponse struct {
	CampaignID string `json:"campaign_id"`
	Name       string `json:"name"`
	Agents     int    `json:"agents"`
	Nodes      int    `json:"nodes"`
	Edges      int    `json:"edges"`
	Characters int    `json:"characters"`
	// The v2 sections (#547). Reporting only the v1 counts would make a successful
	// import of maps, boards and aspects LOOK like they were dropped — the exact
	// silent-omission genre this slice exists to close.
	Aspects int `json:"aspects"`
	Tags    int `json:"tags"`
	Maps    int `json:"maps"`
	Pins    int `json:"pins"`
	Boards  int `json:"boards"`
	// The v3 section (#592): the Butler planning chat's threads and prose turns,
	// counted for the same silent-omission reason as the v2 counters above.
	PlanningThreads        int `json:"planning_threads"`
	PlanningMessages       int `json:"planning_messages"`
	Sessions               int `json:"sessions"`
	Lines                  int `json:"lines"`
	Chunks                 int `json:"chunks"`
	Appearances            int `json:"appearances"`
	DroppedParticipantRefs int `json:"dropped_participant_refs"`
}

// ServeImport ingests an uploaded campaign bundle: POST /api/v1/campaigns/import,
// multipart form field "bundle". The guarded mount (#446) gates the session
// (401 + operator injected) and, because POST is state-changing, the CSRF
// double-submit (403, ADR-0016) — this handler assumes both have passed and
// reads the operator from the context.
//
// The request body is capped by [MaxImportBytes] (ADR-0048's
// 32 MiB constant used purely as a request cap — blob.Store is NOT involved, the
// bundle never lands in object storage) BEFORE anything reads it, so an oversized
// upload is a clean 413 rather than an OOM. A malformed bundle or a newer/older
// unsupported format_version is 400 with a message naming both versions
// (ADR-0053 §7); the import runs SYNCHRONOUSLY (ADR-0049, no job row) and does NOT
// auto-activate the imported campaign (ADR-0053 §7 — the UI offers the switch).
func (h *Handler) ServeImport(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, importLimit)

	file, _, err := r.FormFile("bundle")
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			http.Error(w, "bundle exceeds maximum upload size", http.StatusRequestEntityTooLarge)
			return
		}
		writeImportError(w, http.StatusBadRequest, "missing or invalid bundle upload")
		return
	}
	defer file.Close()

	b, err := Decode(file)
	if err != nil {
		// The size cap surfaces at FormFile above (it reads the multipart body), so
		// Decode never sees a MaxBytesError. Decode already wraps
		// ErrNewerFormat/ErrUnsupportedFormat with a message naming both versions;
		// a plain parse failure is an opaque bad request.
		writeImportError(w, http.StatusBadRequest, importErrorMessage(err))
		return
	}

	u, ok := auth.CurrentUser(r.Context())
	if !ok {
		// RequireSession injects the operator, so a miss is a wiring bug, not a
		// client error.
		h.logError("no operator in import context", errors.New("missing user"))
		http.Error(w, "import failed", http.StatusInternalServerError)
		return
	}
	tenantID, err := h.Store.TenantForUser(r.Context(), u.ID)
	if err != nil {
		h.logError("resolve tenant for import", err)
		http.Error(w, "import failed", http.StatusInternalServerError)
		return
	}

	res, err := Import(r.Context(), h.pg(), tenantID, b)
	if err != nil {
		if errors.Is(err, ErrNewerFormat) || errors.Is(err, ErrUnsupportedFormat) {
			writeImportError(w, http.StatusBadRequest, importErrorMessage(err))
			return
		}
		h.logError("import bundle", err)
		http.Error(w, "import failed", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(importResponse{
		CampaignID:             res.CampaignID.String(),
		Name:                   res.Name,
		Agents:                 res.Agents,
		Nodes:                  res.Nodes,
		Edges:                  res.Edges,
		Characters:             res.Characters,
		Aspects:                res.Aspects,
		Tags:                   res.Tags,
		Maps:                   res.Maps,
		Pins:                   res.Pins,
		Boards:                 res.Boards,
		PlanningThreads:        res.PlanningThreads,
		PlanningMessages:       res.PlanningMessages,
		Appearances:            res.Appearances,
		Sessions:               res.Sessions,
		Lines:                  res.Lines,
		Chunks:                 res.Chunks,
		DroppedParticipantRefs: res.DroppedParticipantRefs,
	})
}

// importErrorMessage surfaces the version-refusal message (which names both
// versions) verbatim to the client, but keeps any other decode failure opaque so
// an internal error string never leaks through the 400 body.
func importErrorMessage(err error) string {
	if errors.Is(err, ErrNewerFormat) || errors.Is(err, ErrUnsupportedFormat) {
		return err.Error()
	}
	return "invalid campaign bundle"
}

// writeImportError writes a JSON {"error": ...} body with the given status — the
// shape the web upload path reads for its failure toast.
func writeImportError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func (h *Handler) logError(msg string, err error) {
	if h.Log != nil {
		h.Log.Error("bundle: "+msg, "err", err)
	}
}
