package llm

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// fakeOllama serves just enough of Ollama's API for the probe: a model listing
// and a chat endpoint that records what it was sent.
type fakeOllama struct {
	models    []string
	chatCalls int
	lastChat  map[string]any
}

func (f *fakeOllama) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/tags", func(w http.ResponseWriter, _ *http.Request) {
		out := tagsResponse{}
		for _, m := range f.models {
			out.Models = append(out.Models, struct {
				Name string `json:"name"`
			}{Name: m})
		}
		_ = json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("/api/chat", func(w http.ResponseWriter, r *http.Request) {
		f.chatCalls++
		f.lastChat = map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&f.lastChat)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"message": map[string]any{"role": "assistant", "content": "ok"},
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func quiet() *slog.Logger {
	return slog.New(slog.NewTextHandler(newDiscard(), &slog.HandlerOptions{Level: slog.LevelError + 1}))
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }
func newDiscard() discardWriter                   { return discardWriter{} }

// Ready used to return `serverURL != ""`, which was true for a URL pointing at
// nothing at all. It has to mean the server actually answered, because that is
// the question anyone asking it wants answered.
func TestReadyReflectsTheServerNotTheFlag(t *testing.T) {
	fake := &fakeOllama{models: []string{"qwen3:8b"}}
	srv := fake.server(t)

	if c := NewClient(Config{ServerURL: srv.URL}); !c.Ready() {
		t.Fatal("a reachable server must be Ready")
	}
	// A well-formed URL pointing at nothing: the old implementation said yes.
	if c := NewClient(Config{ServerURL: "http://127.0.0.1:1"}); c.Ready() {
		t.Fatal("an unreachable server must not be Ready")
	}
	if c := NewClient(Config{ServerURL: ""}); c.Ready() {
		t.Fatal("an unconfigured client must not be Ready")
	}
}

// Every chat request carries keep_alive. Without it Ollama unloads the model
// after 5 idle minutes, so warming it at startup would buy exactly one quiet
// spell before the next caller pays the load again.
func TestChatSendsKeepAlive(t *testing.T) {
	fake := &fakeOllama{models: []string{"qwen3:8b"}}
	srv := fake.server(t)
	c := NewClient(Config{ServerURL: srv.URL, KeepAlive: "45m"})

	if _, err := c.ChatNative(context.Background(), []NativeMessage{{Role: "user"}}, nil, "qwen3:8b"); err != nil {
		t.Fatalf("ChatNative: %v", err)
	}
	if got := fake.lastChat["keep_alive"]; got != "45m" {
		t.Fatalf("keep_alive = %v, want 45m", got)
	}
}

func TestKeepAliveDefaultsToSomethingLongerThanOllamas(t *testing.T) {
	fake := &fakeOllama{models: []string{"qwen3:8b"}}
	srv := fake.server(t)
	c := NewClient(Config{ServerURL: srv.URL}) // no KeepAlive set

	if _, err := c.ChatNative(context.Background(), []NativeMessage{{Role: "user"}}, nil, "qwen3:8b"); err != nil {
		t.Fatalf("ChatNative: %v", err)
	}
	got, _ := fake.lastChat["keep_alive"].(string)
	if got != DefaultKeepAlive {
		t.Fatalf("keep_alive = %q, want the default %q", got, DefaultKeepAlive)
	}
	d, err := time.ParseDuration(DefaultKeepAlive)
	if err != nil {
		t.Fatalf("the default must be a duration Ollama understands: %v", err)
	}
	if d <= 5*time.Minute {
		t.Fatalf("the default (%s) must exceed Ollama's own 5m, or it changes nothing", DefaultKeepAlive)
	}
}

// The happy path: model present, so the probe loads it and no caller pays.
func TestProbeAndWarmLoadsThePresentModel(t *testing.T) {
	fake := &fakeOllama{models: []string{"qwen3:8b", "qwen3:0.6b"}}
	srv := fake.server(t)
	c := NewClient(Config{ServerURL: srv.URL})

	ProbeAndWarm(context.Background(), c, "qwen3:8b", quiet())

	if fake.chatCalls != 1 {
		t.Fatalf("expected exactly one warm-up chat, got %d", fake.chatCalls)
	}
}

// The failure this probe exists for: the server is healthy, so every other
// signal looks fine, and each call fails on a model the host never had.
func TestProbeAndWarmDoesNotWarmAMissingModel(t *testing.T) {
	fake := &fakeOllama{models: []string{"llama3.2:3b"}}
	srv := fake.server(t)
	c := NewClient(Config{ServerURL: srv.URL})

	ProbeAndWarm(context.Background(), c, "qwen3:8b", quiet())

	if fake.chatCalls != 0 {
		t.Fatalf("a missing model must not be warmed, got %d chat calls", fake.chatCalls)
	}
}

// An unreachable LLM must not panic or block: the supervisor is optional, and
// deterministic resolution routes calls without it.
func TestProbeAndWarmSurvivesAnUnreachableServer(t *testing.T) {
	c := NewClient(Config{ServerURL: "http://127.0.0.1:1"})

	done := make(chan struct{})
	go func() {
		ProbeAndWarm(context.Background(), c, "qwen3:8b", quiet())
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(probeTimeout + 5*time.Second):
		t.Fatal("the probe must not hang when the server is unreachable")
	}
}

func TestHasModel(t *testing.T) {
	fake := &fakeOllama{models: []string{"qwen3:8b"}}
	srv := fake.server(t)
	c := NewClient(Config{ServerURL: srv.URL})

	if ok, err := c.HasModel(context.Background(), "qwen3:8b"); err != nil || !ok {
		t.Fatalf("HasModel(present) = %v, %v", ok, err)
	}
	if ok, err := c.HasModel(context.Background(), "nope:1b"); err != nil || ok {
		t.Fatalf("HasModel(absent) = %v, %v", ok, err)
	}
}
