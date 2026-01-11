package registration

import (
	"fmt"
	"log/slog"
	"sync"

	"github.com/emiago/sipgo/sip"
)

// Registration represents a SIP user registration
type Registration struct {
	AOR          string // Address of Record (user SIP URI)
	Contact      string // Contact URI where user can be reached
	Expires      int    // Registration expiration time in seconds
	RegisteredAt int64  // Unix timestamp when registered
}

// Store manages SIP user registrations
type Store struct {
	registrations map[string]*Registration // Key: AOR, Value: Registration
	mu            sync.RWMutex
}

// NewStore creates a new registration store
func NewStore() *Store {
	return &Store{
		registrations: make(map[string]*Registration),
	}
}

// Register registers or updates a user registration
func (s *Store) Register(aor string, contact string, expires int) (*Registration, error) {
	if aor == "" {
		return nil, fmt.Errorf("AOR cannot be empty")
	}
	if contact == "" {
		return nil, fmt.Errorf("contact cannot be empty")
	}
	if expires < 0 {
		return nil, fmt.Errorf("expires cannot be negative")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	reg := &Registration{
		AOR:     aor,
		Contact: contact,
		Expires: expires,
	}

	s.registrations[aor] = reg
	slog.Info("[REGISTRATION] Registered", "aor", aor, "contact", contact, "expires", expires)

	return reg, nil
}

// Unregister removes a user registration
func (s *Store) Unregister(aor string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.registrations[aor]; !exists {
		return fmt.Errorf("registration not found for %s", aor)
	}

	delete(s.registrations, aor)
	slog.Info("[REGISTRATION] Unregistered", "aor", aor)

	return nil
}

// Get retrieves a registration by AOR
func (s *Store) Get(aor string) (*Registration, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	reg, exists := s.registrations[aor]
	return reg, exists
}

// List returns all current registrations
func (s *Store) List() map[string]*Registration {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Return a copy to prevent external modification
	regs := make(map[string]*Registration)
	for k, v := range s.registrations {
		regs[k] = v
	}
	return regs
}

// Clear removes all registrations
func (s *Store) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.registrations = make(map[string]*Registration)
	slog.Info("[REGISTRATION] Cleared all registrations")
}

// Handler handles REGISTER requests
type Handler struct {
	store *Store
}

// NewHandler creates a new registration handler
func NewHandler(store *Store) *Handler {
	return &Handler{
		store: store,
	}
}

// HandleRegister processes a REGISTER request
func (h *Handler) HandleRegister(req *sip.Request, tx sip.ServerTransaction) error {
	slog.Info("[REGISTRATION] Processing REGISTER", "from", req.Source())
	slog.Info("[REGISTRATION] To", "to", req.To())

	// Extract AOR from request To header
	toHeader := req.To()
	if toHeader == nil {
		return fmt.Errorf("missing To header")
	}

	aor := toHeader.Address.String()
	slog.Info("[REGISTRATION] AOR", "aor", aor)

	// Extract contacts from request
	contacts := req.GetHeaders("Contact")
	if len(contacts) == 0 {
		// Unregister all
		if err := h.store.Unregister(aor); err != nil {
			slog.Info("[REGISTRATION] Unregister failed", "error", err.Error())
		}

		// Send 200 OK response
		return h.sendResponse(tx, req, sip.StatusOK, "OK")
	}

	// Register each contact
	for _, contactHdr := range contacts {
		contact, ok := contactHdr.(*sip.ContactHeader)
		if !ok {
			slog.Info("[REGISTRATION] Invalid contact header type")
			continue
		}

		var expires int = 3600 // Default 1 hour
		// Note: Check actual sipgo ContactHeader fields for expiration
		// For now, use default or parse from Expires header in request

		contactURI := contact.Address.String()
		if _, err := h.store.Register(aor, contactURI, expires); err != nil {
			slog.Info("[REGISTRATION] Registration failed", "error", err.Error())
			return h.sendResponse(tx, req, sip.StatusBadRequest, "Invalid registration")
		}
	}

	// Send 200 OK response
	return h.sendResponse(tx, req, sip.StatusOK, "OK")
}

// sendResponse sends a SIP response
func (h *Handler) sendResponse(tx sip.ServerTransaction, req *sip.Request, statusCode sip.StatusCode, reason string) error {
	res := sip.NewResponseFromRequest(req, statusCode, reason, nil)

	// Add Contact header to response
	if statusCode == sip.StatusOK {
		toHeader := req.To()
		if toHeader != nil {
			contactHdr := &sip.ContactHeader{
				Address: toHeader.Address,
			}
			res.AppendHeader(contactHdr)
		}
	}

	if err := tx.Respond(res); err != nil {
		slog.Info("[REGISTRATION] Failed to send response", "error", err.Error())
		return err
	}

	slog.Info("[REGISTRATION] Sent response", "statusCode", int(statusCode), "reason", reason)
	return nil
}

// GetContact retrieves a user's contact address
func (h *Handler) GetContact(aor string) (string, error) {
	reg, exists := h.store.Get(aor)
	if !exists {
		return "", fmt.Errorf("user not registered: %s", aor)
	}
	return reg.Contact, nil
}

// GetRegistration retrieves a specific registration
func (h *Handler) GetRegistration(aor string) (*Registration, error) {
	reg, exists := h.store.Get(aor)
	if !exists {
		return nil, fmt.Errorf("registration not found: %s", aor)
	}
	return reg, nil
}

// GetAllRegistrations returns all current registrations
func (h *Handler) GetAllRegistrations() map[string]*Registration {
	return h.store.List()
}
