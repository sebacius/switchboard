package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/emiago/sipgo/sip"
	"github.com/sebas/switchboard/internal/signaling/b2bua"
	"github.com/sebas/switchboard/internal/signaling/dialog"
)

// This file implements the answer model (design #7): the INVITE handler never
// answers, and which path a call takes depends on what the flow reaches first.
//
//	dial before any media node → Forward: 180 upstream, dial the target, relay
//	                             its 200 (or its failure code); we never answer
//	                             for it
//	a node that plays media    → Answer: 200 OK with our SDP, we own the media
//
// The invariant is "answering means this leg's media is ours to drive".
// A direct extension call is therefore a pure forward — the caller hears real
// ringing from the endpoint, and if nobody picks up the caller gets the
// endpoint's own 486/480 rather than anything we made up.

// forwardRingTimeout bounds how long Forward waits for the target to answer
// before giving up and relaying a failure. It is deliberately shorter than SIP
// Timer B so the caller gets a real final response rather than a transaction
// timeout.
const forwardRingTimeout = 45 * time.Second

// MarkRinging records that a 180 has already been sent for this leg, so a later
// Forward does not send a duplicate. The INVITE handler calls this after ringing
// the caller up front to hold the transaction open across a slow first turn.
func (s *sessionImpl) MarkRinging() {
	s.mu.Lock()
	s.ringingSent = true
	s.mu.Unlock()
}

// ringOnce sends 180 Ringing unless one has already gone out. It returns
// silently when there is nothing to do, so callers need no bookkeeping.
func (s *sessionImpl) ringOnce() {
	s.mu.Lock()
	already := s.ringingSent
	s.ringingSent = true
	s.mu.Unlock()

	if already || s.dialogMgr == nil {
		return
	}
	if err := s.dialogMgr.SendRinging(s.dialog); err != nil {
		s.logger.Warn("[Session] Failed to send 180 Ringing", "call_id", s.callID, "error", err)
	}
}

// HasAnswered reports whether the 200 OK has been sent for this leg.
func (s *sessionImpl) HasAnswered() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.answered
}

// Answer sends the 200 OK with the held-back SDP, taking media ownership.
// It is idempotent — a media node calls it before playing anything, and the
// forward path calls it once the outbound leg answers — so a second call is a
// no-op. Answering a terminated call is an error, not a silent success: the
// caller is gone and the media would have nowhere to go.
func (s *sessionImpl) Answer(_ context.Context) error {
	s.mu.Lock()
	if s.answered {
		s.mu.Unlock()
		return nil
	}
	if s.terminated {
		s.mu.Unlock()
		return fmt.Errorf("answer: call already terminated")
	}
	sdp := s.sdpBody
	// Mark answered BEFORE releasing the lock so a concurrent Answer cannot also
	// send a 200 OK. If SendOK then fails we roll the flag back below.
	s.answered = true
	s.mu.Unlock()

	if s.dialogMgr == nil {
		s.setAnswered(false)
		return fmt.Errorf("answer: no dialog manager")
	}
	if err := s.dialogMgr.SendOK(s.dialog, sdp); err != nil {
		s.setAnswered(false)
		return fmt.Errorf("answer: %w", err)
	}

	s.logger.Info("[Session] Answered (we own the media)", "call_id", s.callID)
	return nil
}

// setAnswered rolls the answered flag back when SendOK failed.
func (s *sessionImpl) setAnswered(v bool) {
	s.mu.Lock()
	s.answered = v
	s.mu.Unlock()
}

// Forward is the pre-answer routing path, relaying the target's own final status
// upstream when the dial fails. It is the right primitive for a LAST attempt —
// the caller deserves the callee's real 486 rather than an apology — and the
// wrong one inside a flow, where the graph decides what happens next.
//
// See ForwardOutcome for the non-relaying form.
func (s *sessionImpl) Forward(ctx context.Context, target string, timeout time.Duration) error {
	outcome, err := s.forward(ctx, target, timeout)
	if err != nil {
		return err
	}
	if outcome.Answered() {
		return nil
	}
	return s.relayForwardFailure(target, outcome.Err)
}

// ForwardOutcome is Forward without the relay: it reports what happened and
// sends the caller NOTHING, so a flow can route busy somewhere different from
// no-answer.
//
// This distinction is load-bearing. Once a 486 reaches the caller their call is
// over, so a node wiring "no_answer" to a colleague is impossible if the dial
// already answered on its behalf. Relaying becomes the graph's decision, made by
// whatever node the failure exit leads to.
func (s *sessionImpl) ForwardOutcome(ctx context.Context, target string, timeout time.Duration) (DialOutcome, error) {
	return s.forward(ctx, target, timeout)
}

