// Package flowtest provides the one fake CallSession the flow tests share.
//
// One fake, not several. Every variant of a test double is a second definition
// of how the system behaves, and they drift.
package flowtest

import (
	"context"
	"sync"
	"time"

	"github.com/sebas/switchboard/internal/signaling/agent"
	"github.com/sebas/switchboard/internal/signaling/b2bua"
	"github.com/sebas/switchboard/internal/signaling/dialog"
	"github.com/sebas/switchboard/internal/signaling/mediaclient"
)

// Session records what a flow did to a call and serves scripted caller input.
type Session struct {
	mu sync.Mutex

	callID string
	ctx    context.Context
	cancel context.CancelFunc

	answered   bool
	terminated bool

	// Spoken, Played and Dialed record what the flow did, in order.
	Spoken  []string
	Played  []string
	Dialed  []string
	Hangups []string

	// Relayed records any final status sent to the caller. A flow that continues
	// after a failed dial must leave this EMPTY: once a status is relayed the
	// caller's call is over and no later node can run.
	Relayed []string

	// Collects records what each digit collection asked for.
	Collects []agent.CollectRequest

	// scripted caller input and dial results, consumed in order.
	collectScript []agent.CollectResult
	dialScript    []agent.DialOutcome
	groupScript   []agent.GroupOutcome
}

// New builds a session.
func New() *Session {
	ctx, cancel := context.WithCancel(context.Background())
	return &Session{callID: "test-call", ctx: ctx, cancel: cancel}
}

// QueueDigits scripts the next collection.
func (s *Session) QueueDigits(digits string, reason agent.CollectReason) *Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.collectScript = append(s.collectScript, agent.CollectResult{Digits: digits, Reason: reason})
	return s
}

// QueueDial scripts the next dial's outcome.
func (s *Session) QueueDial(result agent.DialResult, code int) *Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dialScript = append(s.dialScript, agent.DialOutcome{Result: result, SIPCode: code})
	return s
}

// QueueGroup scripts the next ring group's outcome.
func (s *Session) QueueGroup(result agent.DialResult) *Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.groupScript = append(s.groupScript, agent.GroupOutcome{
		DialOutcome: agent.DialOutcome{Result: result},
	})
	return s
}

// Abandon simulates the caller hanging up.
func (s *Session) Abandon() { s.cancel() }

// --- CallSession ---

func (s *Session) CallID() string           { return s.callID }
func (s *Session) Destination() string      { return "100" }
func (s *Session) CallerID() string         { return "102" }
func (s *Session) Domain() string           { return "example.test" }
func (s *Session) Context() context.Context { return s.ctx }

func (s *Session) PlayAudio(_ context.Context, file string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.answered = true
	s.Played = append(s.Played, file)
	return nil
}

func (s *Session) PlayTTS(_ context.Context, text, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.answered = true
	s.Spoken = append(s.Spoken, text)
	return nil
}

func (s *Session) StopAudio() error { return nil }

func (s *Session) CollectDigits(_ context.Context, req agent.CollectRequest) (agent.CollectResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.answered = true
	s.Collects = append(s.Collects, req)

	if len(s.collectScript) == 0 {
		// Nothing scripted: the caller pressed nothing.
		return agent.CollectResult{Reason: agent.CollectFirstDigitTimeout}, nil
	}
	r := s.collectScript[0]
	s.collectScript = s.collectScript[1:]
	return r, nil
}

func (s *Session) Answer(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.answered = true
	return nil
}

func (s *Session) MarkRinging()      {}
func (s *Session) HasAnswered() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.answered }

func (s *Session) Forward(_ context.Context, target string, _ time.Duration) error {
	out, _ := s.ForwardOutcome(context.Background(), target, 0)
	if out.Answered() {
		return nil
	}
	// The relaying form: record that the caller was told something.
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Relayed = append(s.Relayed, out.ExitName())
	return out.Error()
}

func (s *Session) ForwardOutcome(_ context.Context, target string, _ time.Duration) (agent.DialOutcome, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Dialed = append(s.Dialed, target)

	if len(s.dialScript) == 0 {
		return agent.DialOutcome{Result: agent.DialAnswered, Target: target}, nil
	}
	out := s.dialScript[0]
	s.dialScript = s.dialScript[1:]
	out.Target = target
	return out, nil
}

func (s *Session) ForwardGroup(_ context.Context, rounds [][]string, _ time.Duration) error {
	out, _ := s.ForwardGroupOutcome(context.Background(), rounds, 0)
	if out.Answered() {
		return nil
	}
	return out.Error()
}

func (s *Session) ForwardGroupOutcome(_ context.Context, rounds [][]string, _ time.Duration) (agent.GroupOutcome, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, round := range rounds {
		s.Dialed = append(s.Dialed, round...)
	}

	if len(s.groupScript) == 0 {
		return agent.GroupOutcome{DialOutcome: agent.DialOutcome{Result: agent.DialAnswered}}, nil
	}
	out := s.groupScript[0]
	s.groupScript = s.groupScript[1:]
	return out, nil
}

// Dial is the post-answer bridge. It consumes the same script as
// ForwardOutcome, so a test does not have to know which path a node took —
// which is the point, since that depends on whether a media node ran first.
func (s *Session) Dial(_ context.Context, target string, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Dialed = append(s.Dialed, target)

	if len(s.dialScript) == 0 {
		return nil
	}
	out := s.dialScript[0]
	s.dialScript = s.dialScript[1:]
	if out.Answered() {
		return nil
	}
	return &b2bua.DialError{Target: target, SIPCode: sipCodeFor(out)}
}

// sipCodeFor gives a scripted result the status code a real endpoint would send,
// so classification round-trips.
func sipCodeFor(out agent.DialOutcome) int {
	if out.SIPCode > 0 {
		return out.SIPCode
	}
	switch out.Result {
	case agent.DialBusy:
		return 486
	case agent.DialUnavailable:
		return 480
	case agent.DialNoAnswer:
		return 408
	default:
		return 603
	}
}

func (s *Session) Hangup(reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Hangups = append(s.Hangups, reason)
	s.terminated = true
	return nil
}

func (s *Session) IsTerminated() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.terminated
}

func (s *Session) GetDialog() *dialog.Dialog            { return nil }
func (s *Session) GetSessionID() string                 { return "rtp-session" }
func (s *Session) GetTransport() mediaclient.Transport  { return nil }
func (s *Session) TerminateDialog(string, string) error { return nil }
