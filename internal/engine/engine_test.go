package engine

import (
	"errors"
	"testing"
)

func TestResponse_Err_NilByDefault(t *testing.T) {
	t.Parallel()

	r := &Response{}
	if err := r.Err(); err != nil {
		t.Errorf("expected nil error, got %v", err)
	}
}

func TestResponse_SetStreamErr_And_Err(t *testing.T) {
	t.Parallel()

	r := &Response{}
	testErr := errors.New("stream broken")
	r.SetStreamErr(testErr)

	got := r.Err()
	if got == nil {
		t.Fatal("expected non-nil error after SetStreamErr")
	}
	if got.Error() != "stream broken" {
		t.Errorf("Err() = %q, want %q", got.Error(), "stream broken")
	}
}

func TestResponse_SetStreamErr_Overwrites(t *testing.T) {
	t.Parallel()

	r := &Response{}
	r.SetStreamErr(errors.New("first"))
	r.SetStreamErr(errors.New("second"))

	got := r.Err()
	if got == nil {
		t.Fatal("expected non-nil error")
	}
	if got.Error() != "second" {
		t.Errorf("Err() = %q, want %q", got.Error(), "second")
	}
}

func TestResponse_Fields(t *testing.T) {
	t.Parallel()

	audioCh := make(chan []byte, 1)
	finalTextCh := make(chan struct{})
	notifyDone := make(chan bool, 1)

	r := &Response{
		Text:           "Hello, adventurer!",
		Audio:          audioCh,
		SampleRate:     24000,
		Channels:       1,
		FinalText:      finalTextCh,
		FinalTextValue: "Hello, adventurer!",
		NotifyDone:     notifyDone,
	}

	if r.Text != "Hello, adventurer!" {
		t.Errorf("Text = %q, want %q", r.Text, "Hello, adventurer!")
	}
	if r.SampleRate != 24000 {
		t.Errorf("SampleRate = %d, want %d", r.SampleRate, 24000)
	}
	if r.Channels != 1 {
		t.Errorf("Channels = %d, want %d", r.Channels, 1)
	}
	if r.FinalTextValue != "Hello, adventurer!" {
		t.Errorf("FinalTextValue = %q, want %q", r.FinalTextValue, "Hello, adventurer!")
	}
}

func TestPromptContext_Fields(t *testing.T) {
	t.Parallel()

	pc := PromptContext{
		SystemPrompt: "You are a wise sage.",
		HotContext:   "The player is in a tavern.",
		BudgetTier:   1,
	}

	if pc.SystemPrompt != "You are a wise sage." {
		t.Errorf("SystemPrompt = %q", pc.SystemPrompt)
	}
	if pc.HotContext != "The player is in a tavern." {
		t.Errorf("HotContext = %q", pc.HotContext)
	}
}

func TestContextUpdate_Fields(t *testing.T) {
	t.Parallel()

	cu := ContextUpdate{
		Identity: "A grumpy dwarf.",
		Scene:    "Dark cave.",
	}

	if cu.Identity != "A grumpy dwarf." {
		t.Errorf("Identity = %q", cu.Identity)
	}
	if cu.Scene != "Dark cave." {
		t.Errorf("Scene = %q", cu.Scene)
	}
}
