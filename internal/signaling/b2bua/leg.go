package b2bua

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/sebas/switchboard/internal/signaling/dialog"
)

// Leg represents one side of a call in a B2BUA scenario.
//
// A Leg encapsulates:
//   - SIP dialog state (via Dialog)
//   - Media session (via SessionID)
//   - Lifecycle management (state transitions, hangup)
//
// Legs are created in two ways:
//   - Inbound (A-leg): Adopted from an existing dialog via CallService.AdoptInboundLeg
//   - Outbound (B-leg): Created via CallService.CreateOutboundLeg or Dial
//
// Thread Safety: All methods are safe for concurrent use.
type Leg interface {
	// ID returns the unique identifier for this leg.
	// Format: "leg-{uuid}" for generated IDs or custom ID if provided.
	ID() string

	// CallID returns the SIP Call-ID for this leg.
	// Each leg has its own Call-ID (B2BUA, not proxy).
	CallID() string

	// Direction returns whether this is an inbound or outbound leg.
	Direction() LegDirection

	// GetState returns the current state of the leg.
	GetState() LegState

	// GetTerminationCause returns why the leg was terminated.
	// Returns TerminationCauseNone if not yet terminated.
	GetTerminationCause() TerminationCause

	// WaitForState blocks until the leg reaches the target state or context is canceled.
	// Returns immediately if already in or past the target state.
	// Returns error if the leg reaches a terminal state before the target.
	WaitForState(ctx context.Context, target LegState) error

	// Dialog returns the underlying SIP dialog.
	// Use with caution - prefer Leg methods for state changes.
	Dialog() *dialog.Dialog

	// SessionID returns the RTP Manager session ID for this leg.
	// Empty string if no media session is established.
	SessionID() string

	// Context returns the leg's context.
	// Canceled when the leg is destroyed.
	Context() context.Context

	// Info returns detailed information about this leg.
	Info() *LegInfo

	// --- Lifecycle Operations ---

	// Answer sends 200 OK for an inbound leg (no-op for outbound).
	// Transitions state from Ringing to Answered.
	// Returns error if leg is not in Ringing state.
	Answer(ctx context.Context) error

	// Hangup terminates the leg with BYE (if answered) or CANCEL (if ringing).
	// Transitions state to Destroyed after cleanup.
	// Safe to call multiple times.
	Hangup(ctx context.Context, cause TerminationCause) error

	// Destroy releases all resources without SIP signaling.
	// Use for cleanup after receiving BYE/CANCEL.
	// Safe to call multiple times.
	Destroy()

	// --- Event Callbacks ---

	// OnStateChange registers a callback for state transitions.
	// Callback is invoked synchronously - keep handlers fast.
	// Returns a function to unregister the callback.
	OnStateChange(fn func(old, new LegState)) func()

	// OnTerminated registers a callback for termination.
	// Called once when leg reaches Destroyed state.
	OnTerminated(fn func(cause TerminationCause))
}

// LegInfo contains detailed information about a leg.
type LegInfo struct {
	// Identity
	ID        string       `json:"id"`
	CallID    string       `json:"call_id"`
	Direction LegDirection `json:"direction"`

	// SIP addressing
	LocalURI  string `json:"local_uri"`  // Our contact URI
	RemoteURI string `json:"remote_uri"` // Peer's contact URI
	FromURI   string `json:"from_uri"`   // From header
	ToURI     string `json:"to_uri"`     // To header

	// Media
	SessionID       string `json:"session_id,omitempty"`
	LocalRTPAddr    string `json:"local_rtp_addr,omitempty"`
	LocalRTPPort    int    `json:"local_rtp_port,omitempty"`
	RemoteRTPAddr   string `json:"remote_rtp_addr,omitempty"`
	RemoteRTPPort   int    `json:"remote_rtp_port,omitempty"`
	NegotiatedCodec string `json:"negotiated_codec,omitempty"`

	// State
	State            LegState         `json:"state"`
	TerminationCause TerminationCause `json:"termination_cause,omitempty"`

	// Timing
	CreatedAt    time.Time `json:"created_at"`
	RingingAt    time.Time `json:"ringing_at,omitempty"`
	AnsweredAt   time.Time `json:"answered_at,omitempty"`
	TerminatedAt time.Time `json:"terminated_at,omitempty"`

	// SIP response (for failed outbound legs)
	SIPCode   int    `json:"sip_code,omitempty"`
	SIPReason string `json:"sip_reason,omitempty"`
}

