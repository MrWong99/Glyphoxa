//go:build integration

// This drives the CampaignService Player Character (PC) handlers end to end over
// Connect-JSON against a real *storage.Store (testcontainers Postgres), proving
// the wire → store → wire round-trip: create, list (campaign-scoped), rebind,
// duplicate → AlreadyExists, and delete → NotFound (#276, E4). Tag-isolated
// behind `integration`; reuses startPostgres/seedStore from
// campaign_integration_test.go.

package rpc_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1/managementv1connect"
	"github.com/MrWong99/Glyphoxa/internal/rpc"
)

func TestCharacterCRUD_Integration(t *testing.T) {
	dsn := startPostgres(t)
	store, tenantID, _ := seedStoreTenant(t, dsn) // seedStore inserts the (active) campaign

	mux := http.NewServeMux()
	mux.Handle(rpc.NewCampaignServer(store).Handler(connect.WithInterceptors(tenantOperatorInterceptor(tenantID, "operator-char"))))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	client := managementv1connect.NewCampaignServiceClient(
		http.DefaultClient, srv.URL, connect.WithProtoJSON(),
	)
	ctx := context.Background()

	// No Characters yet.
	list, err := client.ListCharacters(ctx, connect.NewRequest(&managementv1.ListCharactersRequest{}))
	if err != nil {
		t.Fatalf("ListCharacters (initial): %v", err)
	}
	if got := len(list.Msg.GetCharacters()); got != 0 {
		t.Fatalf("initial characters = %d, want 0", got)
	}

	// Create one.
	created, err := client.CreateCharacter(ctx, connect.NewRequest(&managementv1.CreateCharacterRequest{
		Name: "Aravel", Aliases: []string{"the ranger"}, DiscordUserId: "111111111111111111",
	}))
	if err != nil {
		t.Fatalf("CreateCharacter: %v", err)
	}
	pc := created.Msg.GetCharacter()
	if pc.GetId() == "" || pc.GetName() != "Aravel" || pc.GetDiscordUserId() != "111111111111111111" {
		t.Fatalf("created character wrong: %+v", pc)
	}
	if pc.GetLinkedUserId() != "" {
		t.Errorf("linked_user_id = %q, want empty (dormant, ADR-0003)", pc.GetLinkedUserId())
	}

	// A duplicate Discord User in the same campaign is CodeAlreadyExists.
	_, err = client.CreateCharacter(ctx, connect.NewRequest(&managementv1.CreateCharacterRequest{
		Name: "Impostor", DiscordUserId: "111111111111111111",
	}))
	if got := connect.CodeOf(err); got != connect.CodeAlreadyExists {
		t.Fatalf("duplicate discord user code = %v, want AlreadyExists", got)
	}

	// Rebind to a different Discord User + edit name/aliases, then reload.
	if _, err := client.UpdateCharacter(ctx, connect.NewRequest(&managementv1.UpdateCharacterRequest{
		Id: pc.GetId(), Name: "Aravel Reborn", Aliases: []string{"the reborn"}, DiscordUserId: "222222222222222222",
	})); err != nil {
		t.Fatalf("UpdateCharacter (rebind): %v", err)
	}
	list, err = client.ListCharacters(ctx, connect.NewRequest(&managementv1.ListCharactersRequest{}))
	if err != nil {
		t.Fatalf("ListCharacters (after rebind): %v", err)
	}
	if got := len(list.Msg.GetCharacters()); got != 1 {
		t.Fatalf("characters after rebind = %d, want 1", got)
	}
	reloaded := list.Msg.GetCharacters()[0]
	if reloaded.GetName() != "Aravel Reborn" || reloaded.GetDiscordUserId() != "222222222222222222" {
		t.Errorf("rebind did not round-trip: %+v", reloaded)
	}
	if len(reloaded.GetAliases()) != 1 || reloaded.GetAliases()[0] != "the reborn" {
		t.Errorf("aliases did not round-trip: %+v", reloaded.GetAliases())
	}

	// Delete it; the list empties and a second delete is CodeNotFound.
	if _, err := client.DeleteCharacter(ctx, connect.NewRequest(&managementv1.DeleteCharacterRequest{Id: pc.GetId()})); err != nil {
		t.Fatalf("DeleteCharacter: %v", err)
	}
	list, err = client.ListCharacters(ctx, connect.NewRequest(&managementv1.ListCharactersRequest{}))
	if err != nil {
		t.Fatalf("ListCharacters (after delete): %v", err)
	}
	if got := len(list.Msg.GetCharacters()); got != 0 {
		t.Fatalf("characters after delete = %d, want 0", got)
	}
	_, err = client.DeleteCharacter(ctx, connect.NewRequest(&managementv1.DeleteCharacterRequest{Id: pc.GetId()}))
	if got := connect.CodeOf(err); got != connect.CodeNotFound {
		t.Errorf("DeleteCharacter(already-gone) code = %v, want NotFound", got)
	}
}
