package flow

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/sebas/switchboard/internal/signaling/agent"
	"github.com/sebas/switchboard/internal/signaling/dialplan"
)

// outcome is what running a node produced: the exit to take, whether the flow
// ends here, and a human-readable detail for the call record.
type outcome struct {
	exit     string
	terminal bool
	detail   string
}

func exitWith(name, detail string) outcome { return outcome{exit: name, detail: detail} }
func terminalWith(name, d string) outcome  { return outcome{exit: name, terminal: true, detail: d} }

// runNode dispatches one node.
func (e *Engine) runNode(ctx context.Context, sess agent.CallSession, cc agent.CallContext,
	cursor *Cursor, node *dialplan.Node, policy *agent.Policy) outcome {

	switch entry := node.DecodedEntry().(type) {
	case *dialplan.IVREntry:
		return e.runIVR(ctx, sess, cursor, node, entry)
	case *dialplan.TTSEntry:
		return e.runTTS(ctx, sess, entry)
	case *dialplan.PlayAudioEntry:
		return e.runPlayAudio(ctx, sess, entry)
	case *dialplan.DialUserEntry:
		return e.runDialUser(ctx, sess, cc, entry, policy)
	case *dialplan.DialExternalEntry:
		return e.runDialExternal(ctx, sess, entry, policy)
	case *dialplan.TransferEntry:
		return e.runTransfer(ctx, sess, cc, entry, policy)
	case *dialplan.HangupEntry:
		return e.runHangup(sess, entry)
	default:
		return exitWith("failed", fmt.Sprintf("unsupported node type %q", node.Type))
	}
}

// runIVR plays a prompt and collects a selection, retrying within the node.
//
// The retry loop lives HERE rather than as a graph edge. That is what keeps the
// inter-node graph acyclic while still letting a menu re-prompt, and it is why
// max_retries is a bound rather than a suggestion.
func (e *Engine) runIVR(ctx context.Context, sess agent.CallSession, cursor *Cursor,
	node *dialplan.Node, entry *dialplan.IVREntry) outcome {

	nodeCtx, cancel := nodeContext(ctx, entry.TimeoutMs*(entry.MaxRetries+2))
	defer cancel()

	for attempt := 0; ; attempt++ {
		result, err := sess.CollectDigits(nodeCtx, agent.CollectRequest{
			Prompt:              promptFrom(entry.Prompt),
			Interruptible:       entry.Interruptible,
			MaxDigits:           maxDigitsFor(node),
			Terminators:         entry.Terminator,
			FirstDigitTimeoutMs: entry.TimeoutMs,
			// A re-prompt discards type-ahead: the caller's earlier digits
			// answered a question that has since changed.
			FlushBuffer: attempt > 0,
		})
		if err != nil {
			return exitWith("invalid", "collect failed: "+err.Error())
		}

		// A leg with no DTMF transport can never satisfy this node. Retrying
		// would hold the caller in a menu that cannot hear them.
		if result.Reason == agent.CollectNoDTMFTransport {
			return exitWith("retries_exceeded", "no DTMF transport on this leg")
		}
		if result.Reason == agent.CollectCanceled {
			return exitWith("timeout", "caller abandoned")
		}

		cursor.digits = result.Digits

		if result.Digits != "" {
			if _, ok := node.Exits[result.Digits]; ok {
				return exitWith(result.Digits, describeDigits(result.Digits))
			}
		}

		// Nothing usable. Timing out and pressing an unlisted key are different
		// mistakes, and a flow routes them differently.
		exit := "invalid"
		detail := describeDigits(result.Digits) + " (no matching exit)"
		if result.TimedOut() && result.Digits == "" {
			exit, detail = "timeout", "no digits pressed"
		}

		// max_retries bounds re-prompting INSIDE the node. That is the only
		// repetition a flow allows, and it is what lets the inter-node graph
		// stay acyclic while a menu still asks twice.
		//
		// With retries configured, an exhausted node takes retries_exceeded.
		// With none, the first mistake takes timeout or invalid directly, so a
		// flow that wants to route immediately simply sets max_retries to 0.
		if attempt < entry.MaxRetries {
			cursor.retry(node.ID)
			continue
		}
		if entry.MaxRetries > 0 {
			return exitWith("retries_exceeded",
				fmt.Sprintf("%s after %d attempt(s)", detail, attempt+1))
		}
		return exitWith(exit, detail)
	}
}

