package rpc

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"connectrpc.com/connect"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/internal/discordrest"
	"github.com/MrWong99/Glyphoxa/internal/llmbuild"
	"github.com/MrWong99/Glyphoxa/pkg/voice/providererr"
	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
)

// TestHealthClass pins the GM-facing mapping of provider-check failures: each
// known class gets its stable code, and no raw error text (package prefixes,
// env var names, ADR references) survives into the detail.
func TestHealthClass(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		err  error
		code string
	}{
		"no platform keys": {connect.NewError(connect.CodeFailedPrecondition, gmError(llmbuild.ErrNoPlatformKeyEntitlement)), healthNoPlatform},
		"missing key":      {fmt.Errorf("groq: %w (set GROQ_API_KEY)", errMissingKey), healthMissingKey},
		"key rejected":     {&providererr.HTTPError{Op: "groq ping", StatusCode: 401}, healthKeyRejected},
		"discord rejected": {fmt.Errorf("wrap: %w", &discordrest.StatusError{Op: "discordtag: GET /users/@me", Status: 401}), healthKeyRejected},
		"quota":            {&providererr.HTTPError{Op: "elevenlabs.ListVoices", StatusCode: 429}, healthQuota},
		"provider down":    {&providererr.HTTPError{Op: "gemini ping", StatusCode: 503}, healthUnreachable},
		"timeout":          {fmt.Errorf("health probe timed out: %w", context.DeadlineExceeded), healthUnreachable},
		"anything else":    {errors.New("llmbuild: decrypt llm key: boom"), healthCheckFailed},
	} {
		code, detail := healthClass(tc.err)
		if code != tc.code {
			t.Errorf("%s: code = %q, want %q", name, code, tc.code)
		}
		for _, leak := range []string{"llmbuild", "ADR", "GROQ_API_KEY", "HTTP", "failed_precondition"} {
			if strings.Contains(detail, leak) {
				t.Errorf("%s: detail %q leaks %q", name, detail, leak)
			}
		}
	}
}

// TestGetProviderHealth_ClassifiedDetail drives the RPC: no key anywhere is
// "missing_key" without touching the vendor, a rejected key is "key_rejected",
// and a tenant with no saved Bot token is probed with the central one.
func TestGetProviderHealth_ClassifiedDetail(t *testing.T) {
	t.Parallel()
	srv := NewVoiceServer(&fakeVoiceStore{}, voiceTestCipher(t), nil)
	srv.lookupEnv = func(k string) string {
		if k == "GROQ_API_KEY" {
			return "platform-groq"
		}
		return ""
	}
	var listerCalls int
	srv.newLister = func(string) tts.VoiceLister { listerCalls++; return &fakeLister{} }
	srv.pingLLM = func(context.Context, string) error {
		return &providererr.HTTPError{Op: "groq ping", StatusCode: 401}
	}
	srv.pingImage = func(context.Context, string) error { return nil }
	var probedToken string
	srv.botTag = func(_ context.Context, token string) (string, error) { probedToken = token; return "Glyphoxa#1", nil }
	srv.SetEnvBotToken("central-token")

	resp, err := srv.GetProviderHealth(tenantCtx(), connect.NewRequest(&managementv1.GetProviderHealthRequest{}))
	if err != nil {
		t.Fatalf("GetProviderHealth: %v", err)
	}
	got := map[string]*managementv1.ProviderHealth{}
	for _, p := range resp.Msg.GetProviders() {
		got[p.GetProvider()] = p
	}
	if c := got["groq"].GetCode(); c != healthKeyRejected {
		t.Errorf("groq code = %q, want %q", c, healthKeyRejected)
	}
	if c := got["elevenlabs"].GetCode(); c != healthMissingKey {
		t.Errorf("elevenlabs code = %q, want %q", c, healthMissingKey)
	}
	if listerCalls != 0 {
		t.Errorf("a key-less slot still called the vendor %d times", listerCalls)
	}
	if d := got["discord"]; d.GetStatus() != managementv1.HealthStatus_HEALTH_STATUS_HEALTHY || probedToken != "central-token" {
		t.Errorf("discord = %+v probed with %q, want healthy on the central token", d, probedToken)
	}
}
