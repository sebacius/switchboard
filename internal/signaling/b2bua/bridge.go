package b2bua

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/sebas/switchboard/internal/signaling/mediaclient"
)

// Bridge connects two call legs for bidirectional media exchange.
//
// A Bridge is created after both legs reach Answered state.
// It coordinates:
//   - RTP forwarding between the two media sessions
//   - Lifecycle monitoring (terminates if either leg hangs up)
//   - Optional features: recording, DTMF relay, etc.
//
// Thread Safety: All methods are safe for concurrent use.
type Bridge interface {
	// ID returns the unique identifier for this bridge.
	ID() string

	// LegA returns the inbound (A) leg.
	LegA() Leg

	// LegB returns the outbound (B) leg.
	LegB() Leg

	// GetState returns the current state of the bridge.
	GetState() BridgeState

	// Info returns detailed information about the bridge.
	Info() *BridgeInfo

	// --- Lifecycle Operations ---

	// Start activates media bridging between the two legs.
	// Both legs must be in Answered state.
	// Transitions bridge from Created to Active.
	// Returns error if either leg is not answered or bridge already active.
	Start(ctx context.Context) error

	// Stop terminates the bridge and optionally hangs up both legs.
	// If hangupLegs is true, sends BYE on both legs.
	// If hangupLegs is false, legs remain connected (for transfer scenarios).
	// Transitions bridge to Terminated.
	// Safe to call multiple times.
	Stop(hangupLegs bool) error

	// WaitForTermination blocks until the bridge terminates.
	// Returns the cause of termination.
	// Returns immediately if already terminated.
	WaitForTermination(ctx context.Context) (TerminationCause, error)

	// --- Event Callbacks ---

	// OnTerminated registers a callback for bridge termination.
	// Called once when bridge reaches Terminated state.
	OnTerminated(fn func(cause TerminationCause))
}

// BridgeInfo contains detailed information about a bridge.
type BridgeInfo struct {
	// Identity
	ID string `json:"id"`

	// Legs
	LegAID string `json:"leg_a_id"`
	LegBID string `json:"leg_b_id"`

	// State
	State            BridgeState      `json:"state"`
	TerminationCause TerminationCause `json:"termination_cause,omitempty"`
	TerminatedBy     string           `json:"terminated_by,omitempty"` // "leg_a", "leg_b", or "local"

	// Media
	Codec              string `json:"codec,omitempty"`
	TranscodingEnabled bool   `json:"transcoding_enabled,omitempty"`

	// Timing
	CreatedAt    time.Time `json:"created_at"`
	StartedAt    time.Time `json:"started_at,omitempty"` // When Start() was called
	TerminatedAt time.Time `json:"terminated_at,omitempty"`

	// Statistics (populated if RTP Manager provides them)
	PacketsA2B int64 `json:"packets_a_to_b,omitempty"` // Packets forwarded A -> B
	PacketsB2A int64 `json:"packets_b_to_a,omitempty"` // Packets forwarded B -> A
	BytesA2B   int64 `json:"bytes_a_to_b,omitempty"`
	BytesB2A   int64 `json:"bytes_b_to_a,omitempty"`
}

// Duration returns the total bridge duration (start to termination).
// Returns 0 if not yet terminated.
func (i *BridgeInfo) Duration() time.Duration {
	if i.StartedAt.IsZero() || i.TerminatedAt.IsZero() {
		return 0
	}
	return i.TerminatedAt.Sub(i.StartedAt)
}

// BridgeOption configures bridge creation.
type BridgeOption func(*bridgeOptions)

type bridgeOptions struct {
	autoHangup bool
	transport  mediaclient.Transport
}

// WithAutoHangup configures whether legs should be hung up on termination.
// Default is true.
func WithAutoHangup(enable bool) BridgeOption {
	return func(o *bridgeOptions) {
		o.autoHangup = enable
	}
}

// WithTransport sets the RTP Manager transport for media bridging.
// Required for the bridge to establish RTP relay between sessions.
func WithTransport(t mediaclient.Transport) BridgeOption {
	return func(o *bridgeOptions) {
		o.transport = t
	}
}

