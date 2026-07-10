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
// posture (ADR-0041) is applied at the mount by wrapping in auth.RequireSession;
// this type invents no auth of its own.
//
// #290 owns ServeExport (the GET download); #291 adds ServeImport (the POST
// upload) to this same type.
type Handler struct {
	Store *storage.Store
	Log   *slog.Logger
}

// ServeExport streams a campaign bundle download: GET
// /api/v1/campaigns/{id}/export. The {id} path value must parse as a UUID (400
// otherwise); an unknown campaign is 404. ?include_history=true nests the
// transcript payload (ADR-0053 §1, default off). Archived campaigns are
// exportable — a backup must still capture a campaign after it is archived.
//
// The bundle is [Encode]d STRAIGHT to the ResponseWriter (ADR-0048: no
// blob.Store round-trip — the download never lands in object storage). The
// filename is the canonical [Filename] for the campaign name.
func (h *Handler) ServeExport(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.Error(w, "invalid campaign id", http.StatusBadRequest)
		return
	}

	opts := ExportOptions{IncludeHistory: r.URL.Query().Get("include_history") == "true"}

	// GetCampaign resolves name (for the filename) and existence (404) before any
	// bytes are written, so a missing campaign is a clean 404 rather than a
	// half-streamed body. Archived campaigns resolve fine here (backup path).
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

	b, err := Export(r.Context(), h.Store, id, opts)
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
// the per-section counts the UI surfaces ("Imported <name>"). Part 1 always
// reports zero sessions/lines/chunks (History is part 2, #292).
type importResponse struct {
	CampaignID string `json:"campaign_id"`
	Name       string `json:"name"`
	Agents     int    `json:"agents"`
	Nodes      int    `json:"nodes"`
	Edges      int    `json:"edges"`
	Characters int    `json:"characters"`
	Sessions   int    `json:"sessions"`
	Lines      int    `json:"lines"`
	Chunks     int    `json:"chunks"`
}

// ServeImport ingests an uploaded campaign bundle: POST /api/v1/campaigns/import,
// multipart form field "bundle". The mount wraps it in auth.RequireSession (401
// gate + operator injected) THEN auth.RequireCSRF (403 double-submit, ADR-0016) —
// this handler assumes both have passed and reads the operator from the context.
//
// The request body is capped by http.MaxBytesReader at [blob.MaxSize] (ADR-0048's
// 32 MiB constant used purely as a request cap — blob.Store is NOT involved, the
// bundle never lands in object storage) BEFORE anything reads it, so an oversized
// upload is a clean 413 rather than an OOM. A malformed bundle or a newer/older
// unsupported format_version is 400 with a message naming both versions
// (ADR-0053 §7); the import runs SYNCHRONOUSLY (ADR-0049, no job row) and does NOT
// auto-activate the imported campaign (ADR-0053 §7 — the UI offers the switch).
func (h *Handler) ServeImport(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, blob.MaxSize)

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
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			http.Error(w, "bundle exceeds maximum upload size", http.StatusRequestEntityTooLarge)
			return
		}
		// Decode already wraps ErrNewerFormat/ErrUnsupportedFormat with a message
		// naming both versions; a plain parse failure is an opaque bad request.
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

	res, err := Import(r.Context(), h.Store, tenantID, b)
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
		CampaignID: res.CampaignID.String(),
		Name:       res.Name,
		Agents:     res.Agents,
		Nodes:      res.Nodes,
		Edges:      res.Edges,
		Characters: res.Characters,
		Sessions:   res.Sessions,
		Lines:      res.Lines,
		Chunks:     res.Chunks,
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
