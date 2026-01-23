package drain

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/emiago/sipgo/sip"
	"github.com/sebas/switchboard/internal/signaling/b2bua"
	"github.com/sebas/switchboard/internal/signaling/dialog"
	"github.com/sebas/switchboard/internal/signaling/mediaclient"
)

// SessionMigrator handles migration of a single session to a new RTP manager
type SessionMigrator interface {
	// MigrateSession migrates a session to a new RTP manager node
	// Returns nil on success, error on failure
	// Returns ErrSkipBLeg if the session is a B-leg (will be migrated with A-leg)
	MigrateSession(ctx context.Context, sessionID, targetNodeID string) error
}

// ErrSkipBLeg indicates the session is a B-leg and should be skipped
// (it will be migrated together with its A-leg)
var ErrSkipBLeg = fmt.Errorf("session is B-leg, will be migrated with A-leg")

// MigratorConfig configures the session migrator
type MigratorConfig struct {
	Pool          *mediaclient.Pool
	DialogManager *dialog.Manager
	BridgeMapper  b2bua.BridgeMapper // For A-leg to B-leg mapping
	LocalContact  sip.Uri
	Mode          DrainMode
}

// Migrator implements SessionMigrator
type Migrator struct {
	pool         *mediaclient.Pool
	dialogMgr    *dialog.Manager
	bridgeMapper b2bua.BridgeMapper
	localContact sip.Uri
	mode         DrainMode
}

// NewMigrator creates a new session migrator
func NewMigrator(cfg MigratorConfig) *Migrator {
	return &Migrator{
		pool:         cfg.Pool,
		dialogMgr:    cfg.DialogManager,
		bridgeMapper: cfg.BridgeMapper,
		localContact: cfg.LocalContact,
		mode:         cfg.Mode,
	}
}

// SetBridgeMapper sets the bridge mapper for A-leg to B-leg mapping.
// Can be called after construction if the BridgeMapper isn't available at init time.
func (m *Migrator) SetBridgeMapper(mapper b2bua.BridgeMapper) {
	m.bridgeMapper = mapper
}

// MigrateSession migrates a session to a new RTP manager.
// It automatically detects if the session is part of a bridged call
// and migrates both legs together.
func (m *Migrator) MigrateSession(ctx context.Context, sessionID, targetNodeID string) error {
	// Find the dialog associated with this session
	dlg, found := m.dialogMgr.FindBySessionID(sessionID)
	if !found {
		return fmt.Errorf("dialog not found for session %s", sessionID)
	}

	// Check if this is a B-leg (outbound) - skip it, will be migrated with A-leg
	if dlg.Direction == dialog.DirectionOutbound {
		slog.Debug("[Migrator] Skipping B-leg session (will be migrated with A-leg)",
			"session_id", sessionID,
			"call_id", dlg.CallID)
		return ErrSkipBLeg
	}

	// Check if dialog is in a state that can be migrated
	state := dlg.GetState()
	if state != dialog.StateConfirmed {
		return fmt.Errorf("dialog not in confirmed state (state: %s)", state)
	}

	// Check if this A-leg has an associated B-leg (bridged call)
	if m.bridgeMapper != nil {
		bridgeInfo := m.bridgeMapper.GetBridgedBLeg(dlg.CallID)
		if bridgeInfo != nil {
			// This is a bridged call - find the B-leg dialog and migrate both
			blegDlg, foundBleg := m.dialogMgr.Get(bridgeInfo.BLegCallID)
			if foundBleg {
				slog.Info("[Migrator] Detected bridged call, migrating both legs",
					"a_leg_session", sessionID,
					"b_leg_session", blegDlg.GetSessionID(),
					"a_leg_call_id", dlg.CallID,
					"b_leg_call_id", bridgeInfo.BLegCallID)
				return m.migrateBridgedCall(ctx, dlg, blegDlg, targetNodeID)
			}
			// B-leg dialog not found in manager, fall back to IVR migration
			slog.Warn("[Migrator] B-leg dialog not found in manager, treating as IVR call",
				"a_leg_call_id", dlg.CallID,
				"b_leg_call_id", bridgeInfo.BLegCallID)
		}
	}

	// No B-leg - this is an IVR call, migrate just the A-leg
	return m.migrateIVRCall(ctx, dlg, sessionID, targetNodeID)
}