// Duration returns the total duration from creation to termination.
// Returns 0 if not yet terminated.
func (i *LegInfo) Duration() time.Duration {
	if i.TerminatedAt.IsZero() {
		return 0
	}
	return i.TerminatedAt.Sub(i.CreatedAt)
}

// RingDuration returns how long the leg was in Ringing state.
// Returns 0 if never rang or still ringing.
func (i *LegInfo) RingDuration() time.Duration {
	if i.RingingAt.IsZero() {
		return 0
	}
	end := i.AnsweredAt
	if end.IsZero() {
		end = i.TerminatedAt
	}
	if end.IsZero() {
		return 0
	}
	return end.Sub(i.RingingAt)
}

// TalkDuration returns how long the leg was in Answered state.
// Returns 0 if never answered or still talking.
func (i *LegInfo) TalkDuration() time.Duration {
	if i.AnsweredAt.IsZero() {
		return 0
	}
	end := i.TerminatedAt
	if end.IsZero() {
		return 0
	}
	return end.Sub(i.AnsweredAt)
}

// LegOption configures leg creation.
type LegOption func(*legOptions)

type legOptions struct {
	callerID      string
	callerName    string
	onTeardown    func(Leg) // Called when leg is being torn down (before state change)
	aLegSessionID string    // A-leg session ID for bridging on same RTP manager
}

// WithCallerID sets the caller ID (From URI user part) for outbound legs.
// This is typically the caller's phone number or extension.
func WithCallerID(callerID string) LegOption {
	return func(o *legOptions) {
		o.callerID = callerID
	}
}

// WithCallerName sets the caller display name (From header display name) for outbound legs.
func WithCallerName(callerName string) LegOption {
	return func(o *legOptions) {
		o.callerName = callerName
	}
}

// WithTeardownHandler sets a callback invoked when the leg is being torn down.
// This is called BEFORE the state changes to Destroyed, allowing the handler
// to send SIP signaling (BYE) before the leg is marked as terminated.
// For A-leg: handler should call dialogMgr.Terminate() to send BYE to caller.
// For B-leg: handler should call originator.SendBYE() to send BYE to callee.
func WithTeardownHandler(fn func(Leg)) LegOption {
	return func(o *legOptions) {
		o.onTeardown = fn
	}
}

// WithALegSessionID sets the A-leg's RTP session ID for the B-leg.
// This ensures the B-leg is created on the same RTP manager as the A-leg,
// which is required for RTP bridging.
func WithALegSessionID(sessionID string) LegOption {
	return func(o *legOptions) {
		o.aLegSessionID = sessionID
	}
}

// --- Implementation ---

// legImpl is the concrete implementation of the Leg interface.
type legImpl struct {
	mu sync.RWMutex

	// Identity
	id        string
	callID    string
	direction LegDirection

	// SIP addressing
	localURI  string
	remoteURI string
	fromURI   string
	toURI     string

	// State
	state            LegState
	terminationCause TerminationCause

	// SIP dialog
	dialog *dialog.Dialog

	// Media session
	sessionID       string
	localRTPAddr    string
	localRTPPort    int
	remoteRTPAddr   string
	remoteRTPPort   int
	negotiatedCodec string

	// Timing
	createdAt    time.Time
	ringingAt    time.Time
	answeredAt   time.Time
	terminatedAt time.Time

	// SIP response (for failed outbound legs)
	sipCode   int
	sipReason string

	// Outbound dialog state (for sending BYE)
	// These are populated from the 200 OK response
	remoteContactURI string // Contact header from 200 OK - used as Request-URI for BYE
	remoteToURI      string // To header URI from INVITE - used as To header in BYE
	localFromURI     string // From header URI from INVITE - used as From header in BYE
	remoteTag        string // Tag from To header in 200 OK
	localTag         string // Our From tag

	// Lifecycle - Using done channel pattern instead of storing context
	// This follows Go best practices: contexts are for passing to functions,
	// done channels are for signaling termination in long-lived objects
	done      chan struct{}
	closeOnce sync.Once

	// State change notification - replaces polling in WaitForState
	stateChanged chan struct{}

	// Callbacks - using map with unique IDs to fix unregister bug
	stateChangeCallbacks map[uint64]func(old, new LegState)
	terminatedCallbacks  map[uint64]func(cause TerminationCause)
	callbackMu           sync.Mutex
	callbackIDCounter    atomic.Uint64
	onTeardown           func(Leg) // Called before teardown to send SIP BYE
}

