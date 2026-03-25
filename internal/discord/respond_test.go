package discord

import (
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
)

// stubMessageCreator implements messageCreator for testing respond helpers.
type stubMessageCreator struct {
	lastMessage  discord.MessageCreate
	createCalled atomic.Bool
	deferCalled  atomic.Bool
	createErr    error
	deferErr     error
}

func (s *stubMessageCreator) CreateMessage(msg discord.MessageCreate, _ ...rest.RequestOpt) error {
	s.lastMessage = msg
	s.createCalled.Store(true)
	return s.createErr
}

func (s *stubMessageCreator) DeferCreateMessage(_ bool, _ ...rest.RequestOpt) error {
	s.deferCalled.Store(true)
	return s.deferErr
}

// stubModalResponder implements modalResponder for testing RespondModal.
type stubModalResponder struct {
	lastModal   discord.ModalCreate
	modalCalled atomic.Bool
	modalErr    error
}

func (s *stubModalResponder) Modal(modal discord.ModalCreate, _ ...rest.RequestOpt) error {
	s.lastModal = modal
	s.modalCalled.Store(true)
	return s.modalErr
}

func TestRespondEphemeral(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
		err     error
	}{
		{
			name:    "success",
			content: "Hello!",
			err:     nil,
		},
		{
			name:    "error logged but not propagated",
			content: "Fail",
			err:     errors.New("discord API error"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			stub := &stubMessageCreator{createErr: tt.err}
			RespondEphemeral(stub, tt.content)

			if !stub.createCalled.Load() {
				t.Fatal("CreateMessage was not called")
			}
			if stub.lastMessage.Content != tt.content {
				t.Errorf("Content = %q, want %q", stub.lastMessage.Content, tt.content)
			}
			if stub.lastMessage.Flags != discord.MessageFlagEphemeral {
				t.Errorf("Flags = %d, want %d (ephemeral)", stub.lastMessage.Flags, discord.MessageFlagEphemeral)
			}
		})
	}
}

func TestRespondEmbed(t *testing.T) {
	t.Parallel()

	embed := discord.Embed{
		Title:       "Test Embed",
		Description: "Testing embed response",
	}

	stub := &stubMessageCreator{}
	RespondEmbed(stub, embed)

	if !stub.createCalled.Load() {
		t.Fatal("CreateMessage was not called")
	}
	if len(stub.lastMessage.Embeds) != 1 {
		t.Fatalf("expected 1 embed, got %d", len(stub.lastMessage.Embeds))
	}
	if stub.lastMessage.Embeds[0].Title != "Test Embed" {
		t.Errorf("Embed title = %q, want %q", stub.lastMessage.Embeds[0].Title, "Test Embed")
	}
	if stub.lastMessage.Flags != discord.MessageFlagEphemeral {
		t.Errorf("Flags = %d, want ephemeral", stub.lastMessage.Flags)
	}
}

func TestRespondEmbed_Error(t *testing.T) {
	t.Parallel()

	stub := &stubMessageCreator{createErr: errors.New("api down")}
	RespondEmbed(stub, discord.Embed{Title: "Error"})

	// Should not panic; error is logged internally.
	if !stub.createCalled.Load() {
		t.Fatal("CreateMessage was not called")
	}
}

func TestRespondError(t *testing.T) {
	t.Parallel()

	stub := &stubMessageCreator{}
	testErr := errors.New("something went wrong")
	RespondError(stub, testErr)

	if !stub.createCalled.Load() {
		t.Fatal("CreateMessage was not called")
	}
	want := fmt.Sprintf("Error: %v", testErr)
	if stub.lastMessage.Content != want {
		t.Errorf("Content = %q, want %q", stub.lastMessage.Content, want)
	}
	if stub.lastMessage.Flags != discord.MessageFlagEphemeral {
		t.Errorf("Flags = %d, want ephemeral", stub.lastMessage.Flags)
	}
}

func TestRespondModal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
	}{
		{name: "success", err: nil},
		{name: "error logged", err: errors.New("modal open failed")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			stub := &stubModalResponder{modalErr: tt.err}
			modal := discord.ModalCreate{
				CustomID: "test_modal",
				Title:    "Test Modal",
			}
			RespondModal(stub, modal)

			if !stub.modalCalled.Load() {
				t.Fatal("Modal was not called")
			}
			if stub.lastModal.CustomID != "test_modal" {
				t.Errorf("CustomID = %q, want %q", stub.lastModal.CustomID, "test_modal")
			}
			if stub.lastModal.Title != "Test Modal" {
				t.Errorf("Title = %q, want %q", stub.lastModal.Title, "Test Modal")
			}
		})
	}
}

func TestDeferReply(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
	}{
		{name: "success", err: nil},
		{name: "error logged", err: errors.New("defer failed")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			stub := &stubMessageCreator{deferErr: tt.err}
			DeferReply(stub)

			if !stub.deferCalled.Load() {
				t.Fatal("DeferCreateMessage was not called")
			}
		})
	}
}
