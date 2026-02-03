package parking

import (
	"context"
)

// APISlotInfo matches the api.ParkSlotInfo structure for API responses.
type APISlotInfo struct {
	ID        string   `json:"id"`
	CallID    string   `json:"call_id"`
	SessionID string   `json:"session_id"`
	ParkedAt  string   `json:"parked_at"`
	ParkedBy  string   `json:"parked_by"`
	Duration  int      `json:"duration_seconds"`
	MOHFiles  []string `json:"moh_files,omitempty"`
}

// APIAdapter wraps Service to implement api.ParkProvider interface.
type APIAdapter struct {
	service *Service
}

// NewAPIAdapter creates an adapter for the API server.
func NewAPIAdapter(svc *Service) *APIAdapter {
	return &APIAdapter{service: svc}
}

// List returns all parked calls in API format.
func (a *APIAdapter) List() []APISlotInfo {
	slots := a.service.List()
	result := make([]APISlotInfo, len(slots))
	for i, slot := range slots {
		result[i] = toAPISlotInfo(slot)
	}
	return result
}

// Get returns a specific slot in API format.
func (a *APIAdapter) Get(slotID string) (*APISlotInfo, bool) {
	slot, found := a.service.Get(slotID)
	if !found {
		return nil, false
	}
	info := toAPISlotInfo(slot)
	return &info, true
}

// ForceUnpark removes a parked call without a retriever.
func (a *APIAdapter) ForceUnpark(slotID string) error {
	return a.service.ForceUnpark(context.Background(), slotID)
}

func toAPISlotInfo(slot *ParkSlot) APISlotInfo {
	return APISlotInfo{
		ID:        slot.ID,
		CallID:    slot.CallID,
		SessionID: slot.SessionID,
		ParkedAt:  slot.ParkedAt.Format("2006-01-02T15:04:05Z07:00"),
		ParkedBy:  slot.ParkedBy,
		Duration:  int(slot.Duration().Seconds()),
		MOHFiles:  slot.MOHFiles,
	}
}
