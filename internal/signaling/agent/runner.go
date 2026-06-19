package agent

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/sebas/switchboard/internal/signaling/llm"
)

// Default tuning. Caps are configurable via RunnerConfig; these apply when a
// field is left zero.
const (
	defaultSoftCap          = 5
	defaultHardCap          = 10
	defaultEventBuffer      = 16
	defaultListenMaxMs      = 30000
	defaultListenSilenceMs  = 800
	defaultTurnTimeout      = 30 * time.Second
	defaultRunawayHardSpeak = "I'm having trouble completing that. Goodbye."
)

// RunnerConfig holds the per-runner (effectively per-tenant-deployment) wiring.
// CallContext, the system prompt block, and the live session are passed into
// HandleCall, not stored here, so one Runner can serve many concurrent calls.
type RunnerConfig struct {
	// TenantPrompt is the tenant system instruction appended after the
	// per-call CallContext block to form the system message.
	TenantPrompt string

	// Model is the LLM model id passed to ChatNative ("" lets the client default).
	Model string

	// Voice is the TTS voice used for every PlayTTS in the call.
	Voice string

	// Chat is the native tool-calling LLM client.
	Chat llm.ChatClient

	// Tools executes tool calls the model emits (registry + policy live behind it).
	Tools ToolExecutor

	// Logger is the structured logger; slog.Default() is used when nil.
	Logger *slog.Logger

	// SoftCap bounds consecutive autonomous turns before the runner stops
	// re-prompting and falls back to reactive-only. Zero uses defaultSoftCap.
	SoftCap int

	// HardCap bounds consecutive autonomous turns before the runner speaks a
	// deterministic message and tears down. Zero uses defaultHardCap.
	HardCap int

	// RunawayMessage is spoken when the hard cap trips. Empty uses a default.
	RunawayMessage string

	// EventBuffer sizes the events channel. Zero uses defaultEventBuffer.
	EventBuffer int

	// TurnTimeout bounds a single turn (the LLM call + tool dispatch). Zero uses
	// defaultTurnTimeout. The turnCtx is the runaway breaker's lever too.
	TurnTimeout time.Duration

	// ListenMaxMs / ListenSilenceMs parameterise the speech producer's Listen.
	ListenMaxMs     int
	ListenSilenceMs int
}

// Runner is a configured supervisor factory. It is safe for concurrent calls;
// all per-call mutable state lives in callRun, created per HandleCall.
type Runner struct {
	cfg RunnerConfig
	log *slog.Logger
}

// NewRunner builds a Runner, filling in defaults for any unset config fields.
func NewRunner(cfg RunnerConfig) *Runner {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.SoftCap == 0 {
		cfg.SoftCap = defaultSoftCap
	}
	if cfg.HardCap == 0 {
		cfg.HardCap = defaultHardCap
	}
	if cfg.RunawayMessage == "" {
		cfg.RunawayMessage = defaultRunawayHardSpeak
	}
	if cfg.EventBuffer == 0 {
		cfg.EventBuffer = defaultEventBuffer
	}
	if cfg.TurnTimeout == 0 {
		cfg.TurnTimeout = defaultTurnTimeout
	}
	if cfg.ListenMaxMs == 0 {
		cfg.ListenMaxMs = defaultListenMaxMs
	}
	if cfg.ListenSilenceMs == 0 {
		cfg.ListenSilenceMs = defaultListenSilenceMs
	}
	return &Runner{cfg: cfg, log: cfg.Logger}
}

// callRun is the mutable state for one supervised call. It owns the nested
// context tree, the conversation, the events channel, and the once-guarded
// teardown funnel (decision #5, #6, #13).
type callRun struct {
	cfg     RunnerConfig
	log     *slog.Logger
	session CallSession

	// callCtx is the whole-call scope. Cancelling it (BYE/CANCEL/timeout/terminal
	// tool) is the only shutdown signal; the events channel is never closed.
	callCtx    context.Context
	callCancel context.CancelFunc

	// events carries producer notifications (speech today). Buffered; multiple
	// producers; never closed. Producers send ctx-safe via sendEvent.
	events chan Event

	// conversation is the whole-call message history (decision #13). The system
	// message is index 0 and is never trimmed.
	conversation []llm.NativeMessage

	// autonomousTurns counts consecutive autonomous (tool-result-driven) turns.
	// Reset by any caller event; the runaway breaker reads it (decision #12).
	autonomousTurns int

	// teardownOnce guards the idempotent teardown funnel (decision #6).
	teardownOnce sync.Once

	// teardownWG tracks producer goroutines (the speech loop) so HandleCall waits
	// for them to unwind on callCtx cancellation, leaving no leaked goroutine.
	teardownWG sync.WaitGroup
}