// bridgeImpl is the concrete implementation of the Bridge interface.
type bridgeImpl struct {
	mu sync.RWMutex

	// Identity
	id string

	// Legs
	legA Leg
	legB Leg

	// State
	state            BridgeState
	terminationCause TerminationCause
	terminatedBy     string // "leg_a", "leg_b", or "local"

	// Media
	codec              string
	transcodingEnabled bool
	transport          mediaclient.Transport // RTP Manager transport for media bridging
	mediaBridgeID      string                // RTP Manager bridge ID

	// Timing
	createdAt    time.Time
	startedAt    time.Time
	terminatedAt time.Time

	// Statistics
	packetsA2B int64
	packetsB2A int64
	bytesA2B   int64
	bytesB2A   int64

	// Lifecycle - Using done channel pattern instead of storing context
	done      chan struct{}
	closeOnce sync.Once

	// Options
	autoHangup bool

	// Callbacks - using map with unique IDs to fix unregister bug
	terminatedCallbacks  map[uint64]func(cause TerminationCause)
	callbackMu           sync.Mutex
	callbackIDCounter    atomic.Uint64
	terminationWaiters   chan struct{}
	terminationWaitersMu sync.Mutex
}

// NewBridge creates a new bridge between two legs.
func NewBridge(legA, legB Leg, opts ...BridgeOption) (Bridge, error) {
	if legA == nil || legB == nil {
		return nil, ErrInvalidState
	}

	options := &bridgeOptions{
		autoHangup: true, // Default: hangup legs when bridge terminates
	}
	for _, opt := range opts {
		opt(options)
	}

	id := "bridge-" + uuid.New().String()

	bridge := &bridgeImpl{
		id:                  id,
		legA:                legA,
		legB:                legB,
		state:               BridgeStateCreated,
		createdAt:           time.Now(),
		autoHangup:          options.autoHangup,
		transport:           options.transport,
		done:                make(chan struct{}),
		terminatedCallbacks: make(map[uint64]func(cause TerminationCause)),
		terminationWaiters:  make(chan struct{}),
	}

	// Register leg termination monitoring immediately at creation time.
	// This ensures we catch termination even if Start() hasn't been called yet,
	// preventing race conditions where a leg terminates before callbacks are registered.
	slog.Info("[Bridge] Registering leg termination callbacks",
		"bridge_id", id,
		"leg_a_id", legA.ID(),
		"leg_a_call_id", legA.CallID(),
		"leg_b_id", legB.ID(),
		"leg_b_call_id", legB.CallID(),
	)
	legA.OnTerminated(func(cause TerminationCause) {
		slog.Info("[Bridge] A-leg termination callback invoked",
			"bridge_id", bridge.id,
			"cause", cause.String(),
		)
		bridge.handleLegTerminated("leg_a", cause)
	})
	legB.OnTerminated(func(cause TerminationCause) {
		slog.Info("[Bridge] B-leg termination callback invoked",
			"bridge_id", bridge.id,
			"cause", cause.String(),
		)
		bridge.handleLegTerminated("leg_b", cause)
	})

	return bridge, nil
}

// --- Identity Methods ---

func (b *bridgeImpl) ID() string {
	return b.id
}

func (b *bridgeImpl) LegA() Leg {
	return b.legA
}

func (b *bridgeImpl) LegB() Leg {
	return b.legB
}

// --- State Methods ---
func (b *bridgeImpl) GetState() BridgeState {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.state
}

// --- Info Method ---
func (b *bridgeImpl) Info() *BridgeInfo {
	b.mu.RLock()
	defer b.mu.RUnlock()

	return &BridgeInfo{
		ID:                 b.id,
		LegAID:             b.legA.ID(),
		LegBID:             b.legB.ID(),
		State:              b.state,
		TerminationCause:   b.terminationCause,
		TerminatedBy:       b.terminatedBy,
		Codec:              b.codec,
		TranscodingEnabled: b.transcodingEnabled,
		CreatedAt:          b.createdAt,
		StartedAt:          b.startedAt,
		TerminatedAt:       b.terminatedAt,
		PacketsA2B:         b.packetsA2B,
		PacketsB2A:         b.packetsB2A,
		BytesA2B:           b.bytesA2B,
		BytesB2A:           b.bytesB2A,
	}
}

