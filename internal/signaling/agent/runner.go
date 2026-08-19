package agent

import (
	"context"
	"errors"
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

	// maxListenFailures bounds consecutive Listen errors before the runner gives
	// up on the call. Listen failing repeatedly means the media path is broken
	// (RTP session gone, ASR unreachable, the session's own scope cancelled) and
	// there is nothing left to supervise. Without this bound the producer would
	// retry forever at listenFailureBackoff, spinning for the life of the call.
	maxListenFailures = 5

	// listenFailureBackoff paces retries so a fast-failing Listen cannot spin.
	listenFailureBackoff = 100 * time.Millisecond
)

// RunnerConfig holds the per-runner (effectively per-tenant-deployment) wiring.
// CallContext, the system prompt block, and the live session are passed into
// HandleCall, not stored here, so one Runner can serve many concurrent calls.
type RunnerConfig struct {
	// Prompts resolves the tenant system instruction appended after the per-call
	// CallContext block. It is looked up per call (one Runner serves every
	// tenant), and a tenant with no prompt never reaches the runner — admission
	// already rejected it in preflight.
	Prompts PromptSource

	// Model is the LLM model id passed to ChatNative ("" lets the client default).
	Model string

	// Voice is the TTS voice used for every PlayTTS in the call.
	Voice string

	// Chat is the native tool-calling LLM client.
	Chat llm.ChatClient

	// BuildExecutor constructs the per-call tool executor from the CallContext.
	// The tool registry (and thus the executor) depends on (tenant, direction),
	// so it cannot be a single shared instance — each HandleCall builds its own.
	// The returned executor carries that call's registry, authorization policy,
	// and per-call dedup state (registry + policy live behind it).
	BuildExecutor func(cc CallContext) ToolExecutor

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

// TeardownHook is a per-call cleanup the teardown funnel invokes exactly once,
// inside the sync.Once, after the context tree is cancelled and before the
// session is hung up. It is how resources owned OUTSIDE the runner — the
// admission channel slot above all — are released on every exit path without
// the runner having to know what they are.
type TeardownHook func(reason string)

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
	cfg      RunnerConfig
	log      *slog.Logger
	session  CallSession
	executor ToolExecutor

	// cc is this call's identity. The runner reads Direction from it to apply
	// the first-turn discipline (design #11).
	cc CallContext

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

	// hooks are the per-call external releases (admission slot, and anything a
	// future caller attaches) run inside the teardown funnel's sync.Once.
	hooks []TeardownHook
}