// NewInboundLeg creates a leg from an existing inbound dialog.
// Returns error if dlg is nil since an inbound leg requires a valid dialog.
func NewInboundLeg(dlg *dialog.Dialog, sessionID string, opts ...LegOption) (Leg, error) {
	if dlg == nil {
		return nil, ErrInvalidState
	}

	options := &legOptions{}
	for _, opt := range opts {
		opt(options)
	}

	id := "leg-" + uuid.New().String()

	// Determine initial state based on dialog state
	// If the dialog has already sent 200 OK, the leg is answered
	initialState := LegStateRinging
	now := time.Now()
	var answeredAt time.Time

	// Check if the dialog is in a state indicating the call is answered
	// StateWaitingACK = 200 OK sent, awaiting ACK
	// StateConfirmed = ACK received, dialog fully established
	// Either state means the call is "answered" from B2BUA perspective
	dlgState := dlg.GetState()
	if dlgState == dialog.StateWaitingACK || dlgState == dialog.StateConfirmed {
		initialState = LegStateAnswered
		answeredAt = now
	}

	leg := &legImpl{
		id:                   id,
		callID:               dlg.CallID,
		direction:            LegDirectionInbound,
		state:                initialState,
		dialog:               dlg,
		sessionID:            sessionID,
		createdAt:            now,
		ringingAt:            now,
		answeredAt:           answeredAt,
		done:                 make(chan struct{}),
		stateChanged:         make(chan struct{}),
		stateChangeCallbacks: make(map[uint64]func(old, new LegState)),
		terminatedCallbacks:  make(map[uint64]func(cause TerminationCause)),
		onTeardown:           options.onTeardown,
	}

	// Monitor dialog context for termination (e.g., caller sends BYE)
	// This ensures the leg is destroyed when the dialog terminates
	go leg.monitorDialogContext(dlg.Context())

	// Extract SIP addressing from dialog
	// Extract From/To URIs from the INVITE request
	if dlg.InviteRequest != nil {
		if from := dlg.InviteRequest.From(); from != nil {
			leg.fromURI = from.Address.String()
		}
		if to := dlg.InviteRequest.To(); to != nil {
			leg.toURI = to.Address.String()
		}
		if contact := dlg.InviteRequest.Contact(); contact != nil {
			leg.remoteURI = contact.Address.String()
		}
	}

	// Get media endpoint if available
	if addr, port, codec := dlg.GetMediaEndpoint(); addr != "" {
		leg.remoteRTPAddr = addr
		leg.remoteRTPPort = port
		leg.negotiatedCodec = codec
	}

	return leg, nil
}

// NewOutboundLeg creates a new outbound leg for dialing.
func NewOutboundLeg(callID, targetURI string, opts ...LegOption) (Leg, error) {
	options := &legOptions{}
	for _, opt := range opts {
		opt(options)
	}

	id := "leg-" + uuid.New().String()

	leg := &legImpl{
		id:                   id,
		callID:               callID,
		direction:            LegDirectionOutbound,
		state:                LegStateCreated,
		toURI:                targetURI,
		createdAt:            time.Now(),
		done:                 make(chan struct{}),
		stateChanged:         make(chan struct{}),
		stateChangeCallbacks: make(map[uint64]func(old, new LegState)),
		terminatedCallbacks:  make(map[uint64]func(cause TerminationCause)),
		onTeardown:           options.onTeardown,
	}

	return leg, nil
}

// --- Identity Methods ---

func (l *legImpl) ID() string {
	return l.id
}

func (l *legImpl) CallID() string {
	return l.callID
}