// HandleCall supervises one call from the first turn through teardown. It owns
// the call until callCtx is cancelled (by the caller, a terminal tool, a timeout,
// or the parent cancelling callCtx). It returns nil on a clean end.
func (r *Runner) HandleCall(callCtx context.Context, session CallSession, cc CallContext) error {
	if r.cfg.Chat == nil {
		return fmt.Errorf("runner: no chat client configured")
	}
	if r.cfg.Tools == nil {
		return fmt.Errorf("runner: no tool executor configured")
	}

	ctx, cancel := context.WithCancel(callCtx)
	run := &callRun{
		cfg:        r.cfg,
		log:        r.log.With("call_id", session.CallID()),
		session:    session,
		callCtx:    ctx,
		callCancel: cancel,
		events:     make(chan Event, r.cfg.EventBuffer),
	}
	// Final safety net: whatever path HandleCall exits by, the funnel runs (it is
	// idempotent, so an earlier teardown makes this a no-op).
	defer run.teardown("handler-exit")

	// Seed the conversation with the system message (decision #4/#13): the
	// per-call CallContext block followed by the tenant prompt. Never trimmed.
	system := cc.FormatForPrompt()
	if r.cfg.TenantPrompt != "" {
		system = system + "\n" + r.cfg.TenantPrompt
	}
	run.conversation = []llm.NativeMessage{{Role: "system", Content: system}}

	return run.run()
}

// run executes the first-turn decision, then (if the call engaged media) starts
// the speech producer and drains the event loop until callCtx is cancelled.
func (c *callRun) run() error {
	// First-turn single-shot decision (decisions #8/#11): system + empty user
	// message. This turn is reactive (it is the caller's INVITE), so it does not
	// count against the runaway breaker.
	if err := c.runTurn(c.callCtx, ""); err != nil {
		if c.callCtx.Err() != nil {
			// Cancellation (teardown) raced the first turn; not an error.
			return nil
		}
		return fmt.Errorf("first turn: %w", err)
	}
	if c.callCtx.Err() != nil {
		// A terminal tool (or external cancel) ended the call on turn one.
		return nil
	}

	// The call engaged media: start the speech producer and enter the loop.
	c.teardownWG.Add(1)
	go func() {
		defer c.teardownWG.Done()
		c.speechLoop()
	}()

	c.dispatchLoop()

	// dispatchLoop only returns after teardown cancelled callCtx, which unblocks
	// the speech producer's Listen. Wait for it so HandleCall never leaks the
	// producer goroutine (decision #5 / the "no goroutine leak" guarantee).
	c.teardownWG.Wait()
	return nil
}

// dispatchLoop is the single consumer of the events channel. The top-level
// select only observes cancellation between turns; every blocking thing inside
// a turn honors its own scope (decision #5). The channel is never closed.
func (c *callRun) dispatchLoop() {
	for {
		select {
		case <-c.callCtx.Done():
			c.teardown("ctx-cancelled")
			return
		case ev := <-c.events:
			// A caller event is reactive: reset the runaway counter (decision #12).
			c.autonomousTurns = 0
			if err := c.runTurn(c.callCtx, ev.Payload); err != nil {
				if c.callCtx.Err() != nil {
					return
				}
				c.log.Warn("turn failed", "kind", ev.Kind.String(), "error", err)
			}
		}
	}
}

