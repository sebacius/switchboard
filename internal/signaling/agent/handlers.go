package agent

import (
	"context"
	"fmt"
	"strings"
)

// This file holds the call-setup tool inventory (spec: agent-tools) and their
// handlers. The handlers are thin adapters onto CallSession primitives; argument
// validation that should drive model self-correction lives in the executor (for
// dial) or in the handler closure (for hangup/play_audio), and is returned as an
// actionable result string, never as an aborting Go error.

// hangupTool ends the call. Its disposition is Terminal so the runner funnels
// into teardown exactly once.
func hangupTool() Tool {
	return Tool{
		Name:        "hangup",
		Description: "End the call. Use after saying goodbye or when the caller's request is complete or cannot be served.",
		Params: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"reason": map[string]any{
					"type":        "string",
					"description": "Optional short reason for the hangup, for logs.",
				},
			},
		},
		Disposition: DispositionTerminal,
		Handler: func(_ context.Context, args map[string]any, sess CallSession) (string, error) {
			reason, _ := stringArg(args, "reason")
			if strings.TrimSpace(reason) == "" {
				reason = "agent hangup"
			}
			if err := sess.Hangup(reason); err != nil {
				return "", fmt.Errorf("hangup: %w", err)
			}
			return "call ended", nil
		},
	}
}

// playAudioTool plays a prompt file and continues the conversation.
func playAudioTool() Tool {
	return Tool{
		Name:        "play_audio",
		Description: "Play a pre-recorded audio prompt file to the caller, then continue.",
		Params: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"file": map[string]any{
					"type":        "string",
					"description": "The audio file to play (e.g. 'welcome.wav').",
				},
			},
			"required": []any{"file"},
		},
		Disposition: DispositionContinue,
		Handler: func(ctx context.Context, args map[string]any, sess CallSession) (string, error) {
			file, ok := stringArg(args, "file")
			if !ok || strings.TrimSpace(file) == "" {
				// Actionable, model-recoverable: returned as a Go error so the
				// executor wraps it into a Continue result the model can correct.
				return "", fmt.Errorf("play_audio requires a 'file'; name a prompt to play")
			}
			if err := sess.PlayAudio(ctx, file); err != nil {
				return "", err
			}
			return fmt.Sprintf("played %s", file), nil
		},
	}
}

// dialTool advertises the dial capability. The bool reflects whether the tenant
// enabled external reach, which only shapes the advertised description — every
// dial is still authorized by the Policy at execution time, and the model can
// only emit SYMBOLIC targets (extension names / named forwards), never a raw
// external number (capability narrowing, design #10).
func dialTool(externalEnabled bool) Tool {
	desc := "Route the call to an internal extension or a named forward. " +
		"Provide a symbolic 'target' (an extension name or configured forward name); " +
		"raw external numbers are not accepted here."
	if externalEnabled {
		desc += " Some named forwards may reach external destinations, subject to authorization."
	}
	return Tool{
		Name:        "dial",
		Description: desc,
		Params: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"target": map[string]any{
					"type":        "string",
					"description": "Symbolic destination: an extension name or a configured named forward.",
				},
			},
			"required": []any{"target"},
		},
		Disposition: DispositionContinue,
		External:    externalEnabled,
		// Handler is unused: dial is dispatched through CallExecutor.executeDial,
		// which authorizes via Policy before calling dialHandler with the resolved
		// target. Kept nil-safe by never being invoked for dial.
		Handler: nil,
	}
}

// dialHandler performs the actual dial against the resolved (already-authorized)
// target. It uses the existing adopt-and-bridge CallSession.Dial path.
//
// TODO(group7): the spec's "forward the INVITE without answering" path for
// first-turn silent internal routing (relay 180/200, never send our own 200)
// requires the answer-deferral + B2BUA INVITE forwarding that group 7 owns. For
// now dial always uses the existing adopt-and-bridge session.Dial, which assumes
// the supervisor has (or will) own media. When group 7 lands answer-deferral,
// branch here on whether we have answered: forward pre-answer, bridge post-answer.
func dialHandler(ctx context.Context, resolvedTarget string, sess CallSession) (string, error) {
	if err := sess.Dial(ctx, resolvedTarget, defaultDialTimeout); err != nil {
		return "", err
	}
	return fmt.Sprintf("dialed %s", resolvedTarget), nil
}

// TODO(group7): park/unpark tools are deferred. park returns DispositionParked
// (the loop holds the call) and unpark performs the BridgeMedia port. Both need
// parking.Service wired in and are entangled with the answer model (a parked
// call must already own media), so they are not implemented or registered here.
