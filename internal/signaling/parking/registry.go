package parking

import (
	"fmt"
	"sync"
)

const (
	// SlotMin is the minimum slot number (inclusive)
	SlotMin = 700
	// SlotMax is the maximum slot number (inclusive)
	SlotMax = 799
)

// Registry manages park slot allocation and lookup.
type Registry struct {
	mu       sync.RWMutex
	slots    map[string]*ParkSlot // slot ID -> ParkSlot
	byCallID map[string]*ParkSlot // Call-ID -> ParkSlot
}

// NewRegistry creates an empty park registry.
func NewRegistry() *Registry {
	return &Registry{
		slots:    make(map[string]*ParkSlot),
		byCallID: make(map[string]*ParkSlot),
	}
}

// ErrSlotOccupied is returned when attempting to park in an occupied slot.
var ErrSlotOccupied = fmt.Errorf("slot is occupied")

// ErrSlotNotFound is returned when a slot doesn't exist.
var ErrSlotNotFound = fmt.Errorf("slot not found")

// ErrNoSlotsAvailable is returned when all slots are occupied.
var ErrNoSlotsAvailable = fmt.Errorf("no parking slots available")

// ErrCallAlreadyParked is returned when attempting to park an already-parked call.
var ErrCallAlreadyParked = fmt.Errorf("call is already parked")

// Park adds a call to the registry in the specified slot.
// If slotID is empty, auto-assigns the next available slot.
// Returns the assigned slot ID.
func (r *Registry) Park(slot *ParkSlot) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Check if call is already parked
	if _, exists := r.byCallID[slot.CallID]; exists {
		return "", ErrCallAlreadyParked
	}

	// Auto-assign slot if not specified
	if slot.ID == "" {
		assignedID, err := r.findNextAvailableSlot()
		if err != nil {
			return "", err
		}
		slot.ID = assignedID
	} else {
		// Validate slot is in range
		if !r.isValidSlotID(slot.ID) {
			return "", fmt.Errorf("invalid slot ID: %s (must be %d-%d)", slot.ID, SlotMin, SlotMax)
		}
		// Check if slot is occupied
		if _, exists := r.slots[slot.ID]; exists {
			return "", ErrSlotOccupied
		}
	}

	// Store in both maps
	r.slots[slot.ID] = slot
	r.byCallID[slot.CallID] = slot

	return slot.ID, nil
}

// Unpark removes a call from the registry by slot ID.
// Returns the removed ParkSlot or error if not found.
func (r *Registry) Unpark(slotID string) (*ParkSlot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	slot, exists := r.slots[slotID]
	if !exists {
		return nil, ErrSlotNotFound
	}

	delete(r.slots, slotID)
	delete(r.byCallID, slot.CallID)

	return slot, nil
}

// UnparkByCallID removes a call from the registry by Call-ID.
// Returns the removed ParkSlot or error if not found.
func (r *Registry) UnparkByCallID(callID string) (*ParkSlot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	slot, exists := r.byCallID[callID]
	if !exists {
		return nil, ErrSlotNotFound
	}

	delete(r.slots, slot.ID)
	delete(r.byCallID, callID)

	return slot, nil
}

// Get retrieves a slot by ID without removing it.
func (r *Registry) Get(slotID string) (*ParkSlot, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	slot, exists := r.slots[slotID]
	return slot, exists
}

// GetByCallID retrieves a slot by Call-ID without removing it.
func (r *Registry) GetByCallID(callID string) (*ParkSlot, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	slot, exists := r.byCallID[callID]
	return slot, exists
}

// List returns all parked calls.
func (r *Registry) List() []*ParkSlot {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]*ParkSlot, 0, len(r.slots))
	for _, slot := range r.slots {
		result = append(result, slot)
	}
	return result
}

// Count returns the number of parked calls.
func (r *Registry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.slots)
}

// findNextAvailableSlot finds the lowest available slot number.
// Always assigns sequentially from SlotMin.
// Must be called with lock held.
func (r *Registry) findNextAvailableSlot() (string, error) {
	for i := SlotMin; i <= SlotMax; i++ {
		slotID := fmt.Sprintf("%d", i)
		if _, exists := r.slots[slotID]; !exists {
			return slotID, nil
		}
	}
	return "", ErrNoSlotsAvailable
}

// isValidSlotID checks if a slot ID is within the valid range.
func (r *Registry) isValidSlotID(slotID string) bool {
	var num int
	if _, err := fmt.Sscanf(slotID, "%d", &num); err != nil {
		return false
	}
	return num >= SlotMin && num <= SlotMax
}