// migrateIVRCall migrates a single A-leg (IVR call without B-leg)
func (m *Migrator) migrateIVRCall(ctx context.Context, dlg *dialog.Dialog, sessionID, targetNodeID string) error {
	// Get the original media info
	remoteAddr, remotePort, codec := dlg.GetMediaEndpoint()
	if remoteAddr == "" {
		return fmt.Errorf("dialog has no media endpoint info")
	}

	slog.Info("[Migrator] Starting IVR session migration",
		"session_id", sessionID,
		"target_node", targetNodeID,
		"call_id", dlg.CallID)

	// Create new session on target node
	newSessionInfo := mediaclient.SessionInfo{
		CallID:        dlg.CallID,
		RemoteAddr:    remoteAddr,
		RemotePort:    remotePort,
		OfferedCodecs: []string{codec},
	}

	newSession, err := m.pool.CreateSessionOnNode(ctx, targetNodeID, newSessionInfo)
	if err != nil {
		return fmt.Errorf("failed to create session on target node: %w", err)
	}

	slog.Debug("[Migrator] New session created on target node",
		"session_id", sessionID,
		"new_session_id", newSession.SessionID,
		"target_node", targetNodeID)

	// Build re-INVITE options with new SDP
	reInviteOpts := dialog.ReINVITEOptions{
		SDP: newSession.SDPBody,
	}

	// Send re-INVITE to the client
	result, err := m.dialogMgr.SendReINVITE(ctx, dlg, m.localContact, reInviteOpts)
	if err != nil {
		// Rollback: destroy the new session
		slog.Warn("[Migrator] Re-INVITE failed, rolling back",
			"session_id", sessionID,
			"error", err)
		_ = m.pool.DestroySession(ctx, newSession.SessionID, mediaclient.TerminateReasonError)
		return fmt.Errorf("re-INVITE failed: %w", err)
	}

	if !result.Success {
		// Re-INVITE was rejected by the client
		slog.Warn("[Migrator] Re-INVITE rejected by client",
			"session_id", sessionID,
			"status", result.StatusCode,
			"reason", result.Reason)

		// Rollback: destroy the new session
		_ = m.pool.DestroySession(ctx, newSession.SessionID, mediaclient.TerminateReasonError)

		if m.mode == DrainModeAggressive {
			// In aggressive mode, terminate the call
			_ = m.dialogMgr.Terminate(dlg.CallID, dialog.ReasonLocalBYE)
			return fmt.Errorf("re-INVITE rejected (%d %s), call terminated",
				result.StatusCode, result.Reason)
		}

		// In graceful mode, keep the session on the old node
		return fmt.Errorf("re-INVITE rejected (%d %s), keeping on old node",
			result.StatusCode, result.Reason)
	}

	// Success! Destroy the old session and update the dialog
	oldSessionID := dlg.GetSessionID()
	if err := m.pool.DestroySession(ctx, oldSessionID, mediaclient.TerminateReasonNormal); err != nil {
		slog.Warn("[Migrator] Failed to destroy old session (non-fatal)",
			"old_session_id", oldSessionID,
			"error", err)
	}

	// Update dialog with new session ID
	dlg.SetSessionID(newSession.SessionID)

	slog.Info("[Migrator] IVR session migration completed successfully",
		"old_session_id", oldSessionID,
		"new_session_id", newSession.SessionID,
		"target_node", targetNodeID,
		"call_id", dlg.CallID)

	return nil
}

