//go:build integration

package main

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/internal/auth"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// The boot wiring of the operator-standing rule (ADR-0065 review hardening),
// against a real Postgres. The storage and auth suites all install the
// [storage.AccessPolicy] by hand, so none of them notices when the composition
// root stops doing it — and the zero policy requires nothing, so that failure
// is silent: revoked operators stay GM and their guests stay in. This drives
// the helpers runWeb, runVoiceWorker and runVoice call, with two Stores over
// one database standing in for the two processes of a split deployment.

const (
	bootPolicyAdmin   = "600000000000000001" // in GLYPHOXA_OPERATOR_IDS
	bootPolicyGranted = "600000000000000002" // holds an Access Grant
	bootPolicyPlain   = "600000000000000003" // operates a Tenant, never granted
	bootPolicyGuest   = "600000000000000004" // guest of the plain operator's Campaign
)

func TestAccessPolicyBootWiring(t *testing.T) {
	ctx := context.Background()
	pool := bootMigratedPool(t)
	web, worker := storage.New(pool), storage.New(pool)
	log := slog.New(slog.DiscardHandler)

	tenants := map[string]storage.SignupResult{}
	for _, id := range []string{bootPolicyAdmin, bootPolicyGranted, bootPolicyPlain} {
		res, err := web.ProvisionSignup(ctx, storage.SignupParams{
			User:       storage.UpsertUserParams{DiscordUserID: id, Name: "GM " + id},
			TenantName: "Table " + id,
			Session:    storage.NewSession{Token: "tok-" + id, ExpiresAt: time.Now().Add(time.Hour)},
		})
		if err != nil {
			t.Fatalf("provision %s: %v", id, err)
		}
		tenants[id] = res
	}
	if _, err := web.GrantAccess(ctx, bootPolicyGranted, "", false, tenants[bootPolicyAdmin].User.ID); err != nil {
		t.Fatalf("GrantAccess: %v", err)
	}
	campaign, err := web.CreateCampaign(ctx, storage.NewCampaign{
		TenantID: tenants[bootPolicyPlain].Tenant.ID, Name: "Ungranted", System: "dnd5e", Language: "en"})
	if err != nil {
		t.Fatalf("CreateCampaign: %v", err)
	}
	if _, err := web.AddCampaignGuest(ctx, campaign, bootPolicyGuest, []string{"wiki.read"}, tenants[bootPolicyPlain].User.ID); err != nil {
		t.Fatalf("AddCampaignGuest: %v", err)
	}

	bound := func(t *testing.T, st *storage.Store) []string {
		t.Helper()
		bindings, err := st.ListTenantOperatorBindings(ctx)
		if err != nil {
			t.Fatalf("ListTenantOperatorBindings: %v", err)
		}
		ids := []string{}
		for _, b := range bindings {
			ids = append(ids, b.DiscordUserID)
		}
		slices.Sort(ids)
		return ids
	}
	wantBound := func(t *testing.T, when string, st *storage.Store, ids ...string) {
		t.Helper()
		if got := bound(t, st); !slices.Equal(got, ids) {
			t.Errorf("%s: bound operators = %v, want %v", when, got, ids)
		}
	}
	all := []string{bootPolicyAdmin, bootPolicyGranted, bootPolicyPlain}

	// --- The web boot, allowlist with one env admin.
	webEnv := envMap(map[string]string{
		"GLYPHOXA_ADMISSION_MODE": "allowlist",
		"GLYPHOXA_OPERATOR_IDS":   bootPolicyAdmin,
	})
	allow := auth.ParseOperatorAllowlist(webEnv("GLYPHOXA_OPERATOR_IDS"))
	res, err := resolveAdmissionMode(ctx, web, webEnv)
	if err != nil {
		t.Fatalf("resolveAdmissionMode: %v", err)
	}

	// Before the policy is installed nothing is required: this is the state a
	// boot that forgot the call would run in.
	wantBound(t, "web, no policy installed", web, all...)
	if _, err := web.GuestStandingOf(ctx, bootPolicyGuest); err != nil {
		t.Fatalf("fixture: guest standing before the policy = %v, want reachable", err)
	}

	installWebAccessPolicy(web, false, res.Effective, allow)
	wantBound(t, "web, allowlist", web, bootPolicyAdmin, bootPolicyGranted)
	if st, err := web.GuestStandingOf(ctx, bootPolicyGuest); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("web, allowlist: guest of an ungranted host = %+v, %v; want ErrNotFound — host standing is not enforced", st, err)
	}

	// --- The worker boots first: no env of its own, nothing recorded yet. It
	// requires nothing rather than dropping every GM.
	noEnv := envMap(nil)
	if got := installWorkerAccessPolicy(worker, false, noEnv); got.Len() != 0 {
		t.Errorf("worker allowlist = %v, want empty", got.IDs())
	}
	wantBound(t, "worker, web has recorded nothing", worker, all...)

	// A dev web boot records nothing.
	if err := recordWebAccessPosture(ctx, web, true, res, allow, log); err != nil {
		t.Fatalf("recordWebAccessPosture (dev): %v", err)
	}
	if mode, err := web.GetAdmissionPosture(ctx); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("posture after a dev boot = %q, %v; want none recorded", mode, err)
	}
	wantBound(t, "worker, after a dev web boot", worker, all...)

	// --- The web boot records the posture and the admin list; the worker —
	// NOT restarted — now binds the admin and the grant holder only.
	if err := recordWebAccessPosture(ctx, web, false, res, allow, log); err != nil {
		t.Fatalf("recordWebAccessPosture: %v", err)
	}
	if mode, err := web.GetAdmissionPosture(ctx); err != nil || mode != "allowlist" {
		t.Errorf("recorded posture = %q, %v; want allowlist", mode, err)
	}
	var recorded []string
	if err := pool.QueryRow(ctx, `SELECT platform_admin_ids FROM deployment_settings WHERE id`).Scan(&recorded); err != nil {
		t.Fatalf("read recorded admins: %v", err)
	}
	if !slices.Equal(recorded, []string{bootPolicyAdmin}) {
		t.Errorf("recorded Platform Admins = %v, want the env list", recorded)
	}
	wantBound(t, "worker, admin list recorded", worker, bootPolicyAdmin, bootPolicyGranted)

	// A revoked operator is not bound on either tier; the admin still is.
	if err := web.RevokeAccess(ctx, bootPolicyGranted); err != nil {
		t.Fatalf("RevokeAccess: %v", err)
	}
	wantBound(t, "web, grant revoked", web, bootPolicyAdmin)
	wantBound(t, "worker, grant revoked", worker, bootPolicyAdmin)

	// --- The deployment goes open: only the web tier reboots. The worker
	// follows the recorded posture without a restart…
	openEnv := envMap(map[string]string{
		"GLYPHOXA_ADMISSION_MODE": "open",
		"GLYPHOXA_OPERATOR_IDS":   bootPolicyAdmin,
	})
	web2 := storage.New(pool)
	res2, err := resolveAdmissionMode(ctx, web2, openEnv)
	if err != nil {
		t.Fatalf("resolveAdmissionMode (open): %v", err)
	}
	installWebAccessPolicy(web2, false, res2.Effective, allow)
	if err := recordWebAccessPosture(ctx, web2, false, res2, allow, log); err != nil {
		t.Fatalf("recordWebAccessPosture (open): %v", err)
	}
	wantBound(t, "web, open", web2, all...)
	wantBound(t, "worker, web flipped to open", worker, all...)
	if _, err := web2.GuestStandingOf(ctx, bootPolicyGuest); err != nil {
		t.Errorf("web, open: guest standing = %v, want reachable", err)
	}

	// …and back: a recorded lock-down reaches the worker the same way.
	if err := web2.RecordAdmissionPosture(ctx, "allowlist"); err != nil {
		t.Fatal(err)
	}
	wantBound(t, "worker, web locked down again", worker, bootPolicyAdmin)

	// A standalone voice node in dev mode requires nothing, whatever is
	// recorded.
	devNode := storage.New(pool)
	installWorkerAccessPolicy(devNode, true, noEnv)
	wantBound(t, "dev voice node", devNode, all...)
}
