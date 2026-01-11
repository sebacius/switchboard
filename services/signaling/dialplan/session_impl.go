package dialplan

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/sebas/switchboard/services/signaling/dialog"
	"github.com/sebas/switchboard/services/signaling/location"
	"github.com/sebas/switchboard/services/signaling/transport"
)

// sessionImpl implements CallSession, bridging dialplan with existing components.
type sessionImpl struct {
	mu sync.Mutex

	// Identity
	callID      string
	destination string
	callerID    string

	// Core components
	ctx       context.Context
	cancel    context.CancelFunc
	dialog    *dialog.Dialog
	transport transport.Transport
	dialogMgr *dialog.Manager
	locStore  location.LocationStore
	logger    *slog.Logger

	// Session state
	sessionID  string
	terminated bool
}

// SessionConfig contains dependencies for creating a CallSession.
type SessionConfig struct {
	Dialog      *dialog.Dialog
	Transport   transport.Transport
	DialogMgr   *dialog.Manager
	LocStore    location.LocationStore
	Logger      *slog.Logger
	Destination string
	CallerID    string
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
		ctx:         ctx,
		cancel:      cancel,
		dialog:      cfg.Dialog,
		transport:   cfg.Transport,
		dialogMgr:   cfg.DialogMgr,
		locStore:    cfg.LocStore,
		logger:      cfg.Logger,
		sessionID:   cfg.Dialog.GetSessionID(),
	}
}

func (s *sessionImpl) CallID() string      { return s.callID }
func (s *sessionImpl) Destination() string { return s.destination }
func (s *sessionImpl) CallerID() string    { return s.callerID }
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
	playReq := transport.PlayRequest{
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
		case transport.PlayStateCompleted:
			s.logger.Debug("[Session] Playback completed",
				"call_id", s.callID,
				"file", file,
			)
			return nil
		case transport.PlayStateError:
			s.logger.Warn("[Session] Playback error",
				"call_id", s.callID,
				"file", file,
				"error", status.Error,
			)
			return status.Error
		case transport.PlayStateStopped:
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
// This is the B2BUA dial action - it will be expanded in future iterations.
func (s *sessionImpl) Dial(ctx context.Context, target string, timeout time.Duration) error {
	s.logger.Info("[Session] Dial action",
		"call_id", s.callID,
		"target", target,
		"timeout", timeout,
	)

	// Parse target
	contactURI, err := s.resolveTarget(target)
	if err != nil {
		return &DialError{
			Target: target,
			Cause:  err,
		}
	}

	s.logger.Info("[Session] Resolved target",
		"call_id", s.callID,
		"target", target,
		"contact_uri", contactURI,
	)

	// TODO: Implement B2BUA flow:
	// 1. Create leg B dialog (UAC role)
	// 2. Create RTP session for leg B
	// 3. Send INVITE to resolved contact with leg B's SDP
	// 4. Wait for answer (183/200)
	// 5. Create media bridge between leg A and leg B
	// 6. Wait for BYE from either side
	// 7. Propagate hangup to other leg

	// For now, return an error indicating B2BUA is not yet implemented
	return &DialError{
		Target:  target,
		Cause:   fmt.Errorf("B2BUA dial not yet implemented (resolved: %s)", contactURI),
		SIPCode: 501,
	}
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
