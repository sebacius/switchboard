package session

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"time"

	"github.com/pion/rtp"
	"github.com/sebas/switchboard/internal/rtpmanager/dtmf"
	"github.com/sebas/switchboard/internal/rtpmanager/media"
)

// CollectResult is why a digit collection ended and what it gathered.
type CollectResult struct {
	Digits            string
	Reason            CollectReason
	PromptInterrupted bool
}

// CollectReason mirrors the wire enum. It lives here so the session package does
// not depend on generated protobuf types.
type CollectReason int

const (
	CollectReasonUnspecified CollectReason = iota
	CollectReasonTerminator
	CollectReasonMaxDigits
	CollectReasonInterDigitTimeout
	CollectReasonFirstDigitTimeout
	CollectReasonNoInput
	CollectReasonCanceled
	CollectReasonNoDTMFTransport
	CollectReasonError
)

// CollectRequest parameterises one collection.
type CollectRequest struct {
	// Prompt is played while collecting. Nil collects in silence.
	Prompt *media.PlayRequest
	// PromptAudio is pre-synthesized audio for the prompt, when the prompt is
	// TTS rather than a file.
	PromptAudio []byte

	Interruptible       bool
	MaxDigits           int
	Terminators         string
	FirstDigitTimeoutMs int
	InterDigitTimeoutMs int
	OverallTimeoutMs    int
	FlushBuffer         bool
}

// Collection defaults. They are the values a caller experiences as "normal", and
// naming them here keeps a node that omits them behaving sensibly.
const (
	defaultFirstDigitTimeoutMs = 5000
	defaultInterDigitTimeoutMs = 3000
	defaultOverallTimeoutMs    = 30000
)

// CollectDigits plays a prompt and collects digits in ONE operation, owning the
// session's RTP socket throughout.
//
// This is the whole reason the operation is not two calls. The socket can have
// only one reader, so a "play" followed by a "collect" must hand it over in
// between — and a digit pressed during that handover is simply gone. Owning it
// for the duration is also what lets the first digit cut the prompt short, since
// the same loop is already reading while audio is being sent.
func (m *Manager) CollectDigits(ctx context.Context, sessionID string, req CollectRequest) (CollectResult, error) {
	m.mu.RLock()
	sess, ok := m.sessions[sessionID]
	m.mu.RUnlock()
	if !ok {
		return CollectResult{Reason: CollectReasonError}, fmt.Errorf("session not found: %s", sessionID)
	}

	// A leg that negotiated no telephone-event can never deliver a digit.
	// Waiting for one would hang the caller in a menu that cannot hear them, so
	// say so immediately and let the flow degrade by a declared exit.
	if !sess.Answer.HasDTMF() {
		slog.Warn("[SessionMgr] Digit collection on a leg with no DTMF transport",
			"session_id", sessionID)
		return CollectResult{Reason: CollectReasonNoDTMFTransport}, nil
	}

	if req.FlushBuffer {
		if dropped := sess.Digits.Flush(); dropped != "" {
			slog.Debug("[SessionMgr] Flushed stale digits before collecting",
				"session_id", sessionID, "dropped", dropped)
		}
	}

	maxDigits := req.MaxDigits
	if maxDigits <= 0 {
		maxDigits = 1
	}
	overall := durationOr(req.OverallTimeoutMs, defaultOverallTimeoutMs)

	collectCtx, cancel := context.WithTimeout(ctx, overall)
	defer cancel()

	// Take the socket for the whole operation.
	conn, err := net.ListenUDP("udp", &net.UDPAddr{Port: sess.LocalPort, IP: net.IPv4zero})
	if err != nil {
		return CollectResult{Reason: CollectReasonError}, fmt.Errorf("bind RTP port %d: %w", sess.LocalPort, err)
	}
	defer func() { _ = conn.Close() }()

	// Feed digits into the session buffer from this loop, so type-ahead pressed
	// during the prompt survives into the next node.
	detector := dtmf.NewDetector(sess.Answer.TelephoneEventPT)
	readErr := make(chan error, 1)
	go func() {
		readErr <- readDigits(collectCtx, conn, detector, sess.Digits)
	}()

	// Start the prompt, if any. Playback writes to the same port from its own
	// socket; only the READ side is exclusive.
	promptDone := make(chan struct{})
	var promptCancel context.CancelFunc
	if req.Prompt != nil || len(req.PromptAudio) > 0 {
		var promptCtx context.Context
		promptCtx, promptCancel = context.WithCancel(collectCtx)
		defer promptCancel()
		go func() {
			defer close(promptDone)
			m.playPrompt(promptCtx, sess, req)
		}()
	} else {
		close(promptDone)
	}

	result := m.gatherDigits(collectCtx, sess, req, maxDigits, promptDone, promptCancel)

	// Surface a read failure only when nothing was collected; a completed
	// collection is not made wrong by the socket closing afterwards.
	select {
	case err := <-readErr:
		if err != nil && result.Digits == "" && result.Reason != CollectReasonNoInput {
			slog.Debug("[SessionMgr] Digit read ended", "session_id", sessionID, "error", err)
		}
	default:
	}

	slog.Info("[SessionMgr] Digit collection complete",
		"session_id", sessionID, "digits", result.Digits,
		"reason", result.Reason, "interrupted", result.PromptInterrupted)
	return result, nil
}

