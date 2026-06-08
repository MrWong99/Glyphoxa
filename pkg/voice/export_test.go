package voice

import (
	"context"

	"github.com/disgoorg/snowflake/v2"

	"github.com/MrWong99/Glyphoxa/pkg/voice/mock"
)

// This file bridges the package-private seam (voiceManager/voiceConn) to the
// fakes in pkg/voice/mock for white-box tests, and re-exports the internal
// constructors and pieces the _test files exercise directly. It compiles only
// under `go test`, so the production build never sees these adapters.

// fakeManager adapts *mock.Manager to the unexported voiceManager seam: the
// mock can't name voiceConn, so the *mock.Conn it returns is wrapped here.
type fakeManager struct{ m *mock.Manager }

func (f fakeManager) CreateConn(guild snowflake.ID) voiceConn { return f.m.CreateConn(guild) }
func (f fakeManager) RemoveConn(guild snowflake.ID)           { f.m.RemoveConn(guild) }

// newTestManager builds a Manager over a fake voice manager.
func newTestManager(m *mock.Manager, opts ...ManagerOption) *Manager {
	return newManager(fakeManager{m}, opts...)
}

// Test-only exports of the internal pieces the white-box tests drive directly.

func newTestSwitchingProvider() *switchingProvider { return &switchingProvider{} }

func newTestPlaySlot(pb *Playback, src Source, ctx context.Context) *playSlot {
	return &playSlot{pb: pb, src: src, ctx: ctx}
}

func (p *switchingProvider) testSwap(slot *playSlot) *playSlot { return p.swap(slot) }

func newTestInboundDispatcher(guild string, buf int, m MetricsRecorder) *inboundDispatcher {
	return newInboundDispatcher(guild, buf, m)
}
