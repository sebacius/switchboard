package agent

import (
	"context"
	"fmt"

	"github.com/sebas/switchboard/internal/signaling/mediaclient"
)

// Digit collection is the caller's only input to a flow. The types here mirror
// the media client's so the flow engine never imports the transport, and so the
// reasons a collection ended are named the same everywhere they are logged.

// Prompt is what the caller hears during a collection.
type Prompt struct {
	Text  string
	Voice string
	File  string
	Files []string
}

// IsZero reports whether the prompt says nothing.
func (p Prompt) IsZero() bool {
	return p.Text == "" && p.File == "" && len(p.Files) == 0
}

// CollectRequest parameterises one digit collection.
type CollectRequest struct {
	// Prompt is played while collecting; a zero Prompt collects in silence.
	Prompt Prompt
	// Interruptible lets the first digit cut the prompt short.
	Interruptible bool
	MaxDigits     int
	Terminators   string
	// FirstDigitTimeoutMs is measured from the END of the prompt.
	FirstDigitTimeoutMs int
	InterDigitTimeoutMs int
	OverallTimeoutMs    int
	// FlushBuffer discards digits pressed before this collection started, for a
	// node re-prompting after invalid input.
	FlushBuffer bool
}

// CollectReason says why a collection ended.
type CollectReason = mediaclient.CollectReason

// Re-exported so flow nodes need not import the transport package.
const (
	CollectTerminator        = mediaclient.CollectReasonTerminator
	CollectMaxDigits         = mediaclient.CollectReasonMaxDigits
	CollectInterDigitTimeout = mediaclient.CollectReasonInterDigitTimeout
	CollectFirstDigitTimeout = mediaclient.CollectReasonFirstDigitTimeout
	CollectNoInput           = mediaclient.CollectReasonNoInput
	CollectCanceled          = mediaclient.CollectReasonCanceled
	CollectNoDTMFTransport   = mediaclient.CollectReasonNoDTMFTransport
	CollectError             = mediaclient.CollectReasonError
)

// CollectResult is what a collection gathered.
type CollectResult struct {
	Digits            string
	Reason            CollectReason
	PromptInterrupted bool
}

// TimedOut reports whether the collection ended because nobody pressed anything
// in time — as opposed to pressing something this node did not expect, which is
// invalid input and routes elsewhere.
func (r CollectResult) TimedOut() bool {
	return r.Reason == CollectFirstDigitTimeout ||
		r.Reason == CollectInterDigitTimeout ||
		r.Reason == CollectNoInput
}

// CollectDigits plays a prompt and collects digits.
func (s *sessionImpl) CollectDigits(ctx context.Context, req CollectRequest) (CollectResult, error) {
	s.mu.Lock()
	sessionID := s.sessionID
	s.mu.Unlock()

	if sessionID == "" {
		return CollectResult{}, fmt.Errorf("no RTP session established")
	}

	// Collecting requires media, and media requires an answered leg.
	if err := s.Answer(ctx); err != nil {
		return CollectResult{}, fmt.Errorf("answer before collecting digits: %w", err)
	}

	clientReq := mediaclient.CollectRequest{
		SessionID:           sessionID,
		Interruptible:       req.Interruptible,
		MaxDigits:           req.MaxDigits,
		Terminators:         req.Terminators,
		FirstDigitTimeoutMs: req.FirstDigitTimeoutMs,
		InterDigitTimeoutMs: req.InterDigitTimeoutMs,
		OverallTimeoutMs:    req.OverallTimeoutMs,
		FlushBuffer:         req.FlushBuffer,
	}
	if !req.Prompt.IsZero() {
		clientReq.Prompt = &mediaclient.CollectPrompt{
			Text:  req.Prompt.Text,
			Voice: req.Prompt.Voice,
			File:  req.Prompt.File,
			Files: req.Prompt.Files,
		}
	}

	result, err := s.transport.CollectDigits(ctx, clientReq)
	if err != nil {
		return CollectResult{}, fmt.Errorf("collect digits: %w", err)
	}

	s.logger.Debug("[Session] Digits collected",
		"call_id", s.callID, "digits", result.Digits,
		"reason", result.Reason, "interrupted", result.PromptInterrupted)

	return CollectResult{
		Digits:            result.Digits,
		Reason:            result.Reason,
		PromptInterrupted: result.PromptInterrupted,
	}, nil
}
