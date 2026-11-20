package rpc_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/internal/rpc"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// lockedBuffer is a goroutine-safe log sink (the handler logs on the server's
// goroutine, the test reads on its own).
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestCampaignServer_UsesInjectedLogger pins that the CampaignService's feature
// modules log through the logger SetLogger injects, not slog.Default — so the
// composition root's handler (level, format, attributes) applies to them too.
func TestCampaignServer_UsesInjectedLogger(t *testing.T) {
	t.Parallel()
	store := newFakeOrganizeStore()
	store.campaign = storage.Campaign{ID: uuid.New()}
	store.tagErr = errors.New("fake storage failure")

	var sink lockedBuffer
	srv := rpc.NewCampaignServerWith(rpc.CampaignStores{Active: store, Organize: store})
	srv.SetLogger(slog.New(slog.NewTextHandler(&sink, nil)))
	client := campaignClientServe(t, srv)

	_, err := client.SetNodeTags(context.Background(),
		connect.NewRequest(&managementv1.SetNodeTagsRequest{NodeId: uuid.New().String(), Tags: []string{"x"}}))
	if connect.CodeOf(err) != connect.CodeInternal {
		t.Fatalf("code = %v, want Internal", connect.CodeOf(err))
	}
	if !strings.Contains(sink.String(), "SetNodeTags: store write failed") {
		t.Errorf("injected logger got %q, want the handler's failure line", sink.String())
	}
}
