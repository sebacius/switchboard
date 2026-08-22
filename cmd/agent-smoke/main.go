// Command agent-smoke drives the REAL call supervisor against a real LLM —
// Ollama, or an OpenAI-compatible endpoint — with a fake CallSession, so the
// model's behavior can be exercised without SIP, RTP, ASR, or TTS.
//
// The model is given as [provider/]model, the same as --llm-model:
//
//	go run ./cmd/agent-smoke --model qwen3:8b
//	OPENAI_API_KEY=... go run ./cmd/agent-smoke --model openai/gpt-4o
//
// It is the spike rig for the open question in the design: does qwen3 keep its
// tool-calling accuracy with thinking turned off? Unit tests use a scripted
// client and prove the runner's control flow; this proves the model actually
// emits the right tool calls for a real prompt.
//
// The fake session prints what the real one would do:
//
//	>>> tts: <text>          the supervisor speaking (TTS)
//	>>> answer               the 200 OK (supervisor took the media)
//	>>> forward user/105     a PRE-ANSWER forward (the silent-route path)
//	>>> dial user/105        a POST-ANSWER bridge
//	>>> hangup: <reason>     teardown
//
// Lines you type on stdin become caller speech events. Ctrl-D ends the call.
//
// Usage:
//
//	go run ./cmd/agent-smoke --direction internal --caller 102 --callee 105
//	go run ./cmd/agent-smoke --direction inbound --caller +15551234567 --callee 5558001200
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/sebas/switchboard/internal/signaling/agent"
	"github.com/sebas/switchboard/internal/signaling/dialog"
	"github.com/sebas/switchboard/internal/signaling/llm"
	"github.com/sebas/switchboard/internal/signaling/mediaclient"
)