// migrateBridgedCall migrates both A-leg and B-leg of a bridged call
// Now uses dialog.Manager for both legs since B-legs are registered there
func (m *Migrator) migrateBridgedCall(ctx context.Context, dlgA, dlgB *dialog.Dialog, targetNodeID string) error {
	aLegSessionID := dlgA.GetSessionID()
	bLegSessionID := dlgB.GetSessionID()

	// Get media info for both legs
	remoteAddrA, remotePortA, codecA := dlgA.GetMediaEndpoint()
	if remoteAddrA == "" {
		return fmt.Errorf("A-leg has no media endpoint info")
	}

	remoteAddrB, remotePortB, codecB := dlgB.GetMediaEndpoint()
	if remoteAddrB == "" {
		return fmt.Errorf("B-leg has no media endpoint info")
	}

	slog.Info("[Migrator] Starting bridged call migration",
		"a_leg_session", aLegSessionID,
		"b_leg_session", bLegSessionID,
		"target_node", targetNodeID,
		"a_leg_call_id", dlgA.CallID,
		"b_leg_call_id", dlgB.CallID)

	// Step 1: Create new sessions for both legs on target node
	newSessionA, err := m.pool.CreateSessionOnNode(ctx, targetNodeID, mediaclient.SessionInfo{
		CallID:        dlgA.CallID,
		RemoteAddr:    remoteAddrA,
		RemotePort:    remotePortA,
		OfferedCodecs: []string{codecA},
	})
	if err != nil {
		return fmt.Errorf("failed to create A-leg session on target node: %w", err)
	}

	newSessionB, err := m.pool.CreateSessionOnNode(ctx, targetNodeID, mediaclient.SessionInfo{
		CallID:        dlgB.CallID,
		RemoteAddr:    remoteAddrB,
		RemotePort:    remotePortB,
		OfferedCodecs: []string{codecB},
	})
	if err != nil {
		// Rollback A-leg session
		_ = m.pool.DestroySession(ctx, newSessionA.SessionID, mediaclient.TerminateReasonError)
		return fmt.Errorf("failed to create B-leg session on target node: %w", err)
	}

	// Step 2: Send re-INVITE to both legs via dialog manager
	resultA, errA := m.dialogMgr.SendReINVITE(ctx, dlgA, m.localContact, dialog.ReINVITEOptions{
		SDP: newSessionA.SDPBody,
	})

	resultB, errB := m.dialogMgr.SendReINVITE(ctx, dlgB, m.localContact, dialog.ReINVITEOptions{
		SDP: newSessionB.SDPBody,
	})

	// Step 3: Check if both succeeded
	aSuccess := errA == nil && resultA != nil && resultA.Success
	bSuccess := errB == nil && resultB != nil && resultB.Success

	if !aSuccess || !bSuccess {
		// Rollback: destroy both new sessions
		slog.Warn("[Migrator] Bridged migration failed, rolling back",
			"a_leg_session", aLegSessionID,
			"b_leg_session", bLegSessionID,
			"a_success", aSuccess,
			"b_success", bSuccess,
			"error_a", errA,
			"error_b", errB)

		_ = m.pool.DestroySession(ctx, newSessionA.SessionID, mediaclient.TerminateReasonError)
		_ = m.pool.DestroySession(ctx, newSessionB.SessionID, mediaclient.TerminateReasonError)

		if m.mode == DrainModeAggressive {
			// In aggressive mode, terminate the call
			_ = m.dialogMgr.Terminate(dlgA.CallID, dialog.ReasonLocalBYE)
			_ = m.dialogMgr.Terminate(dlgB.CallID, dialog.ReasonLocalBYE)
		}

		// Build error message
		if errA != nil {
			return fmt.Errorf("A-leg re-INVITE failed: %w", errA)
		}
		if errB != nil {
			return fmt.Errorf("B-leg re-INVITE failed: %w", errB)
		}
		if resultA != nil && !resultA.Success {
			return fmt.Errorf("A-leg re-INVITE rejected: %d %s", resultA.StatusCode, resultA.Reason)
		}
		if resultB != nil && !resultB.Success {
			return fmt.Errorf("B-leg re-INVITE rejected: %d %s", resultB.StatusCode, resultB.Reason)
		}
		return fmt.Errorf("bridged migration failed")
	}

	// Step 4: Success! Destroy old sessions
	_ = m.pool.DestroySession(ctx, aLegSessionID, mediaclient.TerminateReasonNormal)
	_ = m.pool.DestroySession(ctx, bLegSessionID, mediaclient.TerminateReasonNormal)

	// Step 5: Update session IDs in both dialogs
	dlgA.SetSessionID(newSessionA.SessionID)
	dlgB.SetSessionID(newSessionB.SessionID)

	// Step 6: Re-establish bridge on the new node
	bridgeID, err := m.pool.BridgeMedia(ctx, newSessionA.SessionID, newSessionB.SessionID)
	if err != nil {
		slog.Error("[Migrator] Failed to re-establish bridge after migration",
			"a_leg_session", newSessionA.SessionID,
			"b_leg_session", newSessionB.SessionID,
			"error", err)
		// This is critical - the calls are migrated but not bridged
		// In aggressive mode, terminate the calls
		if m.mode == DrainModeAggressive {
			_ = m.dialogMgr.Terminate(dlgA.CallID, dialog.ReasonLocalBYE)
			_ = m.dialogMgr.Terminate(dlgB.CallID, dialog.ReasonLocalBYE)
		}
		return fmt.Errorf("failed to re-establish bridge: %w", err)
	}

	slog.Info("[Migrator] Bridged call migration completed successfully",
		"old_a_session", aLegSessionID,
		"old_b_session", bLegSessionID,
		"new_a_session", newSessionA.SessionID,
		"new_b_session", newSessionB.SessionID,
		"bridge_id", bridgeID,
		"target_node", targetNodeID)

	return nil
}