func (l *legImpl) Direction() LegDirection {
	return l.direction
}

// --- State Methods ---

func (l *legImpl) GetState() LegState {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.state
}

func (l *legImpl) GetTerminationCause() TerminationCause {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.terminationCause
}

func (l *legImpl) WaitForState(ctx context.Context, target LegState) error {
	for {
		l.mu.RLock()
		current := l.state
		// Get the current stateChanged channel under the same lock
		// This prevents race conditions where state changes between
		// checking state and waiting on channel
		stateChangedCh := l.stateChanged
		l.mu.RUnlock()

		// Check if we've reached the target state
		if current >= target {
			if current.IsTerminal() && target != LegStateDestroyed && target != LegStateFailed {
				return ErrLegTerminated
			}
			return nil
		}

		// Check if we're in a terminal state before reaching target
		if current.IsTerminal() {
			return ErrLegTerminated
		}

		// Wait for state change, context cancellation, or leg destruction
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-l.done:
			return ErrLegTerminated
		case <-stateChangedCh:
			// State changed, loop back to check
			continue
		}
	}
}

// --- Dialog & Session Methods ---

func (l *legImpl) Dialog() *dialog.Dialog {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.dialog
}

func (l *legImpl) SessionID() string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.sessionID
}

// Context returns a context that is canceled when the leg is destroyed.
// This is derived from the done channel, following Go best practices
// of not storing contexts in structs.
func (l *legImpl) Context() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-l.done
		cancel()
	}()
	return ctx
}

// --- Info Method ---

func (l *legImpl) Info() *LegInfo {
	l.mu.RLock()
	defer l.mu.RUnlock()

	return &LegInfo{
		ID:               l.id,
		CallID:           l.callID,
		Direction:        l.direction,
		LocalURI:         l.localURI,
		RemoteURI:        l.remoteURI,
		FromURI:          l.fromURI,
		ToURI:            l.toURI,
		SessionID:        l.sessionID,
		LocalRTPAddr:     l.localRTPAddr,
		LocalRTPPort:     l.localRTPPort,
		RemoteRTPAddr:    l.remoteRTPAddr,
		RemoteRTPPort:    l.remoteRTPPort,
		NegotiatedCodec:  l.negotiatedCodec,
		State:            l.state,
		TerminationCause: l.terminationCause,
		CreatedAt:        l.createdAt,
		RingingAt:        l.ringingAt,
		AnsweredAt:       l.answeredAt,
		TerminatedAt:     l.terminatedAt,
		SIPCode:          l.sipCode,
		SIPReason:        l.sipReason,
	}
}

// --- Lifecycle Operations ---

func (l *legImpl) Answer(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.direction != LegDirectionInbound {
		return &StateTransitionError{
			Entity:  "leg",
			ID:      l.id,
			From:    l.state,
			To:      LegStateAnswered,
			Message: "only inbound legs can be answered",
		}
	}

	if l.state != LegStateRinging && l.state != LegStateEarlyMedia {
		return &StateTransitionError{
			Entity: "leg",
			ID:     l.id,
			From:   l.state,
			To:     LegStateAnswered,
		}
	}

	// Transition state
	oldState := l.state
	l.state = LegStateAnswered
	l.answeredAt = time.Now()

	l.notifyStateChange(oldState, l.state)
	return nil
}

func (l *legImpl) Hangup(ctx context.Context, cause TerminationCause) error {
	slog.Debug("[Leg] Hangup called",
		"leg_id", l.id,
		"call_id", l.callID,
		"direction", l.direction.String(),
		"cause", cause.String(),
	)

	l.mu.Lock()
	if l.state.IsTerminal() {
		slog.Debug("[Leg] Hangup skipping - already terminal",
			"leg_id", l.id,
			"state", l.state.String(),
		)
		l.mu.Unlock()
		return nil // Already terminated, safe to call multiple times
	}

	// Get teardown handler before changing state
	teardownFn := l.onTeardown
	oldState := l.state

	// Mark as terminating (not yet destroyed) to prevent re-entry
	// but allow teardown handler to still work with the leg
	l.state = LegStateDestroyed
	l.terminationCause = cause
	l.terminatedAt = time.Now()
	l.mu.Unlock()

	// Call teardown handler to send SIP BYE signaling
	// This is called AFTER marking state to prevent infinite recursion
	// if the handler calls Hangup again
	if teardownFn != nil {
		slog.Debug("[Leg] Calling teardown handler",
			"leg_id", l.id,
			"call_id", l.callID,
		)
		teardownFn(l)
		slog.Debug("[Leg] Teardown handler completed",
			"leg_id", l.id,
		)
	} else {
		slog.Debug("[Leg] No teardown handler registered",
			"leg_id", l.id,
		)
	}

	// Close done channel to signal goroutines (only once)
	l.closeOnce.Do(func() {
		close(l.done)
	})

	l.notifyStateChange(oldState, LegStateDestroyed)
	l.notifyTerminated(cause)

	return nil
}

