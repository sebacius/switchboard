package b2bua

import (
	"context"
	"log/slog"
	"time"

	"github.com/emiago/sipgo/sip"
	"github.com/sebas/switchboard/internal/signaling/dialog"
)

// callService is the concrete implementation of CallService.
type callService struct {
	cfg        CallServiceConfig
	originator *Originator
}

// NewCallService creates a new CallService instance.
func NewCallService(cfg CallServiceConfig) CallService {
	// Set defaults
	if cfg.DefaultDialTimeout == 0 {
		cfg.DefaultDialTimeout = 30 * time.Second
	}

	origCfg := OriginatorConfig{
		AdvertiseAddr: cfg.AdvertiseAddr,
		Port:          cfg.Port,
		Transport:     cfg.Transport,
		Client:        cfg.Client,
		LocalContact:  cfg.LocalContact,
	}

	return &callService{
		cfg:        cfg,
		originator: NewOriginator(origCfg),
	}
}

// --- Target Resolution ---

func (s *callService) Lookup(ctx context.Context, target string) (*LookupResult, error) {
	if s.cfg.Resolver == nil {
		return nil, &LookupError{
			Target: target,
			Reason: "no resolver configured",
			Cause:  ErrTargetNotFound,
		}
	}

	return s.cfg.Resolver.Resolve(ctx, target)
}

// --- Leg Creation ---

func (s *callService) AdoptInboundLeg(dlg *dialog.Dialog, sessionID string, opts ...LegOption) (Leg, error) {
	return NewInboundLeg(dlg, sessionID, opts...)
}

func (s *callService) CreateOutboundLeg(ctx context.Context, target *LookupResult, opts ...LegOption) (Leg, error) {
	if target == nil || !target.HasContacts() {
		return nil, ErrNoContacts
	}

	// Use the originator to create the leg
	result, err := s.originator.Originate(ctx, OriginateRequest{
		Target:  target,
		Timeout: s.cfg.DefaultDialTimeout,
		Codecs:  []string{"0"}, // Default to PCMU
	})
	if err != nil {
		return nil, err
	}

	if !result.Success {
		return nil, &DialError{
			Target:    target.Original,
			SIPCode:   result.SIPCode,
			SIPReason: result.SIPReason,
			Cause:     result.Error,
		}
	}

	return result.Leg, nil
}

// --- Bridging ---

func (s *callService) CreateBridge(legA, legB Leg, opts ...BridgeOption) (Bridge, error) {
	// Prepend transport option so that bridges can do RTP bridging
	if s.cfg.Transport != nil {
		opts = append([]BridgeOption{WithTransport(s.cfg.Transport)}, opts...)
	}
	return NewBridge(legA, legB, opts...)
}

// --- High-Level Operations ---

func (s *callService) Dial(ctx context.Context, target string, timeout time.Duration, opts ...LegOption) (Leg, error) {
	if timeout == 0 {
		timeout = s.cfg.DefaultDialTimeout
	}

	// Apply options to extract CallerID/CallerName
	var legOpts legOptions
	for _, opt := range opts {
		opt(&legOpts)
	}

	// Step 1: Lookup
	result, err := s.Lookup(ctx, target)
	if err != nil {
		return nil, err
	}

	if !result.HasContacts() {
		return nil, &DialError{
			Target: target,
			Cause:  ErrNoContacts,
		}
	}

	// Step 2: Originate with CallerID from options
	dialCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	origResult, err := s.originator.Originate(dialCtx, OriginateRequest{
		Target:     result,
		Timeout:    timeout,
		Codecs:     []string{"0"},
		CallerID:   legOpts.callerID,
		CallerName: legOpts.callerName,
	})
	if err != nil {
		return nil, err
	}

	if !origResult.Success {
		return nil, &DialError{
			Target:      target,
			ResolvedURI: result.PrimaryContact().URI,
			SIPCode:     origResult.SIPCode,
			SIPReason:   origResult.SIPReason,
			Cause:       origResult.Error,
		}
	}

	// Step 3: Wait for answer
	leg := origResult.Leg
	if err := leg.WaitForState(dialCtx, LegStateAnswered); err != nil {
		// Clean up on failure
		_ = leg.Hangup(context.Background(), TerminationCauseError)
		return nil, &DialError{
			Target:      target,
			ResolvedURI: result.PrimaryContact().URI,
			Cause:       err,
		}
	}

	return leg, nil
}

func (s *callService) DialAndBridge(ctx context.Context, legA Leg, target string, timeout time.Duration, opts ...LegOption) (*BridgeInfo, error) {
	if timeout == 0 {
		timeout = s.cfg.DefaultDialTimeout
	}

	// Verify A leg is answered
	if legA.GetState() != LegStateAnswered {
		return nil, ErrLegNotAnswered
	}

	slog.Info("[CallService] DialAndBridge starting",
		"leg_a", legA.ID(),
		"target", target,
		"timeout", timeout,
	)

	// Step 1: Dial target (pass through options for CallerID, etc.)
	legB, err := s.Dial(ctx, target, timeout, opts...)
	if err != nil {
		return nil, err
	}

	slog.Info("[CallService] B leg answered",
		"leg_a", legA.ID(),
		"leg_b", legB.ID(),
	)

	// Step 2: Create bridge
	bridge, err := s.CreateBridge(legA, legB, WithAutoHangup(true))
	if err != nil {
		_ = legB.Hangup(ctx, TerminationCauseError)
		return nil, err
	}

	// Step 3: Start bridge
	if err := bridge.Start(ctx); err != nil {
		_ = legB.Hangup(ctx, TerminationCauseError)
		return nil, err
	}

	slog.Info("[CallService] Bridge active",
		"bridge_id", bridge.ID(),
		"leg_a", legA.ID(),
		"leg_b", legB.ID(),
	)

	// Step 4: Wait for bridge to terminate
	// Use the A-leg's context for bridge wait, NOT the dial timeout context.
	// The dial timeout (ctx) should only apply to the dial phase.
	// Once bridged, the call should stay up until either leg hangs up.
	// The A-leg's context is tied to its dialog lifecycle and will be
	// canceled when the A-leg receives BYE or terminates.
	bridgeCtx := legA.Context()
	_, err = bridge.WaitForTermination(bridgeCtx)
	if err != nil && bridgeCtx.Err() != nil {
		// A-leg context was canceled (A-leg hung up or dialplan ended)
		_ = bridge.Stop(true)
	}

	slog.Info("[CallService] Bridge terminated",
		"bridge_id", bridge.ID(),
	)

	return bridge.Info(), nil
}

// --- Ring Group Support (Future) ---

func (s *callService) DialParallel(ctx context.Context, targets []*LookupResult, timeout time.Duration, opts ...LegOption) (Leg, error) {
	return nil, ErrNotImplemented
}

// --- B-leg BYE Handling ---

// HandleIncomingBYE delegates to the originator to handle BYE for outbound legs.
func (s *callService) HandleIncomingBYE(req *sip.Request, tx sip.ServerTransaction) bool {
	return s.originator.HandleIncomingBYE(req, tx)
}

// Ensure callService implements CallService
var _ CallService = (*callService)(nil)