// forward performs the dial and reports the outcome. The returned error is
// reserved for "this could not be attempted at all" — a missing B2BUA, or a
// forward after the call was already answered — as opposed to a dial that was
// attempted and did not connect, which is an outcome rather than an error.
func (s *sessionImpl) forward(ctx context.Context, target string, timeout time.Duration) (DialOutcome, error) {
	if s.callService == nil {
		return DialOutcome{}, &DialError{Target: target, Cause: fmt.Errorf("B2BUA CallService not configured"), SIPCode: 501}
	}
	if s.HasAnswered() {
		// Programming error: the caller should have chosen Dial. Fail loudly
		// rather than sending a second 200 OK.
		return DialOutcome{}, &DialError{Target: target, Cause: fmt.Errorf("forward after answer")}
	}
	if timeout <= 0 {
		timeout = forwardRingTimeout
	}

	s.logger.Info("[Session] Forwarding (pre-answer)", "call_id", s.callID, "target", target, "timeout", timeout)

	// Relay 180 upstream so the caller hears ringback while we dial — unless the
	// INVITE handler already rang to hold the transaction open, in which case a
	// second 180 would be redundant. A failure here is not fatal.
	s.ringOnce()

	callerName := s.callerName
	if callerName == "" {
		callerName = s.callerID
	}

	// Dial blocks until the target answers, rejects, or the timeout expires.
	// Pass the A-leg session/Call-ID so the B-leg lands on the same RTP manager
	// and the drain BridgeMapper can find it.
	bLeg, err := s.callService.Dial(ctx, target, timeout,
		b2bua.WithALegSessionID(s.sessionID),
		b2bua.WithALegCallID(s.callID),
		b2bua.WithCallerID(s.callerID),
		b2bua.WithCallerName(callerName),
	)
	if err != nil {
		return classifyDialError(target, err), nil
	}

	if err := s.completeForward(ctx, bLeg, target); err != nil {
		return classifyDialError(target, err), nil
	}
	return DialOutcome{Result: DialAnswered, Target: target}, nil
}

// completeForward is everything that happens once some outbound leg has
// answered: relay the 200 upstream, adopt the A-leg, bridge, and block until a
// leg hangs up. Forward and ForwardGroup share it because the difference between
// them is entirely in HOW a leg is won, not in what happens afterwards.
func (s *sessionImpl) completeForward(ctx context.Context, bLeg b2bua.Leg, target string) error {
	// Retain the leg so teardown can cancel it if the caller vanishes between
	// here and the bridge being established.
	s.setBLeg(bLeg)

	// The target answered: now — and only now — do we answer upstream. This is
	// the "relay its 200" step; we still never answer on our own behalf here.
	if err := s.Answer(ctx); err != nil {
		_ = bLeg.Hangup(context.Background(), b2bua.TerminationCauseError)
		s.setBLeg(nil)
		return &DialError{Target: target, Cause: fmt.Errorf("relay 200 OK: %w", err)}
	}

	// Both legs are up: adopt the A-leg and bridge media.
	aLeg, err := s.adoptALeg()
	if err != nil {
		_ = bLeg.Hangup(context.Background(), b2bua.TerminationCauseError)
		s.setBLeg(nil)
		return &DialError{Target: target, Cause: fmt.Errorf("adopt inbound leg: %w", err)}
	}

	bridge, err := s.callService.CreateBridge(aLeg, bLeg, b2bua.WithAutoHangup(true))
	if err != nil {
		_ = bLeg.Hangup(context.Background(), b2bua.TerminationCauseError)
		s.setBLeg(nil)
		return &DialError{Target: target, Cause: fmt.Errorf("create bridge: %w", err)}
	}
	if err := bridge.Start(ctx); err != nil {
		_ = bLeg.Hangup(context.Background(), b2bua.TerminationCauseError)
		s.setBLeg(nil)
		return &DialError{Target: target, Cause: fmt.Errorf("start bridge: %w", err)}
	}

	s.logger.Info("[Session] Forward bridged", "call_id", s.callID, "bridge_id", bridge.ID(), "target", target)

	// Wait on the A-leg's own scope, not the dial timeout: once bridged the call
	// lives until a leg hangs up.
	bridgeCtx := aLeg.Context()
	if _, err := bridge.WaitForTermination(bridgeCtx); err != nil && bridgeCtx.Err() != nil {
		_ = bridge.Stop(true)
	}
	s.setBLeg(nil)

	s.logger.Info("[Session] Forward bridge terminated", "call_id", s.callID, "bridge_id", bridge.ID())
	return nil
}

