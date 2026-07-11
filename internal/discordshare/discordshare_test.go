package discordshare_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MrWong99/Glyphoxa/internal/discordshare"
)

// TestListTextChannels_FiltersSortsAndAuthenticates proves ListTextChannels keeps
// only text channels (type 0), sorts by position then name, and sends the Bot auth
// header on the guild-channels GET.
func TestListTextChannels_FiltersSortsAndAuthenticates(t *testing.T) {
	var gotAuth, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		_, _ = io.WriteString(w, `[
			{"id":"20","type":0,"name":"beta","position":2},
			{"id":"30","type":2,"name":"Voice","position":0},
			{"id":"10","type":0,"name":"alpha","position":1},
			{"id":"40","type":4,"name":"Category","position":3}
		]`)
	}))
	defer srv.Close()

	got, err := discordshare.ListTextChannelsAt(context.Background(), "tok", "guild123", srv.URL, nil)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if want := "Bot tok"; gotAuth != want {
		t.Errorf("Authorization = %q, want %q", gotAuth, want)
	}
	if !strings.Contains(gotPath, "/guilds/guild123/channels") {
		t.Errorf("path = %q, want .../guilds/guild123/channels", gotPath)
	}
	// Only the two text channels, sorted by position (10 before 20).
	if len(got) != 2 {
		t.Fatalf("got %d channels, want 2 (%+v)", len(got), got)
	}
	if got[0].ID != "10" || got[0].Name != "alpha" {
		t.Errorf("first channel = %+v, want {10 alpha}", got[0])
	}
	if got[1].ID != "20" || got[1].Name != "beta" {
		t.Errorf("second channel = %+v, want {20 beta}", got[1])
	}
}

// TestListTextChannels_ForbiddenIsReadable proves a 403 maps to the ErrNoAccess
// sentinel (the Bot is not in the guild), not a raw HTTP error.
func TestListTextChannels_ForbiddenIsReadable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	_, err := discordshare.ListTextChannelsAt(context.Background(), "tok", "g", srv.URL, nil)
	if !errors.Is(err, discordshare.ErrNoAccess) {
		t.Fatalf("err = %v, want ErrNoAccess", err)
	}
}

// TestPostFile_MultipartShape proves PostFile sends payload_json (content +
// attachment descriptor) and a files[0] part with the given filename + content
// type, to the channel-messages endpoint with the Bot auth header.
func TestPostFile_MultipartShape(t *testing.T) {
	type capture struct {
		auth       string
		path       string
		content    string
		attachName string
		fileName   string
		fileCT     string
		fileBytes  []byte
	}
	var cap capture
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.auth = r.Header.Get("Authorization")
		cap.path = r.URL.Path
		_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil {
			t.Errorf("parse content-type: %v", err)
		}
		mr := multipart.NewReader(r.Body, params["boundary"])
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("next part: %v", err)
			}
			switch part.FormName() {
			case "payload_json":
				var pl struct {
					Content     string `json:"content"`
					Attachments []struct {
						Filename string `json:"filename"`
					} `json:"attachments"`
					AllowedMentions struct {
						Parse []string `json:"parse"`
					} `json:"allowed_mentions"`
				}
				if err := json.NewDecoder(part).Decode(&pl); err != nil {
					t.Fatalf("decode payload_json: %v", err)
				}
				cap.content = pl.Content
				if len(pl.Attachments) == 1 {
					cap.attachName = pl.Attachments[0].Filename
				}
				// allowed_mentions.parse MUST be present and EMPTY so an "@everyone" in a
				// transcript excerpt cannot ping the guild (this is a public post).
				if pl.AllowedMentions.Parse == nil {
					t.Error("payload_json missing allowed_mentions.parse (mentions not suppressed)")
				}
				if len(pl.AllowedMentions.Parse) != 0 {
					t.Errorf("allowed_mentions.parse = %v, want empty (all mentions suppressed)", pl.AllowedMentions.Parse)
				}
			case "files[0]":
				cap.fileName = part.FileName()
				cap.fileCT = part.Header.Get("Content-Type")
				cap.fileBytes, _ = io.ReadAll(part)
			}
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	err := discordshare.PostFileAt(context.Background(), "tok", "chan99",
		"A grand moment", "highlight.wav", "audio/wav", []byte("RIFFDATA"), srv.URL, nil)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	if cap.auth != "Bot tok" {
		t.Errorf("auth = %q, want Bot tok", cap.auth)
	}
	if !strings.Contains(cap.path, "/channels/chan99/messages") {
		t.Errorf("path = %q, want .../channels/chan99/messages", cap.path)
	}
	if cap.content != "A grand moment" {
		t.Errorf("content = %q, want caption", cap.content)
	}
	if cap.attachName != "highlight.wav" {
		t.Errorf("attachment filename = %q, want highlight.wav", cap.attachName)
	}
	if cap.fileName != "highlight.wav" {
		t.Errorf("files[0] filename = %q, want highlight.wav", cap.fileName)
	}
	if cap.fileCT != "audio/wav" {
		t.Errorf("files[0] content-type = %q, want audio/wav", cap.fileCT)
	}
	if string(cap.fileBytes) != "RIFFDATA" {
		t.Errorf("files[0] bytes = %q, want RIFFDATA", cap.fileBytes)
	}
}

// TestPostFile_ErrorIsReadable proves a non-2xx (413 oversize, 500) surfaces as an
// *APIError carrying the status, so the RPC can render a readable failure.
func TestPostFile_ErrorIsReadable(t *testing.T) {
	for _, status := range []int{http.StatusRequestEntityTooLarge, http.StatusInternalServerError} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
		}))
		err := discordshare.PostFileAt(context.Background(), "tok", "c",
			"cap", "highlight.wav", "audio/wav", []byte("x"), srv.URL, nil)
		srv.Close()
		var apiErr *discordshare.APIError
		if !errors.As(err, &apiErr) {
			t.Fatalf("status %d: err = %v, want *APIError", status, err)
		}
		if apiErr.Status != status {
			t.Errorf("APIError.Status = %d, want %d", apiErr.Status, status)
		}
	}
}
