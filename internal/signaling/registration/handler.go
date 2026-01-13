package registration

import (
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/emiago/sipgo/sip"
	"github.com/sebas/switchboard/internal/signaling/location"
)

// StatusIntervalTooBrief is the SIP status code 423 per RFC 3261.
// Used when the requested registration expires is below the minimum.
const StatusIntervalTooBrief sip.StatusCode = 423

// Handler handles REGISTER requests
type Handler struct {
	locationStore location.LocationStore
	realm         string
}

// NewHandler creates a new registration handler
func NewHandler(locationStore location.LocationStore, realm string) *Handler {
	return &Handler{
		locationStore: locationStore,
		realm:         realm,
	}
}

// HandleRegister processes a REGISTER request
func (h *Handler) HandleRegister(req *sip.Request, tx sip.ServerTransaction) error {
	slog.Debug("[REGISTER] Processing", "from", req.Source())

	// Extract AOR from To header
	toHeader := req.To()
	if toHeader == nil {
		return h.sendResponse(tx, req, sip.StatusBadRequest, "Missing To header")
	}
	aor := toHeader.Address.String()

	// Get source address info for NAT handling
	source := req.Source()
	receivedIP, receivedPort := parseSourceAddr(source)

	// Get transport from Via or connection
	transport := "UDP"
	if via := req.Via(); via != nil {
		if t := via.Transport; t != "" {
			transport = strings.ToUpper(t)
		}
	}

	// Extract Call-ID and CSeq for binding validation
	callID := ""
	if req.CallID() != nil {
		callID = req.CallID().String()
	}
	var cseq uint32
	if cseqHdr := req.CSeq(); cseqHdr != nil {
		cseq = cseqHdr.SeqNo
	}

	// Get User-Agent
	userAgent := ""
	if uaHdr := req.GetHeader("User-Agent"); uaHdr != nil {
		userAgent = uaHdr.Value()
	}

	// Extract contacts from request
	contacts := req.GetHeaders("Contact")

	// Check for wildcard unregister: Contact: *
	// RFC 3261 Section 10.3 Step 6: If Contact: * is present, there must be
	// no other Contact headers and Expires must be 0.
	hasWildcard := false
	for _, contactHdr := range contacts {
		if contact, ok := contactHdr.(*sip.ContactHeader); ok {
			if contact.Address.String() == "*" {
				hasWildcard = true
				break
			}
		}
	}

	if hasWildcard {
		// RFC 3261 Section 10.3: Wildcard must be the only Contact header
		if len(contacts) > 1 {
			return h.sendResponse(tx, req, sip.StatusBadRequest,
				"Contact: * must not be combined with other Contact headers")
		}

		// Wildcard unregister - requires Expires: 0
		expires := h.getExpires(req, nil)
		if expires != 0 {
			return h.sendResponse(tx, req, sip.StatusBadRequest, "Expires must be 0 for Contact: *")
		}
		if err := h.locationStore.Unregister(aor, "", true); err != nil {
			slog.Debug("[REGISTER] Wildcard unregister failed", "error", err)
		}
		return h.sendResponse(tx, req, sip.StatusOK, "OK")
	}

	// No contacts = query (return current bindings)
	if len(contacts) == 0 {
		return h.sendQueryResponse(tx, req, aor)
	}

	// Process each contact
	var lastBinding *location.Binding
	for _, contactHdr := range contacts {
		contact, ok := contactHdr.(*sip.ContactHeader)
		if !ok {
			slog.Debug("[REGISTER] Invalid contact header type")
			continue
		}

		contactURI := contact.Address.String()
		expires := h.getExpires(req, contact)

		// Expires: 0 = unregister this contact
		if expires == 0 {
			bindingID := location.GenerateBindingID(contactURI, h.extractInstanceID(contact))
			if err := h.locationStore.Unregister(aor, bindingID, false); err != nil {
				slog.Debug("[REGISTER] Unregister failed", "error", err)
			}
			continue
		}

		// Create binding
		binding := &location.Binding{
			AOR:          aor,
			ContactURI:   contactURI,
			ReceivedIP:   receivedIP,
			ReceivedPort: receivedPort,
			Transport:    transport,
			InstanceID:   h.extractInstanceID(contact),
			QValue:       h.extractQValue(contact),
			Expires:      expires,
			CallID:       callID,
			CSeq:         cseq,
			UserAgent:    userAgent,
		}

		// Extract Path headers if present
		pathHdrs := req.GetHeaders("Path")
		if len(pathHdrs) > 0 {
			binding.Path = make([]string, len(pathHdrs))
			for i, hdr := range pathHdrs {
				binding.Path[i] = hdr.Value()
			}
		}

		// Register
		registered, err := h.locationStore.Register(binding)
		if err != nil {
			// RFC 3261 Section 10.3: If expires is below minimum, respond with 423
			if errors.Is(err, location.ErrIntervalTooBrief) {
				return h.sendIntervalTooBrief(tx, req)
			}
			slog.Error("[REGISTER] Registration failed", "error", err, "aor", aor)
			return h.sendResponse(tx, req, sip.StatusBadRequest, err.Error())
		}
		lastBinding = registered
	}

	// Send 200 OK with current bindings
	return h.sendOKWithBindings(tx, req, aor, lastBinding)
}