// runTurn runs one model turn under a fresh turnCtx (child of callCtx), then any
// tool calls it emitted. Every turn is processed identically (decision #8): speak
// any content, then execute any tool calls — so tool-only routes silently,
// content-only speaks, and both speak-then-route. The first-turn vs reactive vs
// autonomous distinction lives in the callers (run / dispatchLoop /
// autonomousReprompt), which control the empty-vs-caller user text and whether
// the turn counts against the runaway breaker.
func (c *callRun) runTurn(parent context.Context, userText string) error {
	turnCtx, turnCancel := context.WithTimeout(parent, c.cfg.TurnTimeout)
	defer turnCancel()

	// Append the user message (empty on the first turn) before prompting.
	c.conversation = append(c.conversation, llm.NativeMessage{Role: "user", Content: userText})

	result, err := c.cfg.Chat.ChatNative(turnCtx, c.conversation, c.toolDefs(), c.cfg.Model)
	if err != nil {
		return fmt.Errorf("chat: %w", err)
	}

	// Record the assistant message verbatim (content + thinking + tool_calls) so
	// the next prompt carries the model's own prior reasoning (decision #13).
	c.conversation = append(c.conversation, llm.NativeMessage{
		Role:      "assistant",
		Content:   result.Content,
		Thinking:  result.Thinking,
		ToolCalls: result.ToolCalls,
	})

	// Decision #8/#11 ordering: speak first (if any content), then execute tools.
	// Both → speak then route; content-only → speak; tool-only → route silently.
	if result.Content != "" {
		if err := c.speak(turnCtx, result.Content); err != nil {
			// A barge-in / turn-abort cancels playback but not the call; only a
			// call-level cancel is fatal here.
			if c.callCtx.Err() != nil {
				return err
			}
			c.log.Warn("tts playback failed", "error", err)
		}
	}

	if len(result.ToolCalls) == 0 {
		return nil
	}
	return c.executeTools(turnCtx, result.ToolCalls)
}

// executeTools dispatches each tool call through the ToolExecutor, recording its
// result back into the conversation, then re-prompts autonomously once (subject
// to the runaway breaker) so the tool result feeds back into the model.
func (c *callRun) executeTools(turnCtx context.Context, calls []llm.ToolCall) error {
	terminal := false
	for _, call := range calls {
		select {
		case <-turnCtx.Done():
			return turnCtx.Err()
		default:
		}

		result, disp, err := c.cfg.Tools.Execute(turnCtx, call, c.session)
		if err != nil {
			// Executor-internal failure: feed an actionable result back so the
			// model can recover, and keep going (decision #12).
			c.log.Warn("tool execute error", "tool", call.Function.Name, "error", err)
			if result == "" {
				result = fmt.Sprintf("tool %q failed: %v", call.Function.Name, err)
			}
		}

		c.conversation = append(c.conversation, llm.NativeMessage{
			Role:     "tool",
			ToolName: call.Function.Name,
			Content:  result,
		})

		switch disp {
		case DispositionTerminal:
			terminal = true
		case DispositionParked:
			// The loop holds the call alive; do not re-prompt. Unpark or
			// cancellation drives the next move (decision #5 / Parked disposition).
			c.log.Info("call parked", "tool", call.Function.Name)
			return nil
		case DispositionContinue:
			// fall through; re-prompt below.
		}
	}

	if terminal {
		c.teardown("terminal-tool")
		return nil
	}

	// Tool results fed back: re-prompt autonomously, bounded by the breaker.
	return c.autonomousReprompt(turnCtx)
}

// autonomousReprompt drives the "tool result feeds back" loop. Each pass is an
// autonomous turn (no caller input), so it increments the breaker counter:
//   - soft cap reached → stop re-prompting, wait for the next caller event.
//   - hard cap reached → speak a deterministic message and tear down.
func (c *callRun) autonomousReprompt(parent context.Context) error {
	c.autonomousTurns++

	if c.autonomousTurns >= c.cfg.HardCap {
		c.log.Warn("runaway breaker: hard cap reached, tearing down",
			"autonomous_turns", c.autonomousTurns, "hard_cap", c.cfg.HardCap)
		// Best-effort deterministic goodbye under the call scope, then teardown.
		if err := c.speak(c.callCtx, c.cfg.RunawayMessage); err != nil {
			c.log.Warn("runaway goodbye tts failed", "error", err)
		}
		c.teardown("runaway-hard-cap")
		return nil
	}

	if c.autonomousTurns >= c.cfg.SoftCap {
		c.log.Warn("runaway breaker: soft cap reached, falling back to reactive-only",
			"autonomous_turns", c.autonomousTurns, "soft_cap", c.cfg.SoftCap)
		// Stop autonomous re-prompting; the next caller event resets the counter.
		return nil
	}

	// Re-prompt with no new user text; the tool results are already in history.
	return c.runTurn(parent, "")
}

