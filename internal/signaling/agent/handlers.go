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
			// Playing a prompt means the supervisor handles this leg's media, so
			// answering is implied by the decision to play (design #7).
			if err := sess.Answer(ctx); err != nil {
				return "", fmt.Errorf("answer before play_audio: %w", err)
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
		// A completed dial is Terminal: the handler blocks for the life of the
		// bridge, so it returns only once the call is over. A FAILED dial is
		// downgraded to Continue by the executor so the model can recover.
		Disposition: DispositionTerminal,
		External:    externalEnabled,
		// Handler is unused: dial is dispatched through CallExecutor.executeDial,
		// which authorizes via Policy before calling dialHandler with the resolved
		// target. Kept nil-safe by never being invoked for dial.
		Handler: nil,
	}
}

// dialHandler performs the actual dial against the resolved (already-authorized)
// target. It is the realization of the answer model (design #7): the SIP path it
// takes depends entirely on whether the supervisor has answered.
//
//	not answered → Forward: relay 180 upstream, dial the target, and relay its
//	               200 (or its failure code). The caller hears real ringback and
//	               a direct extension call is a pure forward — no AI in the path.
//	answered     → Dial: the supervisor already owns the media, so the outbound
//	               leg is bridged into the existing RTP session (the classic
//	               adopt-and-bridge B2BUA flow).
func dialHandler(ctx context.Context, resolvedTarget string, sess CallSession) (string, error) {
	if !sess.HasAnswered() {
		if err := sess.Forward(ctx, resolvedTarget, defaultDialTimeout); err != nil {
			return "", err
		}
		return fmt.Sprintf("forwarded the call to %s", resolvedTarget), nil
	}

	if err := sess.Dial(ctx, resolvedTarget, defaultDialTimeout); err != nil {
		return "", err
	}
	return fmt.Sprintf("dialed %s", resolvedTarget), nil
}