// maxDigitsFor infers how many digits a menu accepts from the exits it declares,
// so a two-digit menu needs no extra configuration to work.
func maxDigitsFor(node *dialplan.Node) int {
	longest := 1
	for exit := range node.Exits {
		if isDigits(exit) && len(exit) > longest {
			longest = len(exit)
		}
	}
	return longest
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !strings.ContainsRune("0123456789*#", r) {
			return false
		}
	}
	return true
}

func (e *Engine) runTTS(ctx context.Context, sess agent.CallSession, entry *dialplan.TTSEntry) outcome {
	if err := sess.PlayTTS(ctx, entry.Text, entry.Voice); err != nil {
		return exitWith("done", "playback failed: "+err.Error())
	}
	return exitWith("done", "spoke")
}

func (e *Engine) runPlayAudio(ctx context.Context, sess agent.CallSession, entry *dialplan.PlayAudioEntry) outcome {
	if err := sess.PlayAudio(ctx, entry.File); err != nil {
		return exitWith("done", "playback failed: "+err.Error())
	}
	return exitWith("done", "played "+entry.File)
}

// runDialUser dials an extension or a ring group.
//
// Before any media node the call is unanswered, so the dial FORWARDS: the caller
// hears the destination's own ringback and can still receive its real final
// status. After a media node the call is answered and the dial bridges instead.
func (e *Engine) runDialUser(ctx context.Context, sess agent.CallSession, cc agent.CallContext,
	entry *dialplan.DialUserEntry, policy *agent.Policy) outcome {

	nodeCtx, cancel := nodeContext(ctx, entry.TimeoutMs)
	defer cancel()

	timeout := time.Duration(entry.TimeoutMs) * time.Millisecond

	if name, isGroup := dialplan.IsGroupTarget(entry.Target); isGroup {
		return e.dialGroup(nodeCtx, sess, cc, name, timeout, policy)
	}

	target, ok := e.authorize(cc, entry.Target, policy)
	if !ok {
		// dial_user has no denied exit: an internal destination the tenant may
		// not reach is a configuration error the validator should have caught,
		// so at runtime it presents as unavailable.
		return exitWith("unavailable", "denied by policy")
	}

	return e.dialOne(nodeCtx, sess, target, timeout)
}

// dialOne performs a single dial and maps the outcome onto an exit.
func (e *Engine) dialOne(ctx context.Context, sess agent.CallSession, target string, timeout time.Duration) outcome {
	if sess.HasAnswered() {
		// Post-answer: bridge into media we already own. Dial reports a plain
		// error, so classify it — otherwise every failure looks alike and a
		// flow after a menu could not tell busy from nobody-home.
		if err := sess.Dial(ctx, target, timeout); err != nil {
			out := agent.ClassifyDialError(target, err)
			return exitWith(out.ExitName(), describeOutcome(out))
		}
		return terminalWith("answered", "bridged to "+target)
	}

	// Pre-answer: relay nothing, so the flow decides what the caller hears.
	res, err := sess.ForwardOutcome(ctx, target, timeout)
	if err != nil {
		return exitWith("failed", err.Error())
	}
	if res.Answered() {
		return terminalWith("answered", "connected to "+target)
	}
	return exitWith(res.ExitName(), describeOutcome(res))
}

// dialGroup rings a ring group, authorizing each member individually.
func (e *Engine) dialGroup(ctx context.Context, sess agent.CallSession, cc agent.CallContext,
	name string, timeout time.Duration, policy *agent.Policy) outcome {

	table, ok := e.tenantTable(cc.Tenant)
	if !ok {
		return exitWith("unavailable", "tenant has no routing table")
	}
	group, ok := table.Group(name)
	if !ok {
		return exitWith("unavailable", "ring group "+name+" is not defined")
	}

	// A member is adjudicated on its own merits rather than inheriting the
	// group's verdict.
	var members []string
	for _, m := range group.Members {
		if target, ok := e.authorize(cc, m, policy); ok {
			members = append(members, target)
		} else {
			e.log.Warn("ring group member denied by policy",
				"tenant", cc.Tenant, "group", name, "member", m)
		}
	}
	if len(members) == 0 {
		return exitWith("unavailable", "no authorized members in "+name)
	}

	memberTimeout := timeout
	if group.MemberTimeoutMs > 0 {
		memberTimeout = time.Duration(group.MemberTimeoutMs) * time.Millisecond
	}

	rounds := roundsFor(members)
	if sess.HasAnswered() {
		// Post-answer there is no forward path, so members are tried in order
		// rather than rung together. The last failure decides the exit.
		last := agent.DialOutcome{Result: agent.DialNoAnswer, Target: name}
		for _, round := range rounds {
			for _, member := range round {
				err := sess.Dial(ctx, member, memberTimeout)
				if err == nil {
					return terminalWith("answered", "bridged to "+member)
				}
				last = agent.ClassifyDialError(member, err)
			}
		}
		return exitWith(last.ExitName(), describeOutcome(last))
	}

	res, err := sess.ForwardGroupOutcome(ctx, rounds, memberTimeout)
	if err != nil {
		return exitWith("failed", err.Error())
	}
	if res.Answered() {
		return terminalWith("answered", "connected via "+name)
	}
	return exitWith(res.ExitName(), describeGroup(name, res))
}

