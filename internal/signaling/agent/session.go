package agent

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/emiago/sipgo/sip"
	"github.com/sebas/switchboard/internal/signaling/b2bua"
	"github.com/sebas/switchboard/internal/signaling/dialog"
	"github.com/sebas/switchboard/internal/signaling/location"
	"github.com/sebas/switchboard/internal/signaling/mediaclient"
)

// CallSession provides actions access to call state and operations.
// This is the bridge between dialplan actions and the existing Dialog/Transport layers.
type CallSession interface {
	// Identity
	CallID() string
	Destination() string // Dialed number (To URI user part)
	CallerID() string    // Caller number (From URI user part)
	Domain() string      // SIP domain (To URI host part)

	// Context returns the call's context. Canceled on BYE or timeout.
	Context() context.Context

	// Media operations
	PlayAudio(ctx context.Context, file string) error
	PlayTTS(ctx context.Context, text, voice string) error
	StopAudio() error
	Listen(ctx context.Context, maxDurationMs, silenceTimeoutMs int) (text string, err error)

	// Answer model (design #7). The INVITE handler never answers; the supervisor
	// decides. Answering means "the AI handles this leg's media itself".

	// Answer sends the 200 OK with the supervisor's SDP, taking ownership of the
	// media. It is idempotent: a second call is a no-op returning nil. Every
	// media operation (PlayTTS/PlayAudio/Listen) requires an answered leg.
	Answer(ctx context.Context) error

	// MarkRinging records that a 180 Ringing has already been sent for this leg,
	// so the dial path does not send a second one.
	MarkRinging()

	// HasAnswered reports whether this leg has been answered by the supervisor.
	// It is the branch the dial tool takes between forwarding and bridging, and
	// the branch teardown takes between a 487 and a BYE.
	HasAnswered() bool

	// B2BUA operations (for the dial tool)

	// Forward performs the PRE-ANSWER routing path: it relays 180 Ringing to the
	// caller, dials the target, and only on the target answering does it send the
	// 200 OK upstream and bridge. The supervisor never answers on its own behalf,
	// so the caller hears real ringing and a failed target relays its own status
	// code. Returns an error if the target could not be reached.
	Forward(ctx context.Context, target string, timeout time.Duration) error

	// Dial initiates an outbound call to the target and bridges media. It is the
	// POST-ANSWER path: the supervisor already owns the media, so the B-leg is
	// bridged to the existing RTP session rather than relayed.
	// target can be "user/extension" or "sip:user@host:port"
	// Returns error if dial fails (timeout, rejected, user not found)
	Dial(ctx context.Context, target string, timeout time.Duration) error

	// Termination
	Hangup(reason string) error

	// State queries
	IsTerminated() bool

	// Parking support
	// GetDialog returns the underlying SIP dialog for parking operations.
	GetDialog() *dialog.Dialog

	// GetSessionID returns the RTP session ID.
	GetSessionID() string

	// GetTransport returns the media transport for bridging operations.
	GetTransport() mediaclient.Transport

	// TerminateDialog terminates another dialog by its Call-ID.
	// Used for cross-leg termination in bridging scenarios.
	TerminateDialog(callID string, reason string) error
}

// sessionImpl implements CallSession, bridging dialplan with existing components.
type sessionImpl struct {
	mu sync.Mutex

	// Identity
	callID      string
	destination string
	callerID    string
	callerName  string
	domain      string

	// Core components
	ctx         context.Context
	cancel      context.CancelFunc
	dialog      *dialog.Dialog
	transport   mediaclient.Transport
	dialogMgr   *dialog.Manager
	locStore    location.LocationStore
	callService b2bua.CallService
	logger      *slog.Logger

	// Session state
	sessionID  string
	terminated bool

	// sdpBody is the answer SDP produced when the media session was created. The
	// INVITE handler holds it back instead of sending it in a 200 OK; Answer()
	// sends it when (and only when) the supervisor decides to own the media.
	sdpBody []byte

	// answered records whether the 200 OK has gone out. It drives forward-vs-
	// bridge in the dial tool and 487-vs-BYE in teardown.
	answered bool

	// bLeg is the outbound leg created by Forward/Dial, retained so teardown can
	// cancel an orphaned leg when the caller abandons mid-forward.
	bLeg b2bua.Leg

	// ringingSent records that a 180 has gone out, so the early ring from the
	// INVITE handler and Forward's own ring do not both fire.
	ringingSent bool
}

// SessionConfig contains dependencies for creating a CallSession.
type SessionConfig struct {
	Dialog      *dialog.Dialog
	Transport   mediaclient.Transport
	DialogMgr   *dialog.Manager
	LocStore    location.LocationStore
	CallService b2bua.CallService
	Logger      *slog.Logger
	Destination string
	CallerID    string // From header user part (phone number/extension)
	CallerName  string // From header display name
	Domain      string // To header host part (SIP domain)
	SDPBody     []byte // answer SDP, held back until Answer() sends the 200 OK
}

