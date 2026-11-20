package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1/managementv1connect"
	"github.com/MrWong99/Glyphoxa/internal/auth"
	"github.com/MrWong99/Glyphoxa/internal/session"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// meteredBody counts how many body bytes the server actually pulled.
type meteredBody struct {
	r    io.Reader
	read atomic.Int64
}

func (m *meteredBody) Read(p []byte) (int, error) {
	n, err := m.r.Read(p)
	m.read.Add(int64(n))
	return n, err
}

// TestManagementMounts_UnauthenticatedLargeBodyIsNeverBuffered pins the R2
// wiring against the REAL mount table: an unauthenticated caller POSTing a
// 4 MiB body to any of the six Connect services must be refused without the
// server reading more than the 1 MiB default cap. The property is easy to lose
// at the composition root — one service mounted with the upload cap but
// without the pre-body session check, or a seventh service added with the
// large cap — so it is asserted on managementMounts itself, and the service
// list is checked against the mounts so a new service cannot be forgotten
// here.
//
// connect-go buffers a request before any interceptor runs, so "the
// interceptor will reject it" is not a defence: the read cap is. And connect's
// cap alone only bounds what it buffers — past it, connect drains the rest of
// the body to count it — so the bytes actually READ are what is measured here.
func TestManagementMounts_UnauthenticatedLargeBodyIsNeverBuffered(t *testing.T) {
	// One real procedure per service: an unknown method 404s before the body
	// is considered, which would make the assertion vacuous.
	procedures := map[string]string{
		managementv1connect.CampaignServiceName: managementv1connect.CampaignServiceCreateMapProcedure,
		managementv1connect.AuthServiceName:     managementv1connect.AuthServiceLogoutProcedure,
		managementv1connect.ProviderServiceName: managementv1connect.ProviderServiceListProviderConfigsProcedure,
		managementv1connect.VoiceServiceName:    managementv1connect.VoiceServiceListVoicesProcedure,
		managementv1connect.SessionServiceName:  managementv1connect.SessionServiceGenerateRecapProcedure,
		managementv1connect.ChatServiceName:     managementv1connect.ChatServiceCreatePlanningThreadProcedure,
	}

	// No request here carries a session, so nothing ever reaches the (nil-pool)
	// store or the nil engines: the table is built exactly as runWeb builds it.
	store, log := storage.New(nil), slog.New(slog.DiscardHandler)
	sessions := session.NewIntentControl(store, log, session.IntentControlConfig{})
	mounts := managementMounts(store, nil, nil, nil, log,
		sessions, nil, nil, nil, nil, nil, nil, nil, nil, "", auth.AdmissionAllowlist, "", nil)
	mux := http.NewServeMux()
	services := 0
	for _, m := range mounts {
		mux.Handle(m.Path, m.Handler)
		if strings.HasPrefix(m.Path, "/api/glyphoxa.management.v1.") {
			services++
			name := strings.Trim(strings.TrimPrefix(m.Path, "/api/"), "/")
			if _, ok := procedures[name]; !ok {
				t.Errorf("Connect service %q is mounted but not covered by this test — add a procedure for it", name)
			}
		}
	}
	if services != len(procedures) {
		t.Fatalf("mount table has %d Connect services, this test covers %d", services, len(procedures))
	}

	const bodySize = 4 << 20
	const mayRead = auth.MaxRequestBytes + 64<<10 // the default cap plus buffer slack
	for name, proc := range procedures {
		for _, declared := range []bool{true, false} {
			body := &meteredBody{r: strings.NewReader(`{"name":"` + strings.Repeat("x", bodySize) + `"}`)}
			req := httptest.NewRequest(http.MethodPost, "/api"+proc, body)
			req.Header.Set("Content-Type", "application/json")
			if !declared {
				req.ContentLength = -1 // chunked: no length for the server to refuse up front
			}
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			if rec.Code == http.StatusOK {
				t.Errorf("%s (declared length=%v): an unauthenticated 4 MiB request was served", name, declared)
			}
			if got := body.read.Load(); got > mayRead {
				t.Errorf("%s (declared length=%v): the server read %d bytes of an unauthenticated request body, want at most %d",
					name, declared, got, mayRead)
			}
		}
	}

	// The upload service turns a sessionless caller away without reading at all.
	body := &meteredBody{r: strings.NewReader(strings.Repeat("x", bodySize))}
	req := httptest.NewRequest(http.MethodPost, "/api"+managementv1connect.CampaignServiceCreateMapProcedure, body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || body.read.Load() != 0 {
		t.Errorf("CampaignService without a session: status=%d after reading %d body bytes, want 401 and 0",
			rec.Code, body.read.Load())
	}
}
