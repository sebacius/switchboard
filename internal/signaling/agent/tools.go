package agent

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sebas/switchboard/internal/signaling/llm"
)

// defaultDialTimeout bounds a dial handler's outbound attempt. The runner's
// turnCtx still caps the whole turn; this is the SIP-side patience window.
const defaultDialTimeout = 30 * time.Second

// Tool is an agent-level callable: the schema advertised to the model plus the
// handler and the disposition its successful execution implies. This is the
// agent's own tool type (not llm.AgentTool) because a tool here carries a
// Disposition and a CallSession-typed handler that the LLM layer cannot express.
type Tool struct {
	Name        string
	Description string
	// Params is the JSON-schema "parameters" object advertised to the model.
	Params map[string]any
	// Disposition is the loop verdict returned on a successful run (Continue for
	// play_audio/dial, Terminal for hangup).
	Disposition Disposition
	// External marks a tool capable of reaching an external destination. Such a
	// tool is never placed in an inbound call's registry (affordance removal).
	External bool
	// Handler performs the tool's effect against the session. A returned error is
	// a real operation failure (the executor surfaces it as an actionable result,
	// never as an aborting Go error to the loop). Argument validation that should
	// drive model self-correction is done in the executor before the handler runs.
	Handler func(ctx context.Context, args map[string]any, sess CallSession) (string, error)
}

// BuildRegistry selects the per-call tool set from (tenant, direction) and the
// authorization policy (design #8). The strongest fraud defense lives here:
//
//   - An INBOUND call gets NO external-dial-capable tool at all. The capability
//     is removed from the affordance set, so the model cannot be injected into a
//     capability it was never offered — there is nothing to authorize because
//     there is nothing to call.
//   - INTERNAL / OUTBOUND calls get `dial`, but only when the tenant policy
//     enables external dialing (internal "user/..." routing is always reachable
//     via dial; external reach is what the policy gate controls). Every dial is
//     still adjudicated by the Policy at execution time — the registry is the
//     coarse affordance filter, the Policy is the fine-grained authorizer.
//
// hangup and play_audio are unconditional (no external reach).
func BuildRegistry(cc CallContext, policy *Policy) []Tool {
	tools := []Tool{hangupTool(), playAudioTool()}

	// Inbound trunk peers never receive a dial affordance. Internal directory
	// users and outbound directory users may dial, gated by policy below.
	if cc.Direction == DirectionInbound {
		return tools
	}

	// dial reaches external destinations only when the tenant enabled it; absent
	// that, dial is still useful for internal "user/..." routing, so we offer it
	// for internal/outbound regardless, and the Policy denies external reach.
	tools = append(tools, dialTool(policy != nil && policy.cfg.AllowExternalDial))

	// TODO(group7): register park (DispositionParked) and unpark (BridgeMedia
	// port) here once parking.Service is wired in. They are entangled with the
	// answer model (a parked call must already own media) and need the parking
	// slot lifecycle, so they are deliberately NOT registered yet.

	return tools
}

// CallExecutor is the per-call ToolExecutor. It holds this call's registry, the
// authorization policy, and per-call dedup state. It is constructed once per
// HandleCall (via the runner's BuildExecutor seam) and is used from the single
// dispatch loop, so it needs no internal locking for the dedup state — but we
// guard it anyway to stay safe if a future producer dispatches concurrently.
type CallExecutor struct {
	tools  map[string]Tool
	policy *Policy

	mu       sync.Mutex
	lastFail string // signature of the most recent failed (name,args), "" if none
}

// NewCallExecutor builds the executor over a per-call registry and policy.
func NewCallExecutor(tools []Tool, policy *Policy) *CallExecutor {
	m := make(map[string]Tool, len(tools))
	for _, t := range tools {
		m[t.Name] = t
	}
	return &CallExecutor{tools: m, policy: policy}
}