// NewSession creates a CallSession from an established dialog.
func NewSession(cfg SessionConfig) CallSession {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	// Derive context from dialog's context
	ctx, cancel := context.WithCancel(cfg.Dialog.Context())

	return &sessionImpl{
		callID:      cfg.Dialog.CallID,
		destination: cfg.Destination,
		callerID:    cfg.CallerID,
		callerName:  cfg.CallerName,
		domain:      cfg.Domain,
		ctx:         ctx,
		cancel:      cancel,
		dialog:      cfg.Dialog,
		transport:   cfg.Transport,
		dialogMgr:   cfg.DialogMgr,
		locStore:    cfg.LocStore,
		callService: cfg.CallService,
		logger:      cfg.Logger,
		sessionID:   cfg.Dialog.GetSessionID(),
		sdpBody:     cfg.SDPBody,
	}
}

func (s *sessionImpl) CallID() string           { return s.callID }
func (s *sessionImpl) Destination() string      { return s.destination }
func (s *sessionImpl) CallerID() string         { return s.callerID }
func (s *sessionImpl) Domain() string           { return s.domain }
func (s *sessionImpl) Context() context.Context { return s.ctx }

func (s *sessionImpl) IsTerminated() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.terminated || s.dialog.IsTerminated()
}

func (s *sessionImpl) GetDialog() *dialog.Dialog           { return s.dialog }
func (s *sessionImpl) GetSessionID() string                { return s.sessionID }
func (s *sessionImpl) GetTransport() mediaclient.Transport { return s.transport }

// PlayAudio plays an audio file and blocks until completion.
func (s *sessionImpl) PlayAudio(ctx context.Context, file string) error {
	s.mu.Lock()
	sessionID := s.sessionID
	s.mu.Unlock()

	if sessionID == "" {
		return fmt.Errorf("no RTP session established")
	}

	s.logger.Debug("[Session] Playing audio",
		"call_id", s.callID,
		"file", file,
	)

	// Create play request
	playReq := mediaclient.PlayRequest{
		SessionID: sessionID,
		AudioFile: file,
		Loop:      false,
	}

	// Start playback
	statusCh, err := s.transport.PlayAudio(ctx, playReq)
	if err != nil {
		return fmt.Errorf("start playback: %w", err)
	}

	// Wait for completion or cancellation
	for status := range statusCh {
		switch status.State {
		case mediaclient.PlayStateCompleted:
			s.logger.Debug("[Session] Playback completed",
				"call_id", s.callID,
				"file", file,
			)
			return nil
		case mediaclient.PlayStateError:
			s.logger.Warn("[Session] Playback error",
				"call_id", s.callID,
				"file", file,
				"error", status.Error,
			)
			return status.Error
		case mediaclient.PlayStateStopped:
			s.logger.Debug("[Session] Playback stopped",
				"call_id", s.callID,
				"file", file,
			)
			return nil
		}
	}

	return nil
}

// PlayTTS synthesizes text to speech and plays it.
func (s *sessionImpl) PlayTTS(ctx context.Context, text, voice string) error {
	s.mu.Lock()
	sessionID := s.sessionID
	s.mu.Unlock()

	if sessionID == "" {
		return fmt.Errorf("no RTP session established")
	}

	s.logger.Debug("[Session] Playing TTS",
		"call_id", s.callID,
		"text", text,
		"voice", voice,
	)

	// Create TTS request
	ttsReq := mediaclient.TTSRequest{
		SessionID: sessionID,
		Text:      text,
		Voice:     voice,
	}

	// Start TTS playback
	statusCh, err := s.transport.PlayTTS(ctx, ttsReq)
	if err != nil {
		return fmt.Errorf("start TTS playback: %w", err)
	}

	// Wait for completion or cancellation
	for status := range statusCh {
		switch status.State {
		case mediaclient.PlayStateCompleted:
			s.logger.Debug("[Session] TTS playback completed",
				"call_id", s.callID,
			)
			return nil
		case mediaclient.PlayStateError:
			s.logger.Warn("[Session] TTS playback error",
				"call_id", s.callID,
				"error", status.Error,
			)
			return status.Error
		case mediaclient.PlayStateStopped:
			s.logger.Debug("[Session] TTS playback stopped",
				"call_id", s.callID,
			)
			return nil
		}
	}

	return nil
}

// StopAudio stops any ongoing audio playback.
func (s *sessionImpl) StopAudio() error {
	s.mu.Lock()
	sessionID := s.sessionID
	s.mu.Unlock()

	if sessionID == "" {
		return nil
	}

	return s.transport.StopAudio(s.ctx, sessionID)
}

