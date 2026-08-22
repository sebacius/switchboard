package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeOpenAI is an OpenAI-compatible server that records what it was sent, so a
// test can assert on the request as well as the response.
type fakeOpenAI struct {
	mu sync.Mutex

	models    []string
	status    int
	reply     string // raw JSON body for /v1/chat/completions
	modelsRaw string // raw JSON body for /v1/models, overriding models

	lastBody map[string]any
	lastAuth string
	authSeen bool
	chatting int
}

func (f *fakeOpenAI) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.lastAuth = r.Header.Get("Authorization")
		_, f.authSeen = r.Header["Authorization"]
		if f.status != 0 && f.status != http.StatusOK {
			w.WriteHeader(f.status)
			_, _ = w.Write([]byte(`{"error":{"message":"nope"}}`))
			return
		}
		if f.modelsRaw != "" {
			_, _ = w.Write([]byte(f.modelsRaw))
			return
		}
		data := make([]map[string]string, 0, len(f.models))
		for _, m := range f.models {
			data = append(data, map[string]string{"id": m})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	})

	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.chatting++
		f.lastAuth = r.Header.Get("Authorization")
		_, f.authSeen = r.Header["Authorization"]

		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.lastBody = body

		if f.status != 0 && f.status != http.StatusOK {
			w.WriteHeader(f.status)
			_, _ = w.Write([]byte(f.reply))
			return
		}
		reply := f.reply
		if reply == "" {
			reply = `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`
		}
		_, _ = w.Write([]byte(reply))
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func (f *fakeOpenAI) body() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastBody
}

func (f *fakeOpenAI) chatCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.chatting
}

// newTestOpenAI builds a client against a fake, bypassing the hosted-API key
// rule the way a real gateway deployment does.
func newTestOpenAI(t *testing.T, f *fakeOpenAI, key string) *OpenAIClient {
	t.Helper()
	srv := f.server(t)
	c, err := newOpenAIClient(Config{ServerURL: srv.URL, Model: "gpt-4o", APIKey: key})
	if err != nil {
		t.Fatalf("newOpenAIClient: %v", err)
	}
	return c
}

// think and keep_alive are Ollama's; /v1 rejects unrecognized top-level
// arguments, so sending them would break every call rather than being ignored.
func TestOpenAIRequestOmitsOllamaParameters(t *testing.T) {
	f := &fakeOpenAI{}
	c := newTestOpenAI(t, f, "sk-test")

	tools := []ToolDef{{Type: "function", Function: ToolFunction{Name: "dial", Description: "dial"}}}
	if _, err := c.ChatNative(context.Background(), []NativeMessage{{Role: "user", Content: "hi"}}, tools, ""); err != nil {
		t.Fatalf("ChatNative: %v", err)
	}

	body := f.body()
	for _, key := range []string{"think", "keep_alive"} {
		if _, ok := body[key]; ok {
			t.Errorf("request body carries %q, which /v1 does not accept", key)
		}
	}
	if stream, ok := body["stream"].(bool); !ok || stream {
		t.Errorf("stream = %v, want false", body["stream"])
	}
	if _, ok := body["tools"]; !ok {
		t.Error("tool definitions must be sent: both providers accept the same shape")
	}
}

func TestOpenAISendsBearerOnlyWhenItHasAKey(t *testing.T) {
	t.Run("with a key", func(t *testing.T) {
		f := &fakeOpenAI{}
		c := newTestOpenAI(t, f, "sk-test")
		if _, err := c.ChatNative(context.Background(), []NativeMessage{{Role: "user"}}, nil, ""); err != nil {
			t.Fatalf("ChatNative: %v", err)
		}
		if f.lastAuth != "Bearer sk-test" {
			t.Errorf("Authorization = %q, want a bearer token", f.lastAuth)
		}
	})

	// An empty Bearer is worse than no header: a gateway allowing anonymous
	// access would reject it.
	t.Run("without a key", func(t *testing.T) {
		f := &fakeOpenAI{}
		c := newTestOpenAI(t, f, "")
		if _, err := c.ChatNative(context.Background(), []NativeMessage{{Role: "user"}}, nil, ""); err != nil {
			t.Fatalf("ChatNative: %v", err)
		}
		if f.authSeen {
			t.Errorf("Authorization header present with no key: %q", f.lastAuth)
		}
	})
}