// speak plays text via TTS under a playbackCtx (child of the supplied scope) so
// a future barge-in can cancel just the prompt. The barge-in interrupt hook is
// wired here as playbackCancel; speech-onset detection that would invoke it is a
// later-phase media-layer capability (decision #5, designed-in / deferred).
func (c *callRun) speak(parent context.Context, text string) error {
	if text == "" {
		return nil
	}
	playbackCtx, playbackCancel := context.WithCancel(parent)
	defer playbackCancel()

	// TODO(barge-in): when the media layer exposes speech-onset/VAD, a contentless
	// interrupt event should call playbackCancel to cut the prompt mid-utterance
	// while leaving the turn and call alive. The scope already exists for it;
	// defer above is the cleanup, the interrupt lane reuses the same cancel.

	return c.session.PlayTTS(playbackCtx, text, c.cfg.Voice)
}

// speechLoop is the ASR producer. It loops calling Listen under callCtx and
// pushes each transcript onto the events channel (ctx-safe). It exits on
// callCtx cancellation, so teardown stops it without a goroutine leak.
func (c *callRun) speechLoop() {
	for {
		if c.callCtx.Err() != nil {
			return
		}
		text, err := c.session.Listen(c.callCtx, c.cfg.ListenMaxMs, c.cfg.ListenSilenceMs)
		if err != nil {
			if c.callCtx.Err() != nil {
				return
			}
			c.log.Warn("listen failed", "error", err)
			// Avoid a tight error spin if Listen fails fast for a non-ctx reason.
			select {
			case <-c.callCtx.Done():
				return
			case <-time.After(100 * time.Millisecond):
			}
			continue
		}
		if text == "" {
			continue
		}
		if !c.sendEvent(Event{Kind: EventSpeech, Payload: text}) {
			return
		}
	}
}

// sendEvent pushes an event ctx-safely (decision #5): it never blocks past
// callCtx cancellation, so a producer never deadlocks after the consumer exits.
// Returns false if the call was cancelled.
func (c *callRun) sendEvent(ev Event) bool {
	select {
	case c.events <- ev:
		return true
	case <-c.callCtx.Done():
		return false
	}
}

// toolDefs renders the tool definitions advertised to the model. The runner does
// not own the registry; until Group 5 wires a real registry through the executor
// the runner advertises no tools and the executor adjudicates whatever the model
// emits.
//
// TODO(group5): source ToolDefs from the per-call registry that backs ToolExecutor
// so advertised tools and executable tools stay in lockstep.
func (c *callRun) toolDefs() []llm.ToolDef {
	return nil
}

// teardown is the single idempotent teardown funnel (decision #6). Every
// initiator — caller BYE/CANCEL, a terminal tool, a turn/call timeout, the
// HandleCall defer — converges here; the body runs exactly once, guarded by
// sync.Once and gated by IsTerminated().
func (c *callRun) teardown(reason string) {
	c.teardownOnce.Do(func() {
		c.log.Info("teardown", "reason", reason)

		// Cancel the whole context tree first so in-flight turns, tool handlers,
		// Listen, and the speech producer observe cancellation and unwind.
		c.callCancel()

		// Gate the session-level release: if the session already terminated
		// (e.g. the dialog layer hung up), don't double-free.
		if !c.session.IsTerminated() {
			if err := c.session.Hangup(reason); err != nil {
				c.log.Warn("teardown hangup failed", "error", err)
			}
		}

		// TODO(later-phase): release parking slot if parked; CANCEL/BYE any
		// B-leg; release the tenant channel slot; destroy the RTP session. These
		// are owned by groups that wire admission, parking, and the B2BUA bridge.
		// The funnel + once-only semantics are real now; the resource hooks land
		// with their owners.
	})
}
