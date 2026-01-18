package dialplan

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

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

	// Context returns the call's context. Canceled on BYE or timeout.
	Context() context.Context

	// Media operations
	PlayAudio(ctx context.Context, file string) error
	StopAudio() error

	// B2BUA operations (for dial action)
	// Dial initiates an outbound call to the target.
	// target can be "user/extension" or "sip:user@host:port"
	// Returns error if dial fails (timeout, rejected, user not found)
	Dial(ctx context.Context, target string, timeout time.Duration) error

	// Termination
	Hangup(reason string) error

	// State queries
	IsTerminated() bool
}

// sessionImpl implements CallSession, bridging dialplan with existing components.
type sessionImpl struct {
	mu sync.Mutex

	// Identity
	callID      string
	destination string
	callerID    string
	callerName  string

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
		ctx:         ctx,
		cancel:      cancel,
		dialog:      cfg.Dialog,
		transport:   cfg.Transport,
		dialogMgr:   cfg.DialogMgr,
		locStore:    cfg.LocStore,
		callService: cfg.CallService,
		logger:      cfg.Logger,
		sessionID:   cfg.Dialog.GetSessionID(),
	}
}

func (s *sessionImpl) CallID() string           { return s.callID }
func (s *sessionImpl) Destination() string      { return s.destination }
func (s *sessionImpl) CallerID() string         { return s.callerID }
func (s *sessionImpl) Context() context.Context { return s.ctx }

func (s *sessionImpl) IsTerminated() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.terminated || s.dialog.IsTerminated()
}

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

// Dial initiates an outbound call and bridges on answer.
// Uses the B2BUA CallService for full dial and bridge functionality.
func (s *sessionImpl) Dial(ctx context.Context, target string, timeout time.Duration) error {
	s.logger.Info("[Session] Dial action",
		"call_id", s.callID,
		"target", target,
		"timeout", timeout,
	)

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
			if s.dialogMgr != nil && !s.dialog.IsTerminated() {
				if err := s.dialogMgr.Terminate(s.callID, dialog.ReasonLocalBYE); err != nil {
					s.logger.Warn("[Session] A-leg teardown BYE failed",
						"call_id", s.callID,
						"error", err,
					)
				}
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

// Hangup terminates the call.
func (s *sessionImpl) Hangup(reason string) error {
	s.mu.Lock()
	if s.terminated {
		s.mu.Unlock()
		return nil
	}
	s.terminated = true
	s.mu.Unlock()

	s.logger.Info("[Session] Hangup",
		"call_id", s.callID,
		"reason", reason,
	)

	// Cancel our context to stop any ongoing operations
	s.cancel()

	// Terminate the dialog
	if s.dialogMgr != nil && !s.dialog.IsTerminated() {
		return s.dialogMgr.Terminate(s.callID, dialog.ReasonLocalBYE)
	}

	return nil
}
