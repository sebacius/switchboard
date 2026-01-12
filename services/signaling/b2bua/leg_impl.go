package b2bua

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/sebas/switchboard/services/signaling/dialog"
)

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

	// Lifecycle
	ctx    context.Context
	cancel context.CancelFunc

	// Callbacks
	stateChangeCallbacks  []func(old, new LegState)
	terminatedCallbacks   []func(cause TerminationCause)
	stateChangeCallbackMu sync.Mutex
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

	id := options.id
	if id == "" {
		id = "leg-" + uuid.New().String()
	}

	ctx, cancel := context.WithCancel(context.Background())

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
		id:         id,
		callID:     dlg.CallID,
		direction:  LegDirectionInbound,
		state:      initialState,
		dialog:     dlg,
		sessionID:  sessionID,
		createdAt:  now,
		ringingAt:  now,
		answeredAt: answeredAt,
		ctx:        ctx,
		cancel:     cancel,
	}

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

	id := options.id
	if id == "" {
		id = "leg-" + uuid.New().String()
	}

	ctx, cancel := context.WithCancel(context.Background())

	leg := &legImpl{
		id:        id,
		callID:    callID,
		direction: LegDirectionOutbound,
		state:     LegStateCreated,
		toURI:     targetURI,
		createdAt: time.Now(),
		ctx:       ctx,
		cancel:    cancel,
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
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for {
		l.mu.RLock()
		current := l.state
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

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-l.ctx.Done():
			return ErrLegTerminated
		case <-ticker.C:
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

func (l *legImpl) Context() context.Context {
	return l.ctx
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
	l.mu.Lock()
	if l.state.IsTerminal() {
		l.mu.Unlock()
		return nil // Already terminated, safe to call multiple times
	}

	oldState := l.state
	l.state = LegStateDestroyed
	l.terminationCause = cause
	l.terminatedAt = time.Now()
	l.mu.Unlock()

	// Cancel context to signal goroutines
	l.cancel()

	l.notifyStateChange(oldState, LegStateDestroyed)
	l.notifyTerminated(cause)

	return nil
}

func (l *legImpl) Destroy() {
	l.Hangup(context.Background(), TerminationCauseNormal)
}

// --- Event Callbacks ---

func (l *legImpl) OnStateChange(fn func(old, new LegState)) func() {
	l.stateChangeCallbackMu.Lock()
	l.stateChangeCallbacks = append(l.stateChangeCallbacks, fn)
	idx := len(l.stateChangeCallbacks) - 1
	l.stateChangeCallbackMu.Unlock()

	return func() {
		l.stateChangeCallbackMu.Lock()
		if idx < len(l.stateChangeCallbacks) {
			l.stateChangeCallbacks = append(
				l.stateChangeCallbacks[:idx],
				l.stateChangeCallbacks[idx+1:]...,
			)
		}
		l.stateChangeCallbackMu.Unlock()
	}
}

func (l *legImpl) OnTerminated(fn func(cause TerminationCause)) {
	l.stateChangeCallbackMu.Lock()
	l.terminatedCallbacks = append(l.terminatedCallbacks, fn)
	l.stateChangeCallbackMu.Unlock()
}

// --- Internal Methods ---

// TransitionTo transitions the leg to a new state.
func (l *legImpl) TransitionTo(newState LegState) error {
	l.mu.Lock()
	defer l.mu.Unlock()

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

	l.notifyStateChange(oldState, newState)
	return nil
}

// SetDialog sets the SIP dialog for this leg.
func (l *legImpl) SetDialog(dlg *dialog.Dialog) {
	l.mu.Lock()
	defer l.mu.Unlock()
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

// notifyStateChange invokes registered state change callbacks.
func (l *legImpl) notifyStateChange(old, new LegState) {
	l.stateChangeCallbackMu.Lock()
	callbacks := make([]func(old, new LegState), len(l.stateChangeCallbacks))
	copy(callbacks, l.stateChangeCallbacks)
	l.stateChangeCallbackMu.Unlock()

	for _, fn := range callbacks {
		fn(old, new)
	}
}

// notifyTerminated invokes registered termination callbacks.
func (l *legImpl) notifyTerminated(cause TerminationCause) {
	l.stateChangeCallbackMu.Lock()
	callbacks := make([]func(cause TerminationCause), len(l.terminatedCallbacks))
	copy(callbacks, l.terminatedCallbacks)
	l.stateChangeCallbackMu.Unlock()

	for _, fn := range callbacks {
		fn(cause)
	}
}

// Ensure legImpl implements Leg
var _ Leg = (*legImpl)(nil)
