package b2bua

// BridgedCallInfo contains information about the B-leg of a bridged call
type BridgedCallInfo struct {
	BLegCallID string // Call-ID of the B-leg dialog
}

// BridgeMapper maps A-leg dialogs to their B-leg counterparts for bridged calls.
// Used during drain migration to migrate both legs of a bridged call together.
//
// Both A-leg and B-leg dialogs are registered with dialog.Manager.
// This interface only provides the mapping between them.
//
// Implemented by the Originator which tracks the A-leg to B-leg relationship.
type BridgeMapper interface {
	// GetBridgedBLeg returns the B-leg Call-ID for a given A-leg Call-ID.
	// Returns nil if this A-leg is not part of a bridged call (e.g., IVR call).
	GetBridgedBLeg(aLegCallID string) *BridgedCallInfo
}