// ForwardGroup rings a group and relays the outcome upstream if nobody answers.
// Like Forward, it is the right primitive for a last attempt and the wrong one
// inside a flow; see ForwardGroupOutcome.
func (s *sessionImpl) ForwardGroup(ctx context.Context, rounds [][]string, memberTimeout time.Duration) error {
	outcome, err := s.forwardGroup(ctx, rounds, memberTimeout)
	if err != nil {
		return err
	}
	if outcome.Answered() {
		return nil
	}
	if outcome.Result == DialNoAnswer {
		// Preserved for callers that branch on it.
		return ErrGroupNoAnswer
	}
	return outcome.Error()
}

// ForwardGroupOutcome rings a group and reports what happened, relaying nothing.
// The per-member detail is what lets a call record say "all four were busy"
// rather than the far less useful "nobody answered".
func (s *sessionImpl) ForwardGroupOutcome(ctx context.Context, rounds [][]string, memberTimeout time.Duration) (GroupOutcome, error) {
	return s.forwardGroup(ctx, rounds, memberTimeout)
}

func (s *sessionImpl) forwardGroup(ctx context.Context, rounds [][]string, memberTimeout time.Duration) (GroupOutcome, error) {
	if s.callService == nil {
		return GroupOutcome{}, &DialError{Cause: fmt.Errorf("B2BUA CallService not configured"), SIPCode: 501}
	}
	if s.HasAnswered() {
		return GroupOutcome{}, &DialError{Cause: fmt.Errorf("forward after answer")}
	}
	if memberTimeout <= 0 {
		memberTimeout = forwardRingTimeout
	}

	s.ringOnce()

	callerName := s.callerName
	if callerName == "" {
		callerName = s.callerID
	}

	var members []DialOutcome
	for i, round := range rounds {
		if err := ctx.Err(); err != nil {
			return GroupOutcome{Members: members}, err
		}

		var candidates []*b2bua.LookupResult
		for _, target := range round {
			result, err := s.callService.Lookup(ctx, target)
			if err != nil || !result.HasContacts() {
				// A member with no live registration is skipped, not fatal: a
				// group exists precisely so one absent person does not black-hole
				// the caller. It is still recorded, because "three of four phones
				// were not registered" is worth knowing.
				s.logger.Debug("[Session] Ring group member unreachable",
					"call_id", s.callID, "target", target, "error", err)
				members = append(members, DialOutcome{
					Result: DialUnavailable, Target: target, Err: err,
				})
				continue
			}
			candidates = append(candidates, result)
		}
		if len(candidates) == 0 {
			continue
		}

		s.logger.Info("[Session] Ringing group round",
			"call_id", s.callID, "round", i+1, "of", len(rounds), "members", len(candidates))

		bLeg, outcomes, err := s.callService.DialParallelWithOutcomes(ctx, candidates, memberTimeout,
			b2bua.WithALegSessionID(s.sessionID),
			b2bua.WithALegCallID(s.callID),
			b2bua.WithCallerID(s.callerID),
			b2bua.WithCallerName(callerName),
		)
		for _, o := range outcomes {
			if o.Answered {
				members = append(members, DialOutcome{Result: DialAnswered, Target: o.Target})
				continue
			}
			member := classifyDialError(o.Target, o.Err)
			if o.SIPCode > 0 {
				member.SIPCode, member.SIPReason = o.SIPCode, o.SIPReason
			}
			members = append(members, member)
		}

		if err != nil {
			s.logger.Debug("[Session] Ring group round unanswered",
				"call_id", s.callID, "round", i+1, "error", err)
			continue
		}

		target := strings.Join(round, ",")
		if err := s.completeForward(ctx, bLeg, target); err != nil {
			out := classifyDialError(target, err)
			return GroupOutcome{DialOutcome: out, Members: members}, nil
		}
		return GroupOutcome{
			DialOutcome: DialOutcome{Result: DialAnswered, Target: target},
			Members:     members,
		}, nil
	}

	return GroupOutcome{
		DialOutcome: DialOutcome{Result: summariseGroup(members), Target: "group"},
		Members:     members,
	}, nil
}

