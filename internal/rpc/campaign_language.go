package rpc

import (
	"context"

	"connectrpc.com/connect"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/pkg/voice/address"
)

// ListSupportedLanguages returns the Campaign Language choices: the language
// codes with a registered phonetic encoder (ADR-0024 EncoderRegistry), sorted.
// The registry is the sole language-truth source — the SAME source the wirenpc
// roster reads (internal/wirenpc/roster.go) — so a newly registered encoder
// appears here automatically and no language is hardcoded outside phonetic.go.
// It is a pure read over the default registry: no store, no auth scope, and no
// failure path.
func (s *campaignManagement) ListSupportedLanguages(
	_ context.Context,
	_ *connect.Request[managementv1.ListSupportedLanguagesRequest],
) (*connect.Response[managementv1.ListSupportedLanguagesResponse], error) {
	return connect.NewResponse(&managementv1.ListSupportedLanguagesResponse{
		Languages: address.DefaultEncoders().Languages(),
	}), nil
}