// getExpires extracts expiration time from request
// Priority: Contact param > Expires header > default (3600)
func (h *Handler) getExpires(req *sip.Request, contact *sip.ContactHeader) int {
	// Check Contact expires parameter first
	if contact != nil && contact.Params != nil {
		if expiresStr, ok := contact.Params.Get("expires"); ok {
			if expires, err := strconv.Atoi(expiresStr); err == nil {
				return expires
			}
		}
	}

	// Check Expires header
	if expiresHdr := req.GetHeader("Expires"); expiresHdr != nil {
		if expires, err := strconv.Atoi(expiresHdr.Value()); err == nil {
			return expires
		}
	}

	// Default
	return 3600
}

// extractInstanceID extracts +sip.instance from Contact params
func (h *Handler) extractInstanceID(contact *sip.ContactHeader) string {
	if contact == nil || contact.Params == nil {
		return ""
	}
	if instance, ok := contact.Params.Get("+sip.instance"); ok {
		// Remove angle brackets if present
		instance = strings.Trim(instance, "<>\"")
		return instance
	}
	return ""
}

// extractQValue extracts q parameter from Contact
func (h *Handler) extractQValue(contact *sip.ContactHeader) float32 {
	if contact == nil || contact.Params == nil {
		return 0
	}
	if qStr, ok := contact.Params.Get("q"); ok {
		if q, err := strconv.ParseFloat(qStr, 32); err == nil {
			return float32(q)
		}
	}
	return 0
}

// sendResponse sends a SIP response
func (h *Handler) sendResponse(tx sip.ServerTransaction, req *sip.Request, statusCode sip.StatusCode, reason string) error {
	res := sip.NewResponseFromRequest(req, statusCode, reason, nil)

	// Add received/rport to Via per RFC 3581 for NAT traversal
	h.addViaParams(res, req)

	if err := tx.Respond(res); err != nil {
		slog.Error("[REGISTER] Failed to send response", "error", err)
		return err
	}

	slog.Debug("[REGISTER] Sent response", "status", int(statusCode), "reason", reason)
	return nil
}

// sendIntervalTooBrief sends a 423 Interval Too Brief response with Min-Expires header.
// Per RFC 3261 Section 10.3, this response MUST include a Min-Expires header field
// that indicates the minimum expiration interval the registrar is willing to honor.
func (h *Handler) sendIntervalTooBrief(tx sip.ServerTransaction, req *sip.Request) error {
	res := sip.NewResponseFromRequest(req, StatusIntervalTooBrief, "Interval Too Brief", nil)

	// Add received/rport to Via per RFC 3581 for NAT traversal
	h.addViaParams(res, req)

	// Add Min-Expires header per RFC 3261 Section 10.3
	minExpires := h.locationStore.MinExpires()
	res.AppendHeader(sip.NewHeader("Min-Expires", strconv.Itoa(minExpires)))

	if err := tx.Respond(res); err != nil {
		slog.Error("[REGISTER] Failed to send 423 response", "error", err)
		return err
	}

	slog.Debug("[REGISTER] Sent 423 Interval Too Brief", "min_expires", minExpires)
	return nil
}

// sendQueryResponse sends 200 OK with current bindings (query response)
func (h *Handler) sendQueryResponse(tx sip.ServerTransaction, req *sip.Request, aor string) error {
	res := sip.NewResponseFromRequest(req, sip.StatusOK, "OK", nil)

	// Add received/rport to Via per RFC 3581 for NAT traversal
	h.addViaParams(res, req)

	// Add Date header per RFC 3261 recommendation
	h.addDateHeader(res)

	// Add Contact headers for current bindings
	bindings := h.locationStore.Lookup(aor)
	for _, b := range bindings {
		h.addContactHeader(res, b)
	}

	if err := tx.Respond(res); err != nil {
		slog.Error("[REGISTER] Failed to send query response", "error", err)
		return err
	}

	slog.Debug("[REGISTER] Sent query response", "aor", aor, "bindings", len(bindings))
	return nil
}

