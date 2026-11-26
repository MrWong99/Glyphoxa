package transcript_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/auth"
	"github.com/MrWong99/Glyphoxa/internal/transcript"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// A Campaign Guest on the transcript snapshot (ADR-0065). Their request
// carries the HOST Tenant, so the tenant scope would pass every session of
// every Campaign the host runs; a guest is checked against their one Campaign
// instead.

// guestMux mounts the relay routes with a guest principal injected: the host
// tenant plus the guest scope, as the policy would.
func guestMux(r *transcript.Relay, tenantID, campaignID uuid.UUID) http.Handler {
	inject := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, req *http.Request) {
			ctx := auth.WithTenant(req.Context(), tenantID)
			ctx = auth.WithGuest(ctx, auth.GuestScope{CampaignID: campaignID, Rights: auth.NewRightSet(auth.RightTranscriptsRead)})
			next(w, req.WithContext(ctx))
		}
	}
	m := http.NewServeMux()
	m.HandleFunc("GET /api/v1/sessions/{id}/events", inject(r.ServeEvents))
	m.HandleFunc("GET /api/v1/sessions/{id}", inject(r.ServeSnapshot))
	return m
}

// A live snapshot carries each human line's Discord snowflake for the GM's
// attribution; a guest gets the transcript without them.
func TestSnapshot_GuestGetsNoSpeakerIDs(t *testing.T) {
	bus := voiceevent.NewBus()
	fs := &fakeSessions{id: uuid.New(), active: true}
	relay := transcript.NewRelay(fwd(t, bus, fs), fs, nil, nil)
	relay.SetCampaignScope(func(context.Context, uuid.UUID, uuid.UUID) (bool, error) { return true, nil })
	bus.Publish(voiceevent.STTFinal{At: time.Now(), Text: "I open the door", TurnID: "t1", SpeakerID: "123456789012345678"})

	read := func(h http.Handler) transcript.View {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+fs.id.String(), nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("snapshot status = %d, want 200", rec.Code)
		}
		var v transcript.View
		if err := json.NewDecoder(rec.Body).Decode(&v); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(v.Lines) != 1 || v.Lines[0].Text != "I open the door" {
			t.Fatalf("lines = %+v, want the one spoken line", v.Lines)
		}
		return v
	}
	if got := read(guestMux(relay, uuid.New(), uuid.New())).Lines[0].SpeakerID; got != "" {
		t.Errorf("a guest's snapshot carries the speaker's Discord id %q", got)
	}
	// The anti-vacuity half: the GM does get it, from the same relay.
	if got := read(mux(relay)).Lines[0].SpeakerID; got != "123456789012345678" {
		t.Errorf("the GM's snapshot speaker id = %q, want the snowflake", got)
	}
}

func snapshotStatus(t *testing.T, h http.Handler, sessionID uuid.UUID) int {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+sessionID.String(), nil))
	return rec.Code
}

func TestSnapshot_GuestIsScopedToTheirCampaign(t *testing.T) {
	tenantID, guestCampaign, otherCampaign := uuid.New(), uuid.New(), uuid.New()
	mine, theirs := uuid.New(), uuid.New()
	owner := map[uuid.UUID]uuid.UUID{mine: guestCampaign, theirs: otherCampaign}

	newRelay := func() (*transcript.Relay, *int, *[]uuid.UUID) {
		r := transcript.NewRelay(voiceevent.NewBus(), &fakeSessions{}, nil, nil)
		tenantCalls := 0
		// The tenant scope says YES to both sessions: they are both the host's.
		r.SetTenantScope(func(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
			tenantCalls++
			return true, nil
		})
		asked := []uuid.UUID{}
		r.SetCampaignScope(func(_ context.Context, campaignID, sessionID uuid.UUID) (bool, error) {
			asked = append(asked, campaignID)
			return owner[sessionID] == campaignID, nil
		})
		return r, &tenantCalls, &asked
	}

	r, tenantCalls, asked := newRelay()
	guest := guestMux(r, tenantID, guestCampaign)
	if code := snapshotStatus(t, guest, mine); code != http.StatusOK {
		t.Errorf("guest reading a session of their campaign: %d, want 200", code)
	}
	if code := snapshotStatus(t, guest, theirs); code != http.StatusNotFound {
		t.Errorf("guest reading a session of ANOTHER campaign of the host tenant: %d, want 404", code)
	}
	if code := snapshotStatus(t, guest, uuid.New()); code != http.StatusNotFound {
		t.Errorf("guest reading an unknown session: %d, want 404", code)
	}
	if *tenantCalls != 0 {
		t.Errorf("the tenant scope was consulted %d times for a guest; it would have passed the other campaign's session", *tenantCalls)
	}
	for _, c := range *asked {
		if c != guestCampaign {
			t.Errorf("the campaign scope was asked about %s, want only the guest campaign", c)
		}
	}

	// The GM of the same tenant reads both, through the tenant scope.
	gm := tenantMux(r, tenantID)
	for _, sid := range []uuid.UUID{mine, theirs} {
		if code := snapshotStatus(t, gm, sid); code != http.StatusOK {
			t.Errorf("the GM reading session %s: %d, want 200", sid, code)
		}
	}
	if *tenantCalls != 2 {
		t.Errorf("tenant scope calls for the GM = %d, want 2", *tenantCalls)
	}

	t.Run("no campaign scope wired: a guest is served nothing", func(t *testing.T) {
		// Unlike the tenant scope, an unwired campaign scope must not mean
		// "unscoped" — that would hand a guest the whole host tenant.
		bare := transcript.NewRelay(voiceevent.NewBus(), &fakeSessions{}, nil, nil)
		if code := snapshotStatus(t, guestMux(bare, tenantID, guestCampaign), mine); code != http.StatusNotFound {
			t.Errorf("guest on a relay with no scopes at all: %d, want 404", code)
		}
		onlyTenant := transcript.NewRelay(voiceevent.NewBus(), &fakeSessions{}, nil, nil)
		onlyTenant.SetTenantScope(func(context.Context, uuid.UUID, uuid.UUID) (bool, error) { return true, nil })
		if code := snapshotStatus(t, guestMux(onlyTenant, tenantID, guestCampaign), mine); code != http.StatusNotFound {
			t.Errorf("guest on a relay with only the tenant scope: %d, want 404", code)
		}
	})

	t.Run("a scope failure is a 500, never an answer", func(t *testing.T) {
		broken := transcript.NewRelay(voiceevent.NewBus(), &fakeSessions{}, nil, nil)
		broken.SetCampaignScope(func(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
			return false, errors.New("db down")
		})
		if code := snapshotStatus(t, guestMux(broken, tenantID, guestCampaign), mine); code != http.StatusInternalServerError {
			t.Errorf("campaign scope failure: %d, want 500", code)
		}
	})

	t.Run("the SSE stream applies the same scope before it opens", func(t *testing.T) {
		// The production mount table gives the SSE route no guest posture at
		// all (a guest never reaches it); the handler still must not stream
		// another campaign's session if one did.
		r, _, _ := newRelay()
		rec := httptest.NewRecorder()
		guestMux(r, tenantID, guestCampaign).ServeHTTP(rec,
			httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+theirs.String()+"/events", nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("guest opening another campaign's SSE stream: %d, want 404", rec.Code)
		}
	})
}