// The conversation is translated, not re-tagged: /v1 rejects message properties
// it does not recognize, so thinking and tool_name must be absent rather than
// empty.
func TestOpenAIMessageTranslation(t *testing.T) {
	f := &fakeOpenAI{}
	c := newTestOpenAI(t, f, "sk-test")

	conv := []NativeMessage{
		{Role: "system", Content: "you are a receptionist"},
		{Role: "assistant", Thinking: "route to sales", ToolCalls: []ToolCall{
			{ID: "call_1", Function: ToolCallFunction{Name: "dial", Arguments: map[string]any{"target": "user/105"}}},
		}},
		{Role: "tool", ToolName: "dial", ToolCallID: "call_1", Content: "forwarded"},
	}
	if _, err := c.ChatNative(context.Background(), conv, nil, ""); err != nil {
		t.Fatalf("ChatNative: %v", err)
	}

	raw, _ := json.Marshal(f.body())
	got := string(raw)
	for _, forbidden := range []string{`"thinking"`, `"tool_name"`} {
		if strings.Contains(got, forbidden) {
			t.Errorf("request carries %s, which /v1 rejects: %s", forbidden, got)
		}
	}

	msgs, _ := f.body()["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(msgs))
	}

	assistant, _ := msgs[1].(map[string]any)
	// An assistant message with neither content nor tool_calls is a 400, so
	// content must be present even when empty.
	if _, ok := assistant["content"]; !ok {
		t.Error("assistant message has no content key; an absent content is a 400")
	}
	calls, _ := assistant["tool_calls"].([]any)
	if len(calls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(calls))
	}
	call, _ := calls[0].(map[string]any)
	if call["id"] != "call_1" {
		t.Errorf("tool call id = %v, want call_1", call["id"])
	}
	if call["type"] != "function" {
		t.Errorf("tool call type = %v, want function", call["type"])
	}
	fn, _ := call["function"].(map[string]any)
	// Arguments go out as a JSON string here, unlike Ollama's object.
	args, ok := fn["arguments"].(string)
	if !ok {
		t.Fatalf("arguments = %T, want a JSON string", fn["arguments"])
	}
	if !strings.Contains(args, `"target"`) {
		t.Errorf("arguments = %q, want the target encoded", args)
	}

	toolMsg, _ := msgs[2].(map[string]any)
	if toolMsg["tool_call_id"] != "call_1" {
		t.Errorf("tool result tool_call_id = %v, want call_1", toolMsg["tool_call_id"])
	}
}

// A history with no ids — a scripted conversation, or a gateway that omits them —
// must still produce a well-formed request rather than one that is conditionally
// malformed.
func TestOpenAISynthesizesMissingToolCallIDs(t *testing.T) {
	f := &fakeOpenAI{}
	c := newTestOpenAI(t, f, "sk-test")

	conv := []NativeMessage{
		{Role: "assistant", ToolCalls: []ToolCall{{Function: ToolCallFunction{Name: "dial"}}}},
		{Role: "tool", ToolName: "dial", Content: "forwarded"},
	}
	if _, err := c.ChatNative(context.Background(), conv, nil, ""); err != nil {
		t.Fatalf("ChatNative: %v", err)
	}

	msgs, _ := f.body()["messages"].([]any)
	assistant, _ := msgs[0].(map[string]any)
	calls, _ := assistant["tool_calls"].([]any)
	call, _ := calls[0].(map[string]any)
	callID, _ := call["id"].(string)
	if callID == "" {
		t.Fatal("tool call went out with an empty id")
	}
	toolMsg, _ := msgs[1].(map[string]any)
	resultID, _ := toolMsg["tool_call_id"].(string)
	if resultID == "" {
		t.Fatal("tool result went out with an empty tool_call_id")
	}
	// Non-empty is not enough. Two independently synthesized ids would both be
	// non-empty and still be a 400 — matching is the whole point.
	if resultID != callID {
		t.Errorf("tool result answers %q but the call was advertised as %q; a mismatched pair is exactly the 400 the fallback exists to avoid", resultID, callID)
	}
}

// With several un-ided calls in one turn, each result must find its own call
// rather than all collapsing onto the first.
func TestOpenAICorrelatesSeveralSynthesizedIDs(t *testing.T) {
	f := &fakeOpenAI{}
	c := newTestOpenAI(t, f, "sk-test")

	conv := []NativeMessage{
		{Role: "assistant", ToolCalls: []ToolCall{
			{Function: ToolCallFunction{Name: "lookup"}},
			{Function: ToolCallFunction{Name: "dial"}},
		}},
		{Role: "tool", ToolName: "lookup", Content: "extension 105"},
		{Role: "tool", ToolName: "dial", Content: "forwarded"},
	}
	if _, err := c.ChatNative(context.Background(), conv, nil, ""); err != nil {
		t.Fatalf("ChatNative: %v", err)
	}

	msgs, _ := f.body()["messages"].([]any)
	assistant, _ := msgs[0].(map[string]any)
	calls, _ := assistant["tool_calls"].([]any)
	if len(calls) != 2 {
		t.Fatalf("expected 2 advertised calls, got %d", len(calls))
	}

	advertised := map[string]string{} // tool name -> id
	for _, raw := range calls {
		call, _ := raw.(map[string]any)
		fn, _ := call["function"].(map[string]any)
		name, _ := fn["name"].(string)
		id, _ := call["id"].(string)
		advertised[name] = id
	}

	for i, name := range []string{"lookup", "dial"} {
		toolMsg, _ := msgs[i+1].(map[string]any)
		got, _ := toolMsg["tool_call_id"].(string)
		if want := advertised[name]; got != want {
			t.Errorf("result for %q carries id %q, want %q", name, got, want)
		}
	}
	if advertised["lookup"] == advertised["dial"] {
		t.Error("both calls were advertised under the same id")
	}
}

