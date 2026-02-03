package parking

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/sebas/switchboard/internal/signaling/dialog"
	"github.com/sebas/switchboard/internal/signaling/mediaclient"
)

// Service orchestrates call parking operations.
type Service struct {
	registry  *Registry
	transport mediaclient.Transport
	logger    *slog.Logger
}

// ServiceConfig contains dependencies for creating a ParkService.
type ServiceConfig struct {
	Transport mediaclient.Transport
	Logger    *slog.Logger
}

// NewService creates a new parking service.
func NewService(cfg ServiceConfig) *Service {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	return &Service{
		registry:  NewRegistry(),
		transport: cfg.Transport,
		logger:    cfg.Logger,
	}
}

// ParkRequest contains parameters for parking a call.
type ParkRequest struct {
	// SlotID is the specific slot to park in (empty for auto-assign)
	SlotID string

	// CallID is the SIP Call-ID
	CallID string

	// Dialog is the SIP dialog being parked
	Dialog *dialog.Dialog

	// SessionID is the RTP session ID
	SessionID string

	// ParkedBy identifies who parked the call
	ParkedBy string

	// MOHFiles are the audio files to play as music-on-hold
	MOHFiles []string
}

// ParkResult contains the result of a park operation.
type ParkResult struct {
	SlotID string
}

// Park places a call in a parking slot and starts MOH playback.
func (s *Service) Park(ctx context.Context, req ParkRequest) (*ParkResult, error) {
	s.logger.Info("[Parking] Park request",
		"call_id", req.CallID,
		"session_id", req.SessionID,
		"slot_id", req.SlotID,
		"parked_by", req.ParkedBy,
		"moh_files", req.MOHFiles,
	)

	// Create the slot
	slot := &ParkSlot{
		ID:        req.SlotID,
		CallID:    req.CallID,
		Dialog:    req.Dialog,
		SessionID: req.SessionID,
		ParkedAt:  time.Now(),
		ParkedBy:  req.ParkedBy,
		MOHFiles:  req.MOHFiles,
	}

	// Register the slot
	assignedID, err := s.registry.Park(slot)
	if err != nil {
		s.logger.Error("[Parking] Failed to park call",
			"call_id", req.CallID,
			"error", err,
		)
		return nil, fmt.Errorf("park call: %w", err)
	}

	s.logger.Info("[Parking] Call parked",
		"call_id", req.CallID,
		"slot_id", assignedID,
	)

	// Start MOH playback (fire and forget - runs in background)
	if len(req.MOHFiles) > 0 && req.SessionID != "" {
		go s.startMOH(ctx, req.SessionID, req.MOHFiles, req.CallID)
	}

	return &ParkResult{SlotID: assignedID}, nil
}

// startMOH starts music-on-hold playback for a parked call.
func (s *Service) startMOH(ctx context.Context, sessionID string, files []string, callID string) {
	s.logger.Debug("[Parking] Starting MOH",
		"session_id", sessionID,
		"files", files,
	)

	playReq := mediaclient.PlayRequest{
		SessionID: sessionID,
		Files:     files,
		Loop:      true,
	}

	statusCh, err := s.transport.PlayAudio(ctx, playReq)
	if err != nil {
		s.logger.Error("[Parking] Failed to start MOH",
			"session_id", sessionID,
			"error", err,
		)
		return
	}

	// Monitor playback status (runs until stopped or error)
	for status := range statusCh {
		switch status.State {
		case mediaclient.PlayStateError:
			s.logger.Error("[Parking] MOH playback error",
				"session_id", sessionID,
				"call_id", callID,
				"error", status.Error,
			)
			return
		case mediaclient.PlayStateStopped:
			s.logger.Debug("[Parking] MOH stopped",
				"session_id", sessionID,
				"call_id", callID,
			)
			return
		case mediaclient.PlayStateCompleted:
			// Should not happen with loop=true, but handle gracefully
			s.logger.Debug("[Parking] MOH completed",
				"session_id", sessionID,
				"call_id", callID,
			)
			return
		}
	}
}

// UnparkRequest contains parameters for retrieving a parked call.
type UnparkRequest struct {
	// SlotID is the slot to retrieve from
	SlotID string
}

// UnparkResult contains the result of an unpark operation.
type UnparkResult struct {
	Slot *ParkSlot
}

// Unpark retrieves a call from a parking slot and stops MOH.
func (s *Service) Unpark(ctx context.Context, req UnparkRequest) (*UnparkResult, error) {
	s.logger.Info("[Parking] Unpark request",
		"slot_id", req.SlotID,
	)

	// Remove from registry
	slot, err := s.registry.Unpark(req.SlotID)
	if err != nil {
		s.logger.Error("[Parking] Failed to unpark call",
			"slot_id", req.SlotID,
			"error", err,
		)
		return nil, fmt.Errorf("unpark call: %w", err)
	}

	s.logger.Info("[Parking] Call unparked",
		"slot_id", req.SlotID,
		"call_id", slot.CallID,
		"session_id", slot.SessionID,
	)

	// Stop MOH playback
	if slot.SessionID != "" {
		if err := s.transport.StopAudio(ctx, slot.SessionID); err != nil {
			// Non-fatal - call may have already hung up
			s.logger.Warn("[Parking] Failed to stop MOH",
				"session_id", slot.SessionID,
				"error", err,
			)
		}
	}

	return &UnparkResult{Slot: slot}, nil
}

// CleanupByCallID removes a parked call when the caller hangs up.
// This is called from the dialog termination callback.
func (s *Service) CleanupByCallID(callID string) {
	slot, err := s.registry.UnparkByCallID(callID)
	if err != nil {
		// Not parked - this is normal for non-parked calls
		return
	}

	s.logger.Info("[Parking] Cleaned up parked call (caller hangup)",
		"slot_id", slot.ID,
		"call_id", callID,
		"parked_duration", slot.Duration().String(),
	)

	// Note: MOH will stop automatically when the session is destroyed
	// by the dialog termination handler in app.go
}

// Get retrieves a parked call by slot ID.
func (s *Service) Get(slotID string) (*ParkSlot, bool) {
	return s.registry.Get(slotID)
}

// GetByCallID retrieves a parked call by Call-ID.
func (s *Service) GetByCallID(callID string) (*ParkSlot, bool) {
	return s.registry.GetByCallID(callID)
}

// List returns all parked calls.
func (s *Service) List() []*ParkSlot {
	return s.registry.List()
}

// Count returns the number of parked calls.
func (s *Service) Count() int {
	return s.registry.Count()
}

// ForceUnpark removes a parked call without a retriever.
// Used for administrative cleanup.
func (s *Service) ForceUnpark(ctx context.Context, slotID string) error {
	s.logger.Info("[Parking] Force unpark request",
		"slot_id", slotID,
	)

	// Remove from registry
	slot, err := s.registry.Unpark(slotID)
	if err != nil {
		return err
	}

	s.logger.Info("[Parking] Force unparked call",
		"slot_id", slotID,
		"call_id", slot.CallID,
	)

	// Stop MOH playback
	if slot.SessionID != "" {
		if err := s.transport.StopAudio(ctx, slot.SessionID); err != nil {
			s.logger.Warn("[Parking] Failed to stop MOH during force unpark",
				"session_id", slot.SessionID,
				"error", err,
			)
		}
	}

	return nil
}