// roundsFor turns a member list into ring rounds — one member per round, so
// they are tried in order.
//
// The group's strategy is deliberately not read here: the rotation for
// round-robin is applied to the member list before this point, so both
// strategies produce the same shape and differ only in where the list starts.
func roundsFor(members []string) [][]string {
	rounds := make([][]string, 0, len(members))
	for _, m := range members {
		rounds = append(rounds, []string{m})
	}
	return rounds
}

// runDialExternal dials a symbolic external destination.
func (e *Engine) runDialExternal(ctx context.Context, sess agent.CallSession,
	entry *dialplan.DialExternalEntry, policy *agent.Policy) outcome {

	nodeCtx, cancel := nodeContext(ctx, entry.TimeoutMs)
	defer cancel()

	if policy == nil {
		return exitWith("denied", "no policy configured")
	}

	// AuthorizeDial performs the symbolic narrowing: a name the tenant defined,
	// never a number the flow file supplied.
	target, decision := policy.AuthorizeDial(entry.Target)
	if !decision.Allowed {
		return exitWith("denied", decision.Reason)
	}

	res := e.dialOne(nodeCtx, sess, target, time.Duration(entry.TimeoutMs)*time.Millisecond)
	// dial_external names its failure exit "failed" where dial_user says
	// "unavailable"; map the one outcome that differs.
	if res.exit == "unavailable" {
		res.exit = "failed"
	}
	if res.exit == "rejected" {
		res.exit = "failed"
	}
	return res
}

// runTransfer performs a blind transfer.
func (e *Engine) runTransfer(ctx context.Context, sess agent.CallSession, cc agent.CallContext,
	entry *dialplan.TransferEntry, policy *agent.Policy) outcome {

	target, ok := e.authorize(cc, entry.Target, policy)
	if !ok {
		return exitWith("failed", "denied by policy")
	}

	res := e.dialOne(ctx, sess, target, 0)
	if res.terminal {
		return terminalWith("accepted", res.detail)
	}
	return exitWith("failed", res.detail)
}

// runHangup ends the call with a stated cause.
func (e *Engine) runHangup(sess agent.CallSession, entry *dialplan.HangupEntry) outcome {
	cause := entry.Cause
	if cause == "" {
		cause = "normal_clearing"
	}
	if err := sess.Hangup(cause); err != nil {
		e.log.Debug("hangup returned an error", "cause", cause, "error", err)
	}
	return outcome{exit: "hangup", terminal: true, detail: cause}
}

// authorize resolves and adjudicates an internal destination.
func (e *Engine) authorize(cc agent.CallContext, target string, policy *agent.Policy) (string, bool) {
	if policy == nil {
		return "", false
	}
	// A symbolic name resolves through the tenant's table exactly as it would
	// for any other dial path.
	if table, ok := e.tenantTable(cc.Tenant); ok {
		if resolved, found := table.SymbolicTargets[target]; found && resolved != "" {
			target = resolved
		}
	}
	if d := policy.AuthorizeTarget("flow_dial", target); !d.Allowed {
		return "", false
	}
	return target, true
}

// promptFrom converts a configured prompt into the session's form.
func promptFrom(p dialplan.PromptSpec) agent.Prompt {
	return agent.Prompt{
		Text:  p.Text,
		Voice: p.Voice,
		File:  p.File,
		Files: p.Files,
	}
}

func describeOutcome(o agent.DialOutcome) string {
	if o.SIPCode > 0 {
		return fmt.Sprintf("%s (%d %s)", o.ExitName(), o.SIPCode, o.SIPReason)
	}
	return o.ExitName()
}

func describeGroup(name string, o agent.GroupOutcome) string {
	return fmt.Sprintf("group %s: %s (%d members tried)", name, o.ExitName(), len(o.Members))
}