// HandleCall supervises one call from the first turn through teardown. It owns
// the call until callCtx is cancelled (by the caller, a terminal tool, a timeout,
// or the parent cancelling callCtx). It returns nil on a clean end.
func (r *Runner) HandleCall(callCtx context.Context, session CallSession, cc CallContext, hooks ...TeardownHook) error {
	// Misconfiguration bails out before the teardown funnel exists, so these
	// paths must release the caller's resources themselves. They are the worst
	// possible place to leak: a bad config fails EVERY call, so a leaked
	// admission slot here would drain a tenant's capacity within seconds.
	failEarly := func(err error) error {
		for _, hook := range hooks {
			hook("runner-misconfigured")
		}
		if !session.IsTerminated() {
			if err := session.Hangup("runner misconfigured"); err != nil {
				r.log.Warn("hangup after misconfiguration failed", "error", err)
			}
		}
		return err
	}

	if r.cfg.Chat == nil {
		return failEarly(fmt.Errorf("runner: no chat client configured"))
	}
	if r.cfg.BuildExecutor == nil {
		return failEarly(fmt.Errorf("runner: no executor builder configured"))
	}

	// The tool registry depends on (tenant, direction), so the executor is built
	// per call from the CallContext rather than shared across calls.
	executor := r.cfg.BuildExecutor(cc)
	if executor == nil {
		return failEarly(fmt.Errorf("runner: executor builder returned nil"))
	}

	ctx, cancel := context.WithCancel(callCtx)
	run := &callRun{
		hooks:      hooks,
		cc:         cc,
		cfg:        r.cfg,
		log:        r.log.With("call_id", session.CallID()),
		session:    session,
		executor:   executor,
		callCtx:    ctx,
		callCancel: cancel,
		events:     make(chan Event, r.cfg.EventBuffer),
	}
	// Final safety net: whatever path HandleCall exits by, the funnel runs (it is
	// idempotent, so an earlier teardown makes this a no-op).
	defer run.teardown("handler-exit")

	// Seed the conversation with the system message (decision #4/#13): the
	// per-call CallContext block followed by this tenant's prompt. Never trimmed.
	system := cc.FormatForPrompt()
	if r.cfg.Prompts != nil {
		if tenantPrompt, ok := r.cfg.Prompts.TenantPrompt(cc.Tenant); ok {
			system = system + "\n" + tenantPrompt
		}
	}
	// The direction directive goes LAST, after the tenant prompt, so a long
	// knowledge base cannot bury it (design #11).
	if directive := cc.FirstTurnDirective(); directive != "" {
		system = system + "\n\n" + directive
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
	if err := c.runTurn(c.callCtx, "", true); err != nil {
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

	// Reaching here means the first turn did not route the call away and did not
	// end it, so the supervisor is about to converse: it owns this leg's media by
	// definition (design #7). Answer if speaking has not already done so — a
	// listen loop on an unanswered leg has no RTP session to listen to.
	if !c.session.HasAnswered() {
		if err := c.session.Answer(c.callCtx); err != nil {
			c.teardown("answer-failed")
			return fmt.Errorf("answer before listen loop: %w", err)
		}
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
			if err := c.runTurn(c.callCtx, ev.Payload, false); err != nil {
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
func (c *callRun) runTurn(parent context.Context, userText string, firstTurn bool) error {
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

	// Design #11: an internal first turn that came back as prose gets ONE
	// corrective re-prompt. This must happen BEFORE the speak() below, because
	// speaking is irreversible in two ways at once — the caller hears a greeting
	// they should never have got, and speak() answers the call, which destroys
	// the pre-answer forward path for good.
	if firstTurn {
		corrected, err := c.correctSilentRoute(turnCtx, result)
		if err != nil {
			return err
		}
		result = corrected
	}

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

// silentRouteCorrection is the corrective nudge sent when an internal first turn
// answers with prose instead of routing. It is phrased as a caller-side message
// because that is the role the model is already attending to.
const silentRouteCorrection = "Do not speak. This is an internal call that must be routed silently. " +
	"Respond with a single dial tool call for the dialed extension and no text."

// correctSilentRoute implements design #11's one-shot self-correction. On an
// INTERNAL first turn that returned prose and no tool calls, it discards the
// prose and re-prompts once, asking for a bare dial.
//
// Why this exists: the "route, don't greet" instruction lives in settings.md and
// in FirstTurnDirective, but instructions are not guarantees. Live testing showed
// a large tenant prompt is enough to make the model greet anyway, and a greeting
// on a colleague-to-colleague extension dial is precisely the outcome the whole
// forward path exists to avoid.
//
// It returns the result the caller should act on: the retry's result when the
// retry produced tool calls, otherwise the original. Anything other than the
// prose-only internal first-turn case is returned untouched.
func (c *callRun) correctSilentRoute(turnCtx context.Context, result *llm.ChatResult) (*llm.ChatResult, error) {
	if c.cc.Direction != DirectionInternal {
		return result, nil
	}
	if len(result.ToolCalls) > 0 || result.Content == "" {
		// Already routed, or said nothing at all — neither needs correcting.
		return result, nil
	}

	c.log.Info("internal first turn returned prose; re-prompting once for a silent route",
		"discarded_content_len", len(result.Content))

	// Drop the rejected assistant message so the model does not treat its own
	// prose as an example to follow on the retry.
	if n := len(c.conversation); n > 0 && c.conversation[n-1].Role == "assistant" {
		c.conversation = c.conversation[:n-1]
	}

	c.conversation = append(c.conversation, llm.NativeMessage{Role: "user", Content: silentRouteCorrection})

	retry, err := c.cfg.Chat.ChatNative(turnCtx, c.conversation, c.toolDefs(), c.cfg.Model)
	if err != nil {
		return nil, fmt.Errorf("silent-route retry: %w", err)
	}

	c.conversation = append(c.conversation, llm.NativeMessage{
		Role:      "assistant",
		Content:   retry.Content,
		Thinking:  retry.Thinking,
		ToolCalls: retry.ToolCalls,
	})

	if len(retry.ToolCalls) == 0 {
		// The model still will not route. Converse rather than leaving the caller
		// in silence — one retry is the whole budget (design #11), and a second
		// LLM round-trip already spent part of the INVITE transaction.
		c.log.Warn("silent-route retry still returned prose; falling back to conversing")
		return retry, nil
	}
	return retry, nil
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

		result, disp, err := c.executor.Execute(turnCtx, call, c.session)
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
	return c.runTurn(parent, "", false)
}

// speak plays text via TTS under a playbackCtx (child of the supplied scope) so
// a future barge-in can cancel just the prompt. The barge-in interrupt hook is
// wired here as playbackCancel; speech-onset detection that would invoke it is a
// later-phase media-layer capability (decision #5, designed-in / deferred).
func (c *callRun) speak(parent context.Context, text string) error {
	if text == "" {
		return nil
	}
	// Speaking means the supervisor handles this leg's media itself, which is
	// exactly what answering signifies (design #7). The INVITE handler deferred
	// the 200 OK precisely so this decision could be the model's; Answer is
	// idempotent, so later turns pay nothing.
	if err := c.session.Answer(parent); err != nil {
		return fmt.Errorf("answer before speaking: %w", err)
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
//
// A failing Listen is bounded twice over: each retry waits listenFailureBackoff
// so a fast failure cannot spin the CPU, and maxListenFailures consecutive
// errors tear the call down. The second bound is the important one — a session
// whose media is gone fails Listen instantly and forever, and retrying it for
// the life of the call helps nobody.
func (c *callRun) speechLoop() {
	failures := 0
	for {
		if c.callCtx.Err() != nil {
			return
		}

		// The session may have ended on its own scope (its dialog died) while the
		// call context is still live. That is a terminated call, not a retry.
		if c.session.IsTerminated() {
			c.teardown("session-terminated")
			return
		}

		text, err := c.session.Listen(c.callCtx, c.cfg.ListenMaxMs, c.cfg.ListenSilenceMs)
		if err != nil {
			if c.callCtx.Err() != nil {
				return
			}
			// A cancellation that did not come from our call context came from the
			// session's own scope: the leg is gone, so end the call rather than
			// retrying into a dead session.
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				c.log.Info("listen cancelled by the session scope, ending call", "error", err)
				c.teardown("listen-cancelled")
				return
			}

			failures++
			c.log.Warn("listen failed", "error", err, "consecutive_failures", failures)
			if failures >= maxListenFailures {
				c.log.Error("listen failed repeatedly, tearing down",
					"consecutive_failures", failures, "max", maxListenFailures)
				c.teardown("listen-failed")
				return
			}

			select {
			case <-c.callCtx.Done():
				return
			case <-time.After(listenFailureBackoff):
			}
			continue
		}

		// A successful Listen clears the budget: transient ASR errors should not
		// accumulate across an otherwise healthy call.
		failures = 0

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

// toolAdvertiser is the optional half of the ToolExecutor seam: an executor
// that can describe its own registry. CallExecutor implements it, so the runner
// advertises exactly the tools that executor will accept — advertised and
// executable stay in lockstep, and an "unknown tool" is always a real model
// contract violation rather than a registry mismatch. A test executor that does
// not implement it simply advertises nothing.
type toolAdvertiser interface {
	ToolDefs() []llm.ToolDef
}

// toolDefs renders the tool definitions advertised to the model for this call.
func (c *callRun) toolDefs() []llm.ToolDef {
	if adv, ok := c.executor.(toolAdvertiser); ok {
		return adv.ToolDefs()
	}
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

		// Release resources owned outside the runner (the admission channel slot)
		// before touching SIP, so a slow hangup never holds a tenant's capacity.
		for _, hook := range c.hooks {
			hook(reason)
		}

		// Gate the session-level release: if the session already terminated
		// (e.g. the dialog layer hung up), don't double-free. Hangup itself
		// branches pre-answer (487 + CANCEL any orphaned B-leg) versus
		// post-answer (BYE), and the RTP session is destroyed by the dialog
		// manager's OnTerminated callback — as is any parking slot this call
		// held, via parking.Service.CleanupByCallID.
		if !c.session.IsTerminated() {
			if err := c.session.Hangup(reason); err != nil {
				c.log.Warn("teardown hangup failed", "error", err)
			}
		}
	})
}