// gatherDigits waits for digits under the collection's timing rules.
func (m *Manager) gatherDigits(ctx context.Context, sess *Session, req CollectRequest,
	maxDigits int, promptDone <-chan struct{}, promptCancel context.CancelFunc) CollectResult {

	var result CollectResult
	var collected []rune

	firstTimeout := durationOr(req.FirstDigitTimeoutMs, defaultFirstDigitTimeoutMs)
	interTimeout := durationOr(req.InterDigitTimeoutMs, defaultInterDigitTimeoutMs)

	promptPlaying := true
	for {
		// The first-digit timer starts when the PROMPT ENDS, not when the
		// collection does. Starting it earlier would time out a caller who was
		// still being told what their options are.
		var timer <-chan time.Time
		if !promptPlaying || len(collected) > 0 {
			d := firstTimeout
			if len(collected) > 0 {
				d = interTimeout
			}
			t := time.NewTimer(d)
			defer t.Stop()
			timer = t.C
		}

		ch := sess.Digits.Wait()

		select {
		case <-ctx.Done():
			sess.Digits.Cancel(ch)
			result.Digits = string(collected)
			if result.Reason == CollectReasonUnspecified {
				result.Reason = CollectReasonCanceled
			}
			return result

		case <-promptDone:
			// The prompt finished on its own; start the first-digit timer.
			promptPlaying = false
			sess.Digits.Cancel(ch)
			promptDone = nil
			continue

		case <-timer:
			sess.Digits.Cancel(ch)
			result.Digits = string(collected)
			if len(collected) == 0 {
				result.Reason = CollectReasonFirstDigitTimeout
			} else {
				result.Reason = CollectReasonInterDigitTimeout
			}
			return result

		case digit := <-ch:
			// Barge-in: the first digit cuts the prompt short.
			if promptPlaying && req.Interruptible {
				result.PromptInterrupted = true
				if promptCancel != nil {
					promptCancel()
				}
				_ = m.mediaService.Stop(sess.CallID)
				promptPlaying = false
			}

			if dtmf.Contains(req.Terminators, digit) {
				result.Digits = string(collected)
				result.Reason = CollectReasonTerminator
				return result
			}

			collected = append(collected, digit)
			if len(collected) >= maxDigits {
				result.Digits = string(collected)
				result.Reason = CollectReasonMaxDigits
				return result
			}
		}
	}
}

// playPrompt renders the prompt through the media service.
func (m *Manager) playPrompt(ctx context.Context, sess *Session, req CollectRequest) {
	play := media.PlayRequest{
		CallID:    sess.CallID,
		LocalAddr: sess.LocalAddr,
		LocalPort: sess.LocalPort,
		Endpoint:  sess.RemoteAddr,
		Port:      sess.RemotePort,
		Codec:     sess.Codec,
	}
	if len(req.PromptAudio) > 0 {
		play.RawAudio = req.PromptAudio
	} else if req.Prompt != nil {
		play.Files = req.Prompt.Files
	}

	if err := m.mediaService.Play(ctx, play); err != nil && ctx.Err() == nil {
		slog.Warn("[SessionMgr] Prompt playback failed", "session_id", sess.ID, "error", err)
	}
}

// readDigits reads RTP until the context ends, pushing decoded digits into the
// session buffer.
func readDigits(ctx context.Context, conn *net.UDPConn, detector *dtmf.Detector, buf *dtmf.Buffer) error {
	packet := &rtp.Packet{}
	raw := make([]byte, 1500)

	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		// A short deadline keeps the loop responsive to cancellation without
		// spinning.
		_ = conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))

		n, _, err := conn.ReadFromUDP(raw)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue
			}
			return err
		}
		if err := packet.Unmarshal(raw[:n]); err != nil {
			continue
		}
		if digit, ok := detector.Handle(packet); ok {
			buf.Push(digit)
		}
	}
}

func durationOr(ms, fallback int) time.Duration {
	if ms <= 0 {
		ms = fallback
	}
	return time.Duration(ms) * time.Millisecond
}