// --- Lifecycle Operations ---
func (b *bridgeImpl) Start(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.state != BridgeStateCreated {
		return ErrBridgeActive
	}

	// Verify both legs are answered
	if b.legA.GetState() != LegStateAnswered {
		return ErrLegNotAnswered
	}
	if b.legB.GetState() != LegStateAnswered {
		return ErrLegNotAnswered
	}

	// Get session IDs from both legs
	sessionAID := b.legA.SessionID()
	sessionBID := b.legB.SessionID()

	// Bridge media at RTP Manager level if transport is configured
	if b.transport != nil && sessionAID != "" && sessionBID != "" {
		bridgeID, err := b.transport.BridgeMedia(ctx, sessionAID, sessionBID)
		if err != nil {
			slog.Error("[Bridge] Failed to bridge media",
				"bridge_id", b.id,
				"session_a", sessionAID,
				"session_b", sessionBID,
				"error", err,
			)
			return fmt.Errorf("bridge media: %w", err)
		}
		b.mediaBridgeID = bridgeID
		slog.Info("[Bridge] Media bridged",
			"bridge_id", b.id,
			"media_bridge_id", bridgeID,
			"session_a", sessionAID,
			"session_b", sessionBID,
		)
	} else if b.transport == nil {
		slog.Warn("[Bridge] No transport configured - media bridging skipped",
			"bridge_id", b.id,
		)
	}

	b.state = BridgeStateActive
	b.startedAt = time.Now()

	// Note: Leg termination monitoring is set up in NewBridge() to avoid race conditions
	// where a leg terminates before Start() is called.

	slog.Info("[Bridge] Started",
		"bridge_id", b.id,
		"leg_a", b.legA.ID(),
		"leg_b", b.legB.ID(),
	)

	return nil
}

func (b *bridgeImpl) Stop(hangupLegs bool) error {
	b.mu.Lock()
	if b.state == BridgeStateTerminated {
		b.mu.Unlock()
		return nil // Already terminated
	}

	b.state = BridgeStateTerminating
	mediaBridgeID := b.mediaBridgeID
	transport := b.transport
	b.mu.Unlock()

	// Unbridge media at RTP Manager level
	if transport != nil && mediaBridgeID != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := transport.UnbridgeMedia(ctx, mediaBridgeID); err != nil {
			slog.Warn("[Bridge] Failed to unbridge media",
				"bridge_id", b.id,
				"media_bridge_id", mediaBridgeID,
				"error", err,
			)
			// Continue with teardown even if unbridging fails
		} else {
			slog.Info("[Bridge] Media unbridged",
				"bridge_id", b.id,
				"media_bridge_id", mediaBridgeID,
			)
		}
		cancel()
	}

	// Hangup legs if requested
	if hangupLegs {
		ctx := context.Background()
		legAState := b.legA.GetState()
		legBState := b.legB.GetState()
		slog.Info("[Bridge] Stop() checking leg states for hangup",
			"bridge_id", b.id,
			"leg_a_id", b.legA.ID(),
			"leg_a_state", legAState.String(),
			"leg_b_id", b.legB.ID(),
			"leg_b_state", legBState.String(),
			"auto_hangup", hangupLegs,
		)
		if legAState == LegStateAnswered {
			slog.Info("[Bridge] Hanging up A-leg",
				"bridge_id", b.id,
				"leg_a_id", b.legA.ID(),
			)
			if err := b.legA.Hangup(ctx, TerminationCauseBridgePeer); err != nil {
				slog.Warn("[Bridge] A-leg hangup returned error",
					"bridge_id", b.id,
					"error", err,
				)
			}
		} else {
			slog.Debug("[Bridge] Skipping A-leg hangup - not in Answered state",
				"bridge_id", b.id,
				"leg_a_state", legAState.String(),
			)
		}
		if legBState == LegStateAnswered {
			slog.Info("[Bridge] Hanging up B-leg",
				"bridge_id", b.id,
				"leg_b_id", b.legB.ID(),
			)
			if err := b.legB.Hangup(ctx, TerminationCauseBridgePeer); err != nil {
				slog.Warn("[Bridge] B-leg hangup returned error",
					"bridge_id", b.id,
					"error", err,
				)
			}
		} else {
			slog.Debug("[Bridge] Skipping B-leg hangup - not in Answered state",
				"bridge_id", b.id,
				"leg_b_state", legBState.String(),
			)
		}
	} else {
		slog.Debug("[Bridge] Stop() skipping hangup - auto_hangup disabled",
			"bridge_id", b.id,
		)
	}

	b.mu.Lock()
	b.state = BridgeStateTerminated
	b.terminatedAt = time.Now()
	if b.terminatedBy == "" {
		b.terminatedBy = "local"
	}
	if b.terminationCause == TerminationCauseNone {
		b.terminationCause = TerminationCauseNormal
	}
	cause := b.terminationCause
	b.mu.Unlock()

	// Close done channel to signal goroutines (only once)
	b.closeOnce.Do(func() {
		close(b.done)
	})

	// Notify waiters
	b.terminationWaitersMu.Lock()
	close(b.terminationWaiters)
	b.terminationWaiters = make(chan struct{}) // Reset for next use
	b.terminationWaitersMu.Unlock()

	b.notifyTerminated(cause)

	slog.Info("[Bridge] Stopped",
		"bridge_id", b.id,
		"cause", cause.String(),
		"terminated_by", b.terminatedBy,
	)

	return nil
}