// Execute adjudicates and runs one tool call (design #10 / #12). The contract
// with the runner: a returned Go error aborts nothing here — we return nil error
// and an actionable result string in every model-recoverable case, and only use
// the disposition to drive the loop.
//
//   - unknown tool        → Terminal (hang up; the model emitted a tool we never
//     offered, which is a contract violation, design #10).
//   - missing/invalid arg → actionable result + Continue (self-correction).
//   - identical just-failed call repeated → refusal result + Continue.
//   - dial → run through Policy (capability narrowing + COS + spend); a deny
//     returns the deny reason + Continue.
//   - otherwise           → run the handler; on handler error surface an
//     actionable result + Continue; on success return (result, tool.Disposition).
func (e *CallExecutor) Execute(ctx context.Context, call llm.ToolCall, sess CallSession) (string, Disposition, error) {
	name := call.Function.Name
	args := call.Function.Arguments

	tool, ok := e.tools[name]
	if !ok {
		// The model emitted a tool outside its registry. Per design #10 this is a
		// terminal contract violation, not a recoverable arg error.
		return fmt.Sprintf("tool %q is not available; ending the call", name), DispositionTerminal, nil
	}

	// Refuse a verbatim repeat of the call that just failed, so the model is
	// nudged toward an alternative instead of re-running the same dead end.
	sig := callSignature(name, args)
	e.mu.Lock()
	repeated := e.lastFail != "" && e.lastFail == sig
	e.mu.Unlock()
	if repeated {
		return "you already tried that exact call and it failed; do something else (offer voicemail, ask for a different extension, or hang up)", DispositionContinue, nil
	}

	// dial is the only authorized path; everything else (hangup, play_audio) runs
	// its handler directly with arg validation inside the handler closure.
	if name == "dial" {
		result, disp, failed := e.executeDial(ctx, args, sess)
		e.recordOutcome(sig, failed)
		return result, disp, nil
	}

	result, err := tool.Handler(ctx, args, sess)
	if err != nil {
		// A real operation failure (e.g. playback failed): surface it actionably
		// and keep the call alive so the model can recover.
		e.recordOutcome(sig, true)
		return fmt.Sprintf("%s failed: %v", name, err), DispositionContinue, nil
	}
	e.recordOutcome(sig, false)
	return result, tool.Disposition, nil
}

// executeDial validates the dial argument, authorizes the symbolic target via
// the Policy, then invokes the dial handler. It returns the result, disposition,
// and whether the call "failed" for dedup purposes (a missing arg, a policy deny,
// or a handler error all count as failures the model should not repeat verbatim).
func (e *CallExecutor) executeDial(ctx context.Context, args map[string]any, sess CallSession) (string, Disposition, bool) {
	target, ok := stringArg(args, "target")
	if !ok || strings.TrimSpace(target) == "" {
		// Missing required arg → actionable Continue, not a Go error (spec:
		// "Missing required argument").
		return "dial requires a 'target'; offer voicemail or ask the caller for an extension", DispositionContinue, true
	}

	if e.policy == nil {
		return "dialing is not permitted (no policy configured); offer voicemail or another extension", DispositionContinue, true
	}

	resolved, decision := e.policy.AuthorizeDial(target)
	if !decision.Allowed {
		// Policy deny → the reason becomes the model-visible result; Continue so
		// the model can pick an allowed alternative (spec: "Denied tool does not
		// execute"). The handler never runs.
		return decision.Reason, DispositionContinue, true
	}

	// Authorized: run the real dial against the resolved (never the raw) target.
	if _, err := dialHandler(ctx, resolved, sess); err != nil {
		return fmt.Sprintf("dial to the requested destination failed: %v; offer voicemail or another extension", err), DispositionContinue, true
	}
	return fmt.Sprintf("dialed %s", resolved), DispositionContinue, false
}

// recordOutcome updates the just-failed signature: a failed call is remembered
// so a verbatim repeat is refused; a success clears the marker.
func (e *CallExecutor) recordOutcome(sig string, failed bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if failed {
		e.lastFail = sig
	} else {
		e.lastFail = ""
	}
}

// callSignature is a stable identity for a (name, args) pair used by the repeat
// detector. Keys are sorted so map iteration order does not change the signature.
func callSignature(name string, args map[string]any) string {
	if len(args) == 0 {
		return name + "()"
	}
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(name)
	b.WriteByte('(')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "%s=%v", k, args[k])
	}
	b.WriteByte(')')
	return b.String()
}

// stringArg reads a string argument, tolerating the JSON-decoded any types the
// model may emit (string is the norm; numbers arrive as float64).
func stringArg(args map[string]any, key string) (string, bool) {
	v, ok := args[key]
	if !ok {
		return "", false
	}
	switch s := v.(type) {
	case string:
		return s, true
	case fmt.Stringer:
		return s.String(), true
	default:
		return fmt.Sprintf("%v", v), true
	}
}

// toToolDefs converts a per-call registry to the wire format advertised to the
// model. Exposed so the runner's toolDefs() TODO can later source advertised
// tools from the same registry that backs the executor, keeping advertised and
// executable tools in lockstep. NOT wired into the runner in this group.
func toToolDefs(tools []Tool) []llm.ToolDef {
	defs := make([]llm.ToolDef, 0, len(tools))
	for _, t := range tools {
		params := t.Params
		if params == nil {
			params = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		defs = append(defs, llm.ToolDef{
			Type: "function",
			Function: llm.ToolFunction{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  params,
			},
		})
	}
	return defs
}
