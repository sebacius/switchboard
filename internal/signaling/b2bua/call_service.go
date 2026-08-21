package b2bua

import (
	"context"
	"log/slog"
	"sync"
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
		DialogManager: cfg.DialogManager,
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
		Target:        result,
		Timeout:       timeout,
		Codecs:        []string{"0"},
		CallerID:      legOpts.callerID,
		CallerName:    legOpts.callerName,
		ALegSessionID: legOpts.aLegSessionID,
		ALegCallID:    legOpts.aLegCallID,
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
		"leg_a_session", legA.SessionID(),
		"target", target,
		"timeout", timeout,
	)

	// Step 1: Dial target (pass through options for CallerID, etc.)
	// Prepend A-leg session ID and Call-ID so B-leg:
	// - Is created on the same RTP manager (for bridging)
	// - Can be looked up by BridgeMapper (for drain migration)
	opts = append([]LegOption{
		WithALegSessionID(legA.SessionID()),
		WithALegCallID(legA.CallID()),
	}, opts...)
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

// --- Ring Group Support ---

// DialParallel originates to every target at once and returns the first leg to
// answer, canceling the rest. It is the ring-group primitive: one round of a
// group is one DialParallel call, so a single-member round is just the
// degenerate case of the same code path.
//
// Losing legs are torn down two ways on purpose. Canceling the shared dial
// context aborts anything still ringing, and any leg that answers inside the
// race window — after a winner was picked but before its context died — is hung
// up explicitly. Without the second path a caller could be left connected to a
// phone nobody is on.
func (s *callService) DialParallel(ctx context.Context, targets []*LookupResult, timeout time.Duration, opts ...LegOption) (Leg, error) {
	if len(targets) == 0 {
		return nil, &DialError{Cause: ErrNoContacts}
	}
	if timeout == 0 {
		timeout = s.cfg.DefaultDialTimeout
	}

	var legOpts legOptions
	for _, opt := range opts {
		opt(&legOpts)
	}

	// One shared deadline for the whole round: the group's per-member timeout is
	// what bounds how long a caller waits before the next round starts.
	dialCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	type outcome struct {
		leg    Leg
		err    error
		target string
	}
	results := make(chan outcome, len(targets))

	var wg sync.WaitGroup
	for _, target := range targets {
		if target == nil || !target.HasContacts() {
			name := ""
			if target != nil {
				name = target.Original
			}
			results <- outcome{err: &DialError{Target: name, Cause: ErrNoContacts}, target: name}
			continue
		}

		wg.Add(1)
		go func(target *LookupResult) {
			defer wg.Done()

			origResult, err := s.originator.Originate(dialCtx, OriginateRequest{
				Target:        target,
				Timeout:       timeout,
				Codecs:        []string{"0"},
				CallerID:      legOpts.callerID,
				CallerName:    legOpts.callerName,
				ALegSessionID: legOpts.aLegSessionID,
				ALegCallID:    legOpts.aLegCallID,
			})
			if err != nil {
				results <- outcome{err: err, target: target.Original}
				return
			}
			if !origResult.Success {
				results <- outcome{
					err: &DialError{
						Target:      target.Original,
						ResolvedURI: target.PrimaryContact().URI,
						SIPCode:     origResult.SIPCode,
						SIPReason:   origResult.SIPReason,
						Cause:       origResult.Error,
					},
					target: target.Original,
				}
				return
			}

			leg := origResult.Leg
			if err := leg.WaitForState(dialCtx, LegStateAnswered); err != nil {
				_ = leg.Hangup(context.Background(), TerminationCauseCancel)
				results <- outcome{err: err, target: target.Original}
				return
			}
			results <- outcome{leg: leg, target: target.Original}
		}(target)
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	var winner Leg
	var winnerTarget string
	var firstErr error
	for r := range results {
		if r.leg == nil {
			if firstErr == nil {
				firstErr = r.err
			}
			continue
		}
		if winner == nil {
			winner = r.leg
			winnerTarget = r.target
			// Stop every other leg ringing. The winner survives this: like Dial,
			// the leg's lifetime is its own dialog context, not the dial context.
			cancel()
			continue
		}
		// Answered in the race window after a winner was already chosen.
		slog.Info("[CallService] DialParallel canceling late answer", "target", r.target, "winner", winnerTarget)
		_ = r.leg.Hangup(context.Background(), TerminationCauseNormal)
	}

	if winner == nil {
		if firstErr == nil {
			firstErr = ErrDialTimeout
		}
		return nil, firstErr
	}

	slog.Info("[CallService] DialParallel answered", "winner", winnerTarget, "candidates", len(targets))
	return winner, nil
}

// --- B-leg BYE Handling ---

// HandleIncomingBYE delegates to the originator to handle BYE for outbound legs.
func (s *callService) HandleIncomingBYE(req *sip.Request, tx sip.ServerTransaction) bool {
	return s.originator.HandleIncomingBYE(req, tx)
}

// GetBridgeMapper returns the originator as a BridgeMapper for drain migration.
func (s *callService) GetBridgeMapper() BridgeMapper {
	return s.originator
}

// Ensure callService implements CallService
var _ CallService = (*callService)(nil)
