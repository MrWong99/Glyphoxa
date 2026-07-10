package bundle

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/google/uuid"

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

func (h *Handler) logError(msg string, err error) {
	if h.Log != nil {
		h.Log.Error("bundle: "+msg, "err", err)
	}
}
