package asr

import (
	"context"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// capturedRequest is what the fake ASR server saw, so a test can assert on the
// multipart body the client actually put on the wire.
type capturedRequest struct {
	fields map[string]string
	file   []byte
}

// newFakeASR stands up an ASR server that parses the multipart body and replies
// with a fixed transcript. It records the parts so tests can inspect them.
func newFakeASR(t *testing.T, reply string) (*httptest.Server, *capturedRequest) {
	t.Helper()
	captured := &capturedRequest{fields: map[string]string{}}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/audio/transcriptions" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}

		_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil {
			http.Error(w, "bad content type", http.StatusBadRequest)
			return
		}
		mr := multipart.NewReader(r.Body, params["boundary"])
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				http.Error(w, "bad multipart", http.StatusBadRequest)
				return
			}
			data, _ := io.ReadAll(part)
			if part.FileName() != "" {
				captured.file = data
			} else {
				captured.fields[part.FormName()] = string(data)
			}
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text":"` + reply + `"}`))
	}))
	t.Cleanup(srv.Close)
	return srv, captured
}

// The model field is REQUIRED by the OpenAI transcription contract. Omitting it
// made faster-whisper servers reject every request with a 422, which showed up
// as the supervisor never hearing the caller — so this is the regression test
// for that bug.
func TestTranscribeSendsModelField(t *testing.T) {
	srv, captured := newFakeASR(t, "hello there")

	client := NewClient(Config{ServerURL: srv.URL, Model: "Systran/faster-whisper-base"})
	text, err := client.Transcribe(context.Background(), []byte("RIFFfake-wav-data"))
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}

	if got := captured.fields["model"]; got != "Systran/faster-whisper-base" {
		t.Fatalf("expected the configured model on the wire, got %q", got)
	}
	if string(captured.file) != "RIFFfake-wav-data" {
		t.Fatalf("audio payload not transmitted intact, got %q", captured.file)
	}
	if text != "hello there" {
		t.Fatalf("expected the parsed transcript, got %q", text)
	}
}

// An unset model must not mean "send nothing" — that is exactly the 422. It
// falls back to a real model id instead.
func TestNewClientDefaultsModel(t *testing.T) {
	srv, captured := newFakeASR(t, "ok")

	client := NewClient(Config{ServerURL: srv.URL})
	if _, err := client.Transcribe(context.Background(), []byte("wav")); err != nil {
		t.Fatalf("Transcribe: %v", err)
	}

	if got := captured.fields["model"]; got != DefaultModel {
		t.Fatalf("expected the default model %q, got %q", DefaultModel, got)
	}
	if DefaultModel == "" {
		t.Fatal("DefaultModel must be a real model id: an empty one reproduces the 422")
	}
}

// The server's own error body is the thing that made this bug diagnosable at
// all (it named the missing field), so it must survive into the Go error.
func TestTranscribeSurfacesServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"detail":[{"loc":["body","model"],"msg":"Field required"}]}`))
	}))
	defer srv.Close()

	client := NewClient(Config{ServerURL: srv.URL})
	_, err := client.Transcribe(context.Background(), []byte("wav"))
	if err == nil {
		t.Fatal("expected an error for a 422 response")
	}
	if !strings.Contains(err.Error(), "422") || !strings.Contains(err.Error(), "Field required") {
		t.Fatalf("expected the status and server body in the error, got %v", err)
	}
}

// An unconfigured client fails fast with a clear message rather than issuing a
// request to an empty URL.
func TestTranscribeRequiresServerURL(t *testing.T) {
	client := NewClient(Config{})
	if client.Ready() {
		t.Fatal("a client with no server URL must not report ready")
	}
	if _, err := client.Transcribe(context.Background(), []byte("wav")); err == nil {
		t.Fatal("expected an error when no ASR server is configured")
	}
}