func (l *legImpl) Destroy() {
	_ = l.Hangup(context.Background(), TerminationCauseNormal)
}

// --- Event Callbacks ---

// OnStateChange registers a callback for state transitions.
// Returns a function to unregister the callback.
// Uses map-based registration with unique IDs to fix the bug where
// unregistering a callback could cause index issues.
func (l *legImpl) OnStateChange(fn func(old, new LegState)) func() {
	id := l.callbackIDCounter.Add(1)

	l.callbackMu.Lock()
	l.stateChangeCallbacks[id] = fn
	l.callbackMu.Unlock()

	return func() {
		l.callbackMu.Lock()
		delete(l.stateChangeCallbacks, id)
		l.callbackMu.Unlock()
	}
}

// OnTerminated registers a callback for termination.
// Called once when leg reaches Destroyed state.
func (l *legImpl) OnTerminated(fn func(cause TerminationCause)) {
	id := l.callbackIDCounter.Add(1)

	l.callbackMu.Lock()
	l.terminatedCallbacks[id] = fn
	l.callbackMu.Unlock()
}

// --- Internal Methods ---

// TransitionTo transitions the leg to a new state.
// State change is atomic, and callbacks are invoked after releasing the lock
// to prevent deadlocks if callbacks try to access the leg.
func (l *legImpl) TransitionTo(newState LegState) error {
	l.mu.Lock()

	oldState := l.state
	l.state = newState

	// Update timing
	switch newState {
	case LegStateRinging, LegStateEarlyMedia:
		if l.ringingAt.IsZero() {
			l.ringingAt = time.Now()
		}
	case LegStateAnswered:
		l.answeredAt = time.Now()
	case LegStateFailed, LegStateDestroyed:
		l.terminatedAt = time.Now()
	}

	// Signal state change by replacing the channel (broadcast pattern)
	// Any goroutine waiting on the old channel will be woken up
	oldCh := l.stateChanged
	l.stateChanged = make(chan struct{})

	// Release lock before notifying callbacks to prevent deadlock
	// if callbacks try to access the leg (e.g., call GetState())
	l.mu.Unlock()

	// Close old channel to wake up any waiting goroutines
	close(oldCh)

	l.notifyStateChange(oldState, newState)
	return nil
}

// SetDialog sets the SIP dialog for this leg.
func (l *legImpl) SetDialog(dlg *dialog.Dialog) {
	l.mu.Lock()
	direction := l.direction
	l.dialog = dlg

	if dlg != nil {
		l.callID = dlg.CallID
		// Extract From/To URIs from the INVITE request
		if dlg.InviteRequest != nil {
			if from := dlg.InviteRequest.From(); from != nil {
				l.fromURI = from.Address.String()
			}
			if to := dlg.InviteRequest.To(); to != nil {
				l.toURI = to.Address.String()
			}
			if contact := dlg.InviteRequest.Contact(); contact != nil {
				l.remoteURI = contact.Address.String()
			}
		}
	}
	l.mu.Unlock()

	// For outbound legs, start monitoring the dialog context for termination.
	// Inbound legs start monitoring in NewInboundLeg().
	// This ensures the B-leg is destroyed when the remote party sends BYE.
	if dlg != nil && direction == LegDirectionOutbound {
		go l.monitorDialogContext(dlg.Context())
	}
}