func (b *bridgeImpl) WaitForTermination(ctx context.Context) (TerminationCause, error) {
	b.mu.RLock()
	if b.state == BridgeStateTerminated {
		cause := b.terminationCause
		b.mu.RUnlock()
		return cause, nil
	}
	b.mu.RUnlock()

	b.terminationWaitersMu.Lock()
	waitCh := b.terminationWaiters
	b.terminationWaitersMu.Unlock()

	select {
	case <-ctx.Done():
		return TerminationCauseNone, ctx.Err()
	case <-waitCh:
		b.mu.RLock()
		cause := b.terminationCause
		b.mu.RUnlock()
		return cause, nil
	}
}

// --- Event Callbacks ---

// OnTerminated registers a callback for termination.
// Uses map-based registration with unique IDs to fix the bug where
// unregistering a callback could cause index issues.
func (b *bridgeImpl) OnTerminated(fn func(cause TerminationCause)) {
	id := b.callbackIDCounter.Add(1)

	b.callbackMu.Lock()
	b.terminatedCallbacks[id] = fn
	b.callbackMu.Unlock()
}

// --- Internal Methods ---

func (b *bridgeImpl) handleLegTerminated(legName string, cause TerminationCause) {
	slog.Debug("[Bridge] handleLegTerminated called",
		"bridge_id", b.id,
		"leg_name", legName,
		"cause", cause.String(),
	)

	b.mu.Lock()
	if b.state == BridgeStateTerminated || b.state == BridgeStateTerminating {
		slog.Debug("[Bridge] handleLegTerminated skipping - already terminating",
			"bridge_id", b.id,
			"state", b.state.String(),
		)
		b.mu.Unlock()
		return
	}

	// Set state to Terminating immediately under lock to prevent race
	// where both legs terminate simultaneously and both try to Stop()
	b.state = BridgeStateTerminating
	b.terminatedBy = legName
	b.terminationCause = TerminationCauseBridgePeer
	b.mu.Unlock()

	slog.Info("[Bridge] Leg terminated",
		"bridge_id", b.id,
		"leg", legName,
		"cause", cause.String(),
	)

	// Stop the bridge (which will hangup the other leg if autoHangup is true)
	_ = b.Stop(b.autoHangup)
}

func (b *bridgeImpl) notifyTerminated(cause TerminationCause) {
	b.callbackMu.Lock()
	// Copy callbacks to slice for iteration (safe to iterate after unlock)
	callbacks := make([]func(cause TerminationCause), 0, len(b.terminatedCallbacks))
	for _, fn := range b.terminatedCallbacks {
		callbacks = append(callbacks, fn)
	}
	b.callbackMu.Unlock()

	for _, fn := range callbacks {
		fn(cause)
	}
}

// Ensure bridgeImpl implements Bridge
var _ Bridge = (*bridgeImpl)(nil)