// sendOKWithBindings sends 200 OK with updated binding info
func (h *Handler) sendOKWithBindings(tx sip.ServerTransaction, req *sip.Request, aor string, binding *location.Binding) error {
	res := sip.NewResponseFromRequest(req, sip.StatusOK, "OK", nil)

	// Add received/rport to Via per RFC 3581 for NAT traversal
	h.addViaParams(res, req)

	// Add Date header per RFC 3261 recommendation
	h.addDateHeader(res)

	// Add Contact headers for all current bindings
	bindings := h.locationStore.Lookup(aor)
	for _, b := range bindings {
		h.addContactHeader(res, b)
	}

	if err := tx.Respond(res); err != nil {
		slog.Error("[REGISTER] Failed to send OK response", "error", err)
		return err
	}

	slog.Info("[REGISTER] Success", "aor", aor, "bindings", len(bindings))
	return nil
}

// addContactHeader adds a Contact header for a binding
func (h *Handler) addContactHeader(res *sip.Response, b *location.Binding) {
	// Parse the contact URI
	var uri sip.Uri
	if err := sip.ParseUri(b.ContactURI, &uri); err != nil {
		slog.Debug("[REGISTER] Failed to parse contact URI", "uri", b.ContactURI, "error", err)
		return
	}

	contactHdr := &sip.ContactHeader{
		Address: uri,
		Params:  sip.NewParams(),
	}

	// Add expires parameter
	contactHdr.Params.Add("expires", fmt.Sprintf("%d", b.Expires))

	res.AppendHeader(contactHdr)
}

// GetContact retrieves a user's contact address (highest priority binding)
func (h *Handler) GetContact(aor string) (string, error) {
	binding := h.locationStore.LookupOne(aor)
	if binding == nil {
		return "", fmt.Errorf("user not registered: %s", aor)
	}
	return binding.EffectiveContact(), nil
}

// GetBinding retrieves a specific binding
func (h *Handler) GetBinding(aor string) (*location.Binding, error) {
	binding := h.locationStore.LookupOne(aor)
	if binding == nil {
		return nil, fmt.Errorf("binding not found: %s", aor)
	}
	return binding, nil
}

// GetAllBindings returns all bindings for an AOR
func (h *Handler) GetAllBindings(aor string) []*location.Binding {
	return h.locationStore.Lookup(aor)
}

// GetAllRegistrations returns all current registrations grouped by AOR
func (h *Handler) GetAllRegistrations() map[string][]*location.Binding {
	return h.locationStore.ListByAOR()
}

// ListAll returns all bindings
func (h *Handler) ListAll() []*location.Binding {
	return h.locationStore.List()
}

// parseSourceAddr parses source address into IP and port
func parseSourceAddr(source string) (string, int) {
	if source == "" {
		return "", 0
	}

	// Handle IPv6
	if strings.HasPrefix(source, "[") {
		idx := strings.LastIndex(source, "]:")
		if idx > 0 {
			ip := source[1:idx]
			portStr := source[idx+2:]
			if port, err := strconv.Atoi(portStr); err == nil {
				return ip, port
			}
		}
		return source, 0
	}

	// IPv4
	parts := strings.Split(source, ":")
	if len(parts) == 2 {
		if port, err := strconv.Atoi(parts[1]); err == nil {
			return parts[0], port
		}
	}
	return source, 0
}

// addViaParams adds received and rport parameters to the Via header in the response.
// Per RFC 3581 (Symmetric Response Routing), the server SHOULD add:
// - received: the source IP address the request was received from
// - rport: the source port the request was received from
// This enables proper NAT traversal by routing responses to the actual source.
func (h *Handler) addViaParams(res *sip.Response, req *sip.Request) {
	via := res.Via()
	if via == nil {
		return
	}

	// Get source IP and port from the request
	receivedIP, receivedPort := parseSourceAddr(req.Source())
	if receivedIP == "" {
		return
	}

	// Add received parameter with source IP
	// RFC 3261 Section 18.2.1: Add received if the sent-by host differs from source
	if via.Params == nil {
		via.Params = sip.NewParams()
	}
	via.Params.Add("received", receivedIP)

	// Add rport parameter per RFC 3581 if source port is available
	if receivedPort > 0 {
		via.Params.Add("rport", strconv.Itoa(receivedPort))
	}
}

// addDateHeader adds a Date header to the response.
// Per RFC 3261 Section 20.17, the Date header field contains the date and time.
// Including it in 2xx responses to REGISTER is recommended for client clock sync.
func (h *Handler) addDateHeader(res *sip.Response) {
	// RFC 3261 specifies the format should be RFC 1123 (which is HTTP date format)
	// Example: "Sun, 06 Nov 1994 08:49:37 GMT"
	dateStr := time.Now().UTC().Format(time.RFC1123)
	res.AppendHeader(sip.NewHeader("Date", dateStr))
}