// SetSessionID sets the RTP session ID.
func (l *legImpl) SetSessionID(sessionID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sessionID = sessionID
}

// SetMediaEndpoint sets the local media endpoint.
func (l *legImpl) SetMediaEndpoint(addr string, port int, codec string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.localRTPAddr = addr
	l.localRTPPort = port
	l.negotiatedCodec = codec
}

// SetRemoteMediaEndpoint sets the remote media endpoint.
func (l *legImpl) SetRemoteMediaEndpoint(addr string, port int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.remoteRTPAddr = addr
	l.remoteRTPPort = port
}

// SetSIPResponse sets the final SIP response for failed legs.
func (l *legImpl) SetSIPResponse(code int, reason string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sipCode = code
	l.sipReason = reason
}

// SetTerminationCause sets the termination cause.
func (l *legImpl) SetTerminationCause(cause TerminationCause) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.terminationCause = cause
}

// SetOutboundDialogState stores the dialog state needed to send BYE for outbound legs.
// This should be called when the 200 OK is received.
// - remoteContactURI: Contact header from 200 OK (used as Request-URI in BYE)
// - remoteToURI: To header URI from INVITE (used as To header in BYE)
// - localFromURI: From header URI from INVITE (used as From header in BYE)
// - remoteTag: Tag from To header in 200 OK
// - localTag: Our From tag from INVITE
func (l *legImpl) SetOutboundDialogState(remoteContactURI, remoteToURI, localFromURI, remoteTag, localTag string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.remoteContactURI = remoteContactURI
	l.remoteToURI = remoteToURI
	l.localFromURI = localFromURI
	l.remoteTag = remoteTag
	l.localTag = localTag
}

// GetOutboundDialogState returns the dialog state for sending BYE.
func (l *legImpl) GetOutboundDialogState() (remoteContactURI, remoteToURI, localFromURI, remoteTag, localTag string) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.remoteContactURI, l.remoteToURI, l.localFromURI, l.remoteTag, l.localTag
}

// SetTeardownHandler sets the teardown callback.
// This is called before state changes to Destroyed to allow SIP signaling.
func (l *legImpl) SetTeardownHandler(fn func(Leg)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.onTeardown = fn
}

// notifyStateChange invokes registered state change callbacks.
func (l *legImpl) notifyStateChange(old, new LegState) {
	l.callbackMu.Lock()
	// Copy callbacks to slice for iteration (safe to iterate after unlock)
	callbacks := make([]func(old, new LegState), 0, len(l.stateChangeCallbacks))
	for _, fn := range l.stateChangeCallbacks {
		callbacks = append(callbacks, fn)
	}
	l.callbackMu.Unlock()

	for _, fn := range callbacks {
		fn(old, new)
	}
}

// notifyTerminated invokes registered termination callbacks.
func (l *legImpl) notifyTerminated(cause TerminationCause) {
	l.callbackMu.Lock()
	// Copy callbacks to slice for iteration (safe to iterate after unlock)
	callbacks := make([]func(cause TerminationCause), 0, len(l.terminatedCallbacks))
	for _, fn := range l.terminatedCallbacks {
		callbacks = append(callbacks, fn)
	}
	numCallbacks := len(callbacks)
	l.callbackMu.Unlock()

	slog.Debug("[Leg] Notifying termination callbacks",
		"leg_id", l.id,
		"call_id", l.callID,
		"cause", cause.String(),
		"num_callbacks", numCallbacks,
	)

	for i, fn := range callbacks {
		slog.Debug("[Leg] Invoking termination callback",
			"leg_id", l.id,
			"callback_index", i,
		)
		fn(cause)
	}
}

// monitorDialogContext monitors the dialog's context for termination.
// When the dialog context is canceled (e.g., BYE received), the leg is destroyed.
func (l *legImpl) monitorDialogContext(dlgCtx context.Context) {
	select {
	case <-dlgCtx.Done():
		// Dialog was terminated, destroy the leg
		_ = l.Hangup(context.Background(), TerminationCauseRemoteBYE)
	case <-l.done:
		// Leg was destroyed by other means, stop monitoring
		return
	}
}

// Ensure legImpl implements Leg
var _ Leg = (*legImpl)(nil)