// Listen captures audio from the caller and returns transcribed text.
func (s *sessionImpl) Listen(ctx context.Context, maxDurationMs, silenceTimeoutMs int) (string, error) {
	s.mu.Lock()
	sessionID := s.sessionID
	s.mu.Unlock()

	if sessionID == "" {
		return "", fmt.Errorf("no RTP session established")
	}

	s.logger.Debug("[Session] Listening for audio",
		"call_id", s.callID,
		"max_duration_ms", maxDurationMs,
		"silence_timeout_ms", silenceTimeoutMs,
	)

	listenReq := mediaclient.ListenRequest{
		SessionID:        sessionID,
		MaxDurationMs:    maxDurationMs,
		SilenceTimeoutMs: silenceTimeoutMs,
	}

	result, err := s.transport.Listen(ctx, listenReq)
	if err != nil {
		return "", fmt.Errorf("listen failed: %w", err)
	}

	s.logger.Debug("[Session] Listen complete",
		"call_id", s.callID,
		"text", result.Text,
		"duration_ms", result.DurationMs,
		"timeout", result.Timeout,
	)

	return result.Text, nil
}

// Dial initiates an outbound call and bridges on answer.
// Uses the B2BUA CallService for full dial and bridge functionality.
func (s *sessionImpl) Dial(ctx context.Context, target string, timeout time.Duration) error {
	s.logger.Info("[Session] Dial action (post-answer bridge)",
		"call_id", s.callID,
		"target", target,
		"timeout", timeout,
	)

	// Dial bridges into media we already own. Reaching it before answering means
	// the caller picked the wrong path; Forward is the pre-answer equivalent.
	if !s.HasAnswered() {
		return &DialError{
			Target: target,
			Cause:  fmt.Errorf("dial before answer: use Forward for the pre-answer path"),
		}
	}

	// Check if CallService is configured
	if s.callService == nil {
		// Fall back to basic resolution for diagnostics
		contactURI, err := s.resolveTarget(target)
		if err != nil {
			return &DialError{
				Target: target,
				Cause:  err,
			}
		}
		s.logger.Info("[Session] Resolved target (no CallService)",
			"call_id", s.callID,
			"target", target,
			"contact_uri", contactURI,
		)
		return &DialError{
			Target:  target,
			Cause:   fmt.Errorf("B2BUA CallService not configured"),
			SIPCode: 501,
		}
	}

	// Adopt the A-leg (inbound dialog) as a B2BUA leg
	// The teardown handler is called when the A-leg is hung up (e.g., when B hangs up and bridge terminates)
	// It sends BYE to the caller via the dialog manager
	aLeg, err := s.callService.AdoptInboundLeg(s.dialog, s.sessionID,
		b2bua.WithTeardownHandler(func(leg b2bua.Leg) {
			cause := leg.GetTerminationCause()
			dialogState := s.dialog.GetState()
			s.logger.Info("[Session] A-leg teardown handler invoked",
				"call_id", s.callID,
				"cause", cause.String(),
				"dialog_state", dialogState.String(),
				"dialog_terminated", s.dialog.IsTerminated(),
				"dialogMgr_nil", s.dialogMgr == nil,
			)
			// Don't send BYE if the remote party initiated (they sent BYE to us)
			if cause == b2bua.TerminationCauseRemoteBYE {
				s.logger.Debug("[Session] Skipping A-leg teardown BYE - remote initiated",
					"call_id", s.callID,
				)
				return
			}
			if s.dialogMgr != nil && !s.dialog.IsTerminated() {
				s.logger.Info("[Session] Sending BYE to A-leg via dialogMgr.Terminate",
					"call_id", s.callID,
					"dialog_state", dialogState.String(),
				)
				if err := s.dialogMgr.Terminate(s.callID, dialog.ReasonLocalBYE); err != nil {
					s.logger.Warn("[Session] A-leg teardown BYE failed",
						"call_id", s.callID,
						"error", err,
					)
				} else {
					s.logger.Info("[Session] A-leg BYE sent successfully",
						"call_id", s.callID,
					)
				}
			} else {
				s.logger.Debug("[Session] Skipping A-leg teardown BYE - dialogMgr nil or dialog terminated",
					"call_id", s.callID,
					"dialogMgr_nil", s.dialogMgr == nil,
					"dialog_terminated", s.dialog.IsTerminated(),
				)
			}
		}),
	)
	if err != nil {
		return &DialError{
			Target: target,
			Cause:  fmt.Errorf("adopt inbound leg: %w", err),
		}
	}

	s.logger.Info("[Session] A-leg adopted",
		"call_id", s.callID,
		"leg_id", aLeg.ID(),
	)

	// Use DialAndBridge for the complete B2BUA flow
	// This will: lookup target, create B-leg, wait for answer, bridge media, wait for termination
	// Pass CallerID from the inbound call to set the From header on the outbound INVITE
	callerName := s.callerName
	if callerName == "" {
		callerName = s.callerID // Fallback to callerID if no display name
	}
	bridgeInfo, err := s.callService.DialAndBridge(ctx, aLeg, target, timeout,
		b2bua.WithCallerID(s.callerID),
		b2bua.WithCallerName(callerName),
	)
	if err != nil {
		// Extract SIP code from DialError if available
		if dialErr, ok := err.(*b2bua.DialError); ok {
			return &DialError{
				Target:    target,
				SIPCode:   dialErr.SIPCode,
				SIPReason: dialErr.SIPReason,
				Cause:     dialErr.Cause,
			}
		}
		return &DialError{
			Target: target,
			Cause:  err,
		}
	}

	s.logger.Info("[Session] Bridge terminated",
		"call_id", s.callID,
		"bridge_id", bridgeInfo.ID,
		"duration", bridgeInfo.Duration(),
	)

	return nil
}