// summariseGroup reduces per-member outcomes to the exit the group as a whole
// should take. Busy wins over no-answer when every member was busy, because
// "they are all on the phone" and "nobody is there" deserve different handling.
func summariseGroup(members []DialOutcome) DialResult {
	if len(members) == 0 {
		return DialUnavailable
	}
	all := func(r DialResult) bool {
		for _, m := range members {
			if m.Result != r {
				return false
			}
		}
		return true
	}
	switch {
	case all(DialBusy):
		return DialBusy
	case all(DialUnavailable):
		return DialUnavailable
	default:
		return DialNoAnswer
	}
}

// relayForwardFailure maps an outbound dial failure onto the caller's INVITE
// transaction. Because we never answered, the caller can still receive a real
// final status — a busy endpoint yields 486, an unreachable one 480 — which is
// the whole point of forwarding rather than answering first.
func (s *sessionImpl) relayForwardFailure(target string, cause error) error {
	code, reason := forwardFailureStatus(cause)

	if s.dialogMgr != nil && !s.HasAnswered() && !s.dialog.IsTerminated() {
		if err := s.dialogMgr.RespondStatus(s.dialog, code, reason); err != nil {
			s.logger.Warn("[Session] Failed to relay forward failure", "call_id", s.callID, "error", err)
		}
	}

	s.logger.Info("[Session] Forward failed", "call_id", s.callID, "target", target, "code", int(code), "reason", reason)

	if dialErr, ok := cause.(*b2bua.DialError); ok {
		return &DialError{Target: target, SIPCode: dialErr.SIPCode, SIPReason: dialErr.SIPReason, Cause: dialErr.Cause}
	}
	return &DialError{Target: target, SIPCode: int(code), SIPReason: reason, Cause: cause}
}

// forwardFailureStatus picks the status code relayed upstream. A B2BUA DialError
// carries the target's own final response, which is the most honest thing to
// relay; anything else (no contacts, timeout) becomes 480 Temporarily
// Unavailable.
func forwardFailureStatus(cause error) (sip.StatusCode, string) {
	if dialErr, ok := cause.(*b2bua.DialError); ok && dialErr.SIPCode >= 400 && dialErr.SIPCode <= 699 {
		reason := dialErr.SIPReason
		if reason == "" {
			reason = "Forward Failed"
		}
		return sip.StatusCode(dialErr.SIPCode), reason
	}
	return sip.StatusTemporarilyUnavailable, "Temporarily Unavailable"
}

// adoptALeg wraps the inbound dialog as a B2BUA A-leg with the same teardown
// semantics the post-answer bridge path uses: when the bridge tears the A-leg
// down (because B hung up), send BYE upstream — unless the caller is the one who
// sent the BYE, in which case the dialog layer already handled it.
func (s *sessionImpl) adoptALeg() (b2bua.Leg, error) {
	return s.callService.AdoptInboundLeg(s.dialog, s.sessionID,
		b2bua.WithTeardownHandler(func(leg b2bua.Leg) {
			if leg.GetTerminationCause() == b2bua.TerminationCauseRemoteBYE {
				return
			}
			if s.dialogMgr != nil && !s.dialog.IsTerminated() {
				if err := s.dialogMgr.Terminate(s.callID, dialog.ReasonLocalBYE); err != nil {
					s.logger.Warn("[Session] A-leg teardown BYE failed", "call_id", s.callID, "error", err)
				}
			}
		}),
	)
}

// setBLeg stores (or clears) the outbound leg handle under the lock.
func (s *sessionImpl) setBLeg(leg b2bua.Leg) {
	s.mu.Lock()
	s.bLeg = leg
	s.mu.Unlock()
}

// hangupBLeg tears down an outbound leg still in flight. It is called from the
// teardown funnel so that a caller who abandons while the target is ringing does
// not leave an orphaned B-leg ringing a phone nobody will answer.
func (s *sessionImpl) hangupBLeg(reason string) {
	s.mu.Lock()
	leg := s.bLeg
	s.bLeg = nil
	s.mu.Unlock()

	if leg == nil {
		return
	}
	s.logger.Info("[Session] Canceling orphaned B-leg", "call_id", s.callID, "leg_id", leg.ID(), "reason", reason)
	// Use a background context: the call context is already canceled by the
	// time teardown runs, and the CANCEL/BYE still has to go out on the wire.
	if err := leg.Hangup(context.Background(), b2bua.TerminationCauseNormal); err != nil {
		s.logger.Warn("[Session] B-leg hangup failed", "call_id", s.callID, "error", err)
	}
}