// OpenAI sends arguments as a JSON string where Ollama sends an object. The
// runner and every tool handler must not be able to tell the difference.
func TestOpenAIDecodesStringToolArguments(t *testing.T) {
	f := &fakeOpenAI{reply: `{"choices":[{"message":{"role":"assistant","content":"",
		"tool_calls":[{"id":"call_9","type":"function","function":{"name":"dial","arguments":"{\"target\":\"user/105\"}"}}]}}]}`}
	c := newTestOpenAI(t, f, "sk-test")

	res, err := c.ChatNative(context.Background(), []NativeMessage{{Role: "user"}}, nil, "")
	if err != nil {
		t.Fatalf("ChatNative: %v", err)
	}
	if len(res.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(res.ToolCalls))
	}
	if got := res.ToolCalls[0].ID; got != "call_9" {
		t.Errorf("ID = %q, want call_9", got)
	}
	if got := res.ToolCalls[0].Function.Arguments["target"]; got != "user/105" {
		t.Errorf("Arguments[target] = %v, want user/105", got)
	}
}

// A formatting slip in the arguments is recoverable: every consequential tool
// validates its arguments and returns an actionable message the model corrects
// next turn. Failing the turn instead would make the caller hear the
// "assistant is unavailable" apology over nothing.
func TestOpenAIMalformedArgumentsDoNotFailTheTurn(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{"empty object", `{}`},
		{"empty string", ``},
		{"whitespace", `   `},
		{"json null", `null`},
		{"truncated json", `{\"target\": `},
		{"not json at all", `user/105`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reply := `{"choices":[{"message":{"role":"assistant","content":"",
				"tool_calls":[{"id":"c1","type":"function","function":{"name":"dial","arguments":"` + tt.raw + `"}}]}}]}`
			f := &fakeOpenAI{reply: reply}
			c := newTestOpenAI(t, f, "sk-test")

			res, err := c.ChatNative(context.Background(), []NativeMessage{{Role: "user"}}, nil, "")
			if err != nil {
				t.Fatalf("a malformed argument must not fail the turn: %v", err)
			}
			if len(res.ToolCalls) != 1 {
				t.Fatalf("expected the tool call to survive, got %d", len(res.ToolCalls))
			}
			// Handlers read from this map directly; nil would panic them.
			if res.ToolCalls[0].Function.Arguments == nil {
				t.Error("Arguments is nil; handlers index into it directly")
			}
		})
	}
}

// The guarantee this whole provider had to earn: whatever shape reasoning
// arrives in, it is never eligible for TTS.
func TestOpenAIReasoningNeverReachesContent(t *testing.T) {
	tests := []struct {
		name         string
		content      string
		reasoning    string
		reasonField  string
		wantContent  string
		wantThinking string
	}{
		{"provider reasoning field", "Hello", "deliberating", "reasoning_content", "Hello", "deliberating"},
		{"openrouter reasoning field", "Hello", "deliberating", "reasoning", "Hello", "deliberating"},
		{"inline think tags", "<think>plan</think>Hello", "", "", "Hello", "plan"},
		{"unterminated think tag", "<think>plan", "", "", "", "plan"},
		{"orphaned close tag", "plan</think>Hello", "", "", "Hello", "plan"},
		{"multiple spans", "a<think>x</think>b<think>y</think>c", "", "", "abc", "x\ny"},
		{"no reasoning at all", "Hello", "", "", "Hello", ""},
		{"both sources at once", "<think>a</think>Hi", "r", "reasoning_content", "Hi", "r\na"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := map[string]any{"role": "assistant", "content": tt.content}
			if tt.reasonField != "" {
				msg[tt.reasonField] = tt.reasoning
			}
			reply, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": msg}}})

			f := &fakeOpenAI{reply: string(reply)}
			c := newTestOpenAI(t, f, "sk-test")

			res, err := c.ChatNative(context.Background(), []NativeMessage{{Role: "user"}}, nil, "")
			if err != nil {
				t.Fatalf("ChatNative: %v", err)
			}
			if res.Content != tt.wantContent {
				t.Errorf("Content = %q, want %q", res.Content, tt.wantContent)
			}
			if res.Thinking != tt.wantThinking {
				t.Errorf("Thinking = %q, want %q", res.Thinking, tt.wantThinking)
			}
			// The invariant, stated independently of the table: whatever
			// reasoning existed must not be sitting in the spoken half.
			if tt.wantThinking != "" && strings.Contains(res.Content, tt.wantThinking) {
				t.Errorf("reasoning %q leaked into spoken content %q", tt.wantThinking, res.Content)
			}
		})
	}
}