func main() {
	var (
		llmServer    = flag.String("llm-server", "", "LLM server URL (default: the provider's own)")
		model        = flag.String("model", "qwen3:8b", "Supervisor model as [provider/]model (providers: ollama, openai)")
		tenant       = flag.String("tenant", "devtenant", "Tenant whose prompt to load")
		caller       = flag.String("caller", "102", "Caller (From user part)")
		callee       = flag.String("callee", "105", "Callee (To user part / DID)")
		direction    = flag.String("direction", "internal", "Call direction: internal | inbound | outbound")
		settingsPath = flag.String("settings-path", "resources/config", "Directory containing settings.md")
		tenantsPath  = flag.String("tenants-path", "resources/tenants", "Directory containing tenant .md files")
		policyPath   = flag.String("policy-config", "resources/config/policy.json", "Class-of-Service configuration")
		verbose      = flag.Bool("verbose", false, "Log the model's thinking field (never spoken; useful to spot leakage)")
		turnTimeout  = flag.Duration("turn-timeout", 30*time.Second, "Per-turn deadline. The production default is 30s because the first turn runs inside the INVITE transaction; raise it only to measure slow hardware.")
	)
	flag.Parse()

	level := slog.LevelWarn
	if *verbose {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	dir := agent.Direction(*direction)
	switch dir {
	case agent.DirectionInternal, agent.DirectionInbound, agent.DirectionOutbound:
	default:
		fmt.Fprintf(os.Stderr, "invalid --direction %q (want internal, inbound, or outbound)\n", *direction)
		os.Exit(2)
	}

	prompts, err := agent.NewPromptStore(*settingsPath, *tenantsPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load prompts: %v\n", err)
		os.Exit(1)
	}
	if _, ok := prompts.TenantPrompt(*tenant); !ok {
		fmt.Fprintf(os.Stderr, "tenant %q is not loaded (looked in %s); admission would reject this call\n", *tenant, *tenantsPath)
		fmt.Fprintf(os.Stderr, "loaded tenants: %v\n", prompts.Tenants())
		os.Exit(1)
	}

	policyCfg, err := agent.LoadPolicyConfig(*policyPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load policy: %v\n", err)
		os.Exit(1)
	}

	// The routing table supplies the symbolic targets the model may dial. The
	// harness does not run the deterministic resolver — it exists to exercise the
	// supervisor — but the model must be offered the same names it would be
	// offered in production, or the smoke test measures a different system.
	routingStore, err := agent.NewRoutingStore(*tenantsPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load routing tables: %v\n", err)
		os.Exit(1)
	}

	// The HTTP client timeout must be at least the turn deadline, or it fires
	// first and --turn-timeout does nothing. In production the opposite ordering
	// is correct (a 30s turn ctx inside a 60s HTTP timeout), but here the whole
	// point of the flag is to outwait slow hardware.
	provider, modelID, err := llm.ParseModelRef(*model)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	chat, err := llm.New(llm.Config{
		Provider:  provider,
		ServerURL: *llmServer,
		Model:     modelID,
		APIKey:    os.Getenv("OPENAI_API_KEY"),
		Timeout:   *turnTimeout + 30*time.Second,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "llm: %v\n", err)
		os.Exit(1)
	}

	cc := agent.CallContext{
		Caller:    *caller,
		Callee:    *callee,
		Direction: dir,
		Tenant:    *tenant,
	}

	runner := agent.NewRunner(agent.RunnerConfig{
		Prompts:     prompts,
		Model:       modelID,
		Chat:        chat,
		Logger:      logger,
		TurnTimeout: *turnTimeout,
		BuildExecutor: func(cc agent.CallContext) agent.ToolExecutor {
			policy := agent.NewPolicy(cc.Tenant,
				policyCfg.TenantPolicyFor(cc.Tenant, agent.SymbolicTargetsFor(routingStore, cc.Tenant)),
				logger)
			// No parking service in the harness: park/unpark need real media.
			registry := agent.BuildRegistry(cc, policy, agent.RegistryDeps{Logger: logger})
			return agent.NewCallExecutor(registry, policy, "")
		},
	})

	session := newFakeSession(cc)

	fmt.Printf("--- call start: %s %s -> %s (tenant %s, provider %s, model %s)\n",
		dir, cc.Caller, cc.Callee, cc.Tenant, provider, modelID)
	fmt.Println("--- type caller speech and press enter; Ctrl-D ends the call")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Stdin becomes the caller: each line is one utterance the fake Listen hands
	// to the runner's speech producer.
	go func() {
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			session.say(scanner.Text())
		}
		fmt.Println("--- caller hung up (EOF)")
		_ = session.Hangup("caller hung up")
	}()

	if err := runner.HandleCall(ctx, session, cc); err != nil {
		fmt.Fprintf(os.Stderr, "supervisor error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("--- call end")
}

// fakeSession implements agent.CallSession by printing what a real session would
// do. It models the one piece of real state that matters to the answer decision:
// whether the leg has been answered.
type fakeSession struct {
	cc agent.CallContext

	mu         sync.Mutex
	answered   bool
	terminated bool

	// speech carries caller utterances from stdin to Listen. Closed on EOF,
	// which makes Listen return and the call end.
	speech chan string
	closed sync.Once

	ctx    context.Context
	cancel context.CancelFunc
}

func newFakeSession(cc agent.CallContext) *fakeSession {
	ctx, cancel := context.WithCancel(context.Background())
	return &fakeSession{
		cc:     cc,
		speech: make(chan string, 8),
		ctx:    ctx,
		cancel: cancel,
	}
}

// say pushes one caller utterance, dropping it if the call is already gone.
func (f *fakeSession) say(text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	select {
	case f.speech <- text:
	case <-f.ctx.Done():
	}
}

// close ends the call the way a caller BYE would.
func (f *fakeSession) close() {
	f.closed.Do(func() { close(f.speech) })
}

func (f *fakeSession) CallID() string            { return "smoke-call-1" }
func (f *fakeSession) Destination() string       { return f.cc.Callee }
func (f *fakeSession) CallerID() string          { return f.cc.Caller }
func (f *fakeSession) Domain() string            { return f.cc.Tenant + ".switchboard.local" }
func (f *fakeSession) Context() context.Context  { return f.ctx }
func (f *fakeSession) GetDialog() *dialog.Dialog { return nil }
func (f *fakeSession) GetSessionID() string      { return "smoke-session-1" }
func (f *fakeSession) StopAudio() error          { return nil }
func (f *fakeSession) GetTransport() mediaclient.Transport {
	return nil
}

// MarkRinging is a no-op here: the harness has no SIP transaction to hold open.
func (f *fakeSession) MarkRinging() {}

func (f *fakeSession) HasAnswered() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.answered
}

func (f *fakeSession) Answer(context.Context) error {
	f.mu.Lock()
	if f.answered {
		f.mu.Unlock()
		return nil
	}
	f.answered = true
	f.mu.Unlock()
	fmt.Println(">>> answer")
	return nil
}

func (f *fakeSession) PlayTTS(_ context.Context, text, _ string) error {
	fmt.Printf(">>> tts: %s\n", text)
	return nil
}

func (f *fakeSession) PlayAudio(_ context.Context, file string) error {
	fmt.Printf(">>> play_audio: %s\n", file)
	return nil
}

// Listen blocks for the next stdin line. A closed channel means the caller hung
// up, so it cancels the call context rather than spinning.
func (f *fakeSession) Listen(ctx context.Context, _, _ int) (string, error) {
	select {
	case text, ok := <-f.speech:
		if !ok {
			f.cancel()
			return "", context.Canceled
		}
		return text, nil
	case <-ctx.Done():
		return "", ctx.Err()
	case <-f.ctx.Done():
		return "", context.Canceled
	}
}

// Forward is the pre-answer path. In a real call it would relay 180 and the
// target's own 200; here it prints and ends the call, since a forwarded call
// leaves the supervisor's control.
func (f *fakeSession) Forward(_ context.Context, target string, _ time.Duration) error {
	fmt.Printf(">>> forward %s (pre-answer; caller hears real ringback)\n", target)
	f.cancel()
	return nil
}

// ForwardGroup is the pre-answer ring-group path. The harness prints the rounds
// rather than ringing anything: the supervisor never calls this (resolution
// does), so it exists to satisfy the interface and to make an accidental call
// from the runner visible instead of silent.
func (f *fakeSession) ForwardGroup(_ context.Context, rounds [][]string, _ time.Duration) error {
	fmt.Printf(">>> ring group %v (pre-answer)\n", rounds)
	f.cancel()
	return nil
}

// Dial is the post-answer bridge path.
func (f *fakeSession) Dial(_ context.Context, target string, _ time.Duration) error {
	fmt.Printf(">>> dial %s (post-answer bridge)\n", target)
	f.cancel()
	return nil
}

func (f *fakeSession) Hangup(reason string) error {
	f.mu.Lock()
	if f.terminated {
		f.mu.Unlock()
		return nil
	}
	f.terminated = true
	f.mu.Unlock()

	fmt.Printf(">>> hangup: %s\n", reason)
	f.cancel()
	f.close()
	return nil
}

func (f *fakeSession) IsTerminated() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.terminated
}

func (f *fakeSession) TerminateDialog(callID, reason string) error {
	fmt.Printf(">>> terminate dialog %s: %s\n", callID, reason)
	return nil
}