// resolveTarget resolves a dial target to a contact URI.
// Supports:
//   - "user/extension" -> lookup in location service
//   - "sip:user@host:port" -> use directly
func (s *sessionImpl) resolveTarget(target string) (string, error) {
	// Check for user/ prefix
	if strings.HasPrefix(target, "user/") {
		extension := strings.TrimPrefix(target, "user/")
		return s.lookupUser(extension)
	}

	// Check for sip: URI
	if strings.HasPrefix(target, "sip:") {
		return target, nil
	}

	// Assume it's a user extension
	return s.lookupUser(target)
}

// lookupUser looks up a user in the location service.
func (s *sessionImpl) lookupUser(extension string) (string, error) {
	if s.locStore == nil {
		return "", fmt.Errorf("location service not configured")
	}

	// Build AOR (Address of Record)
	// Format: sip:extension@domain
	// For now, just use the extension as the AOR key
	aor := fmt.Sprintf("sip:%s@", extension)

	// Try to find a binding that starts with this AOR
	bindings := s.locStore.List()
	for _, binding := range bindings {
		if strings.HasPrefix(binding.AOR, aor) || strings.Contains(binding.AOR, extension) {
			return binding.EffectiveContact(), nil
		}
	}

	// Also try direct lookup
	binding := s.locStore.LookupOne(extension)
	if binding != nil {
		return binding.EffectiveContact(), nil
	}

	return "", ErrUserNotFound
}

// Hangup terminates the call. It is the session half of the runner's idempotent
// teardown funnel (design #6) and branches on whether we ever answered:
//
//	pre-answer  → 487 Request Terminated on the still-open INVITE transaction,
//	              plus a CANCEL/BYE on any outbound leg left ringing
//	post-answer → BYE via the dialog manager, media torn down as usual
//
// The first call wins; subsequent calls are no-ops, so a caller BYE racing the
// hangup tool cannot double-free or provoke a "dialog not found".
func (s *sessionImpl) Hangup(reason string) error {
	s.mu.Lock()
	if s.terminated {
		s.mu.Unlock()
		return nil
	}
	s.terminated = true
	answered := s.answered
	s.mu.Unlock()

	s.logger.Info("[Session] Hangup",
		"call_id", s.callID,
		"reason", reason,
		"answered", answered,
	)

	// Cancel our context to stop any ongoing operations
	s.cancel()

	// An outbound leg may still be ringing (the caller abandoned mid-forward).
	// Cancel it first so we never leave a phone ringing for a call that is gone.
	s.hangupBLeg(reason)

	if s.dialogMgr == nil || s.dialog.IsTerminated() {
		return nil
	}

	if !answered {
		// Pre-answer abort: the INVITE transaction is still open, so the correct
		// response is 487 — not a BYE, which would reference a dialog the caller
		// never confirmed.
		if err := s.dialogMgr.RespondStatus(s.dialog, sip.StatusRequestTerminated, "Request Terminated"); err != nil {
			s.logger.Warn("[Session] Pre-answer 487 failed", "call_id", s.callID, "error", err)
		}
		return s.dialogMgr.Terminate(s.callID, dialog.ReasonCancel)
	}

	// Post-answer: normal BYE path.
	return s.dialogMgr.Terminate(s.callID, dialog.ReasonLocalBYE)
}

// TerminateDialog terminates another dialog by its Call-ID.
// Used for cross-leg termination in bridging scenarios (e.g., parking).
func (s *sessionImpl) TerminateDialog(callID string, reason string) error {
	s.logger.Info("[Session] TerminateDialog",
		"target_call_id", callID,
		"reason", reason,
	)

	if s.dialogMgr == nil {
		return fmt.Errorf("dialog manager not available")
	}

	return s.dialogMgr.Terminate(callID, dialog.ReasonLocalBYE)
}
