package tts

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newFakeTTS stands up a TTS server that decodes the request body and returns
// WAV-ish bytes, recording what it was asked for.
func newFakeTTS(t *testing.T) (*httptest.Server, *SpeechRequest) {
	t.Helper()
	var captured SpeechRequest

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/audio/speech" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &captured); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte("RIFF....WAVE"))
	}))
	t.Cleanup(srv.Close)
	return srv, &captured
}

// An empty voice must never reach the server. Piper rejects it with
// `Error loading voice: , KeyError: ”` (HTTP 400), which silences the assistant
// completely — it can hear the caller and decide what to say, but nothing comes
// out. That only surfaces on a live call, so it is worth pinning here.
func TestSynthesizeDefaultsEmptyVoice(t *testing.T) {
	srv, captured := newFakeTTS(t)

	client := NewClient(Config{ServerURL: srv.URL})
	audio, err := client.Synthesize(context.Background(), "hello", "")
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}

	if captured.Voice == "" {
		t.Fatal("an empty voice must be replaced before the request goes out")
	}
	if captured.Voice != DefaultVoice {
		t.Fatalf("expected the default voice %q, got %q", DefaultVoice, captured.Voice)
	}
	if DefaultVoice == "" {
		t.Fatal("DefaultVoice must be a real voice id: an empty one is the bug")
	}
	if len(audio) == 0 {
		t.Fatal("expected audio bytes back")
	}
}

// A caller that names a voice gets exactly that voice — the default must not
// override a deliberate choice.
func TestSynthesizePassesExplicitVoiceThrough(t *testing.T) {
	srv, captured := newFakeTTS(t)

	client := NewClient(Config{ServerURL: srv.URL})
	if _, err := client.Synthesize(context.Background(), "hello", "echo"); err != nil {
		t.Fatalf("Synthesize: %v", err)
	}

	if captured.Voice != "echo" {
		t.Fatalf("expected the requested voice to be used, got %q", captured.Voice)
	}
	if captured.Input != "hello" || captured.Model != "tts-1" || captured.ResponseFormat != "wav" {
		t.Fatalf("unexpected request shape: %+v", *captured)
	}
}

// The server's error body is what made the empty-voice failure diagnosable, so
// it has to survive into the Go error rather than being flattened to a status.
func TestSynthesizeSurfacesServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"message":"Error loading voice: , KeyError: ''"}`))
	}))
	defer srv.Close()

	client := NewClient(Config{ServerURL: srv.URL})
	_, err := client.Synthesize(context.Background(), "hello", "nonexistent-voice")
	if err == nil {
		t.Fatal("expected an error for a 400 response")
	}
	if !strings.Contains(err.Error(), "400") || !strings.Contains(err.Error(), "Error loading voice") {
		t.Fatalf("expected status and server body in the error, got %v", err)
	}
}

// An unconfigured client fails fast rather than posting to an empty URL.
func TestSynthesizeRequiresServerURL(t *testing.T) {
	client := NewClient(Config{})
	if client.Ready() {
		t.Fatal("a client with no server URL must not report ready")
	}
	if _, err := client.Synthesize(context.Background(), "hello", "alloy"); err == nil {
		t.Fatal("expected an error when no TTS server is configured")
	}
}