// A wall of JSON in a log is a worse debugging experience than the one sentence
// the provider already wrote.
func TestOpenAIErrorsPreferTheProviderMessage(t *testing.T) {
	f := &fakeOpenAI{
		status: http.StatusTooManyRequests,
		reply:  `{"error":{"message":"rate limit reached for gpt-4o","type":"rate_limit_error"}}`,
	}
	c := newTestOpenAI(t, f, "sk-test")

	_, err := c.ChatNative(context.Background(), []NativeMessage{{Role: "user"}}, nil, "")
	if err == nil {
		t.Fatal("expected an error on a 429")
	}
	if !strings.Contains(err.Error(), "429") {
		t.Errorf("error must name the status, got: %v", err)
	}
	if !strings.Contains(err.Error(), "rate limit reached") {
		t.Errorf("error must carry the provider's own message, got: %v", err)
	}
}

// Rejected credentials and an unreachable server look identical in a log and
// have completely different fixes, so they must be distinguishable in code.
func TestOpenAIUnauthorizedIsDistinguishable(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		f := &fakeOpenAI{status: status, reply: `{"error":{"message":"invalid api key"}}`}
		c := newTestOpenAI(t, f, "sk-wrong")

		_, err := c.ChatNative(context.Background(), []NativeMessage{{Role: "user"}}, nil, "")
		if !errors.Is(err, ErrUnauthorized) {
			t.Errorf("status %d: err = %v, want it to wrap ErrUnauthorized", status, err)
		}
		if strings.Contains(err.Error(), "sk-wrong") {
			t.Errorf("status %d: the key leaked into the error: %v", status, err)
		}
	}
}

func TestOpenAIReadyReflectsTheServer(t *testing.T) {
	t.Run("server answers", func(t *testing.T) {
		f := &fakeOpenAI{models: []string{"gpt-4o"}}
		if c := newTestOpenAI(t, f, "sk-test"); !c.Ready() {
			t.Error("Ready() = false against a server that answers")
		}
	})

	t.Run("credentials rejected", func(t *testing.T) {
		f := &fakeOpenAI{status: http.StatusUnauthorized}
		if c := newTestOpenAI(t, f, "sk-wrong"); c.Ready() {
			t.Error("Ready() = true against a server that rejected us")
		}
	})

	t.Run("nothing serving", func(t *testing.T) {
		c, err := newOpenAIClient(Config{ServerURL: "http://127.0.0.1:1", Model: "gpt-4o"})
		if err != nil {
			t.Fatalf("newOpenAIClient: %v", err)
		}
		if c.Ready() {
			t.Error("Ready() = true for a URL nothing is serving")
		}
	})
}

// A hosted provider has no model to load, so a warm-up request would buy nothing
// and may be billed.
func TestOpenAIProbeNeverWarms(t *testing.T) {
	f := &fakeOpenAI{models: []string{"gpt-4o"}}
	c := newTestOpenAI(t, f, "sk-test")

	ProbeAndWarm(context.Background(), c, "gpt-4o", quiet())

	if n := f.chatCalls(); n != 0 {
		t.Errorf("the probe sent %d chat requests to a hosted provider, want 0", n)
	}
}

// Gateways serve models they do not enumerate, so absence from the listing must
// not be reported as a failure the way it is for Ollama.
func TestOpenAIProbeToleratesAnAdvisoryListing(t *testing.T) {
	f := &fakeOpenAI{models: []string{"something-else"}}
	c := newTestOpenAI(t, f, "sk-test")

	if c.ProbeProfile().ModelListAuthoritative {
		t.Error("an OpenAI-compatible model listing must not be treated as conclusive")
	}
	if c.ProbeProfile().Warmable {
		t.Error("a hosted provider must not be marked warmable")
	}

	ProbeAndWarm(context.Background(), c, "gpt-4o", quiet())
	if n := f.chatCalls(); n != 0 {
		t.Errorf("the probe sent %d chat requests, want 0", n)
	}
}

func TestOpenAIListModels(t *testing.T) {
	f := &fakeOpenAI{models: []string{"gpt-4o", "gpt-4o-mini"}}
	c := newTestOpenAI(t, f, "sk-test")

	names, err := c.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(names) != 2 || names[0] != "gpt-4o" {
		t.Errorf("ListModels() = %v, want the advertised ids", names)
	}
}
