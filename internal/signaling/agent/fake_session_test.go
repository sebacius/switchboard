package agent

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sebas/switchboard/internal/signaling/dialog"
	"github.com/sebas/switchboard/internal/signaling/mediaclient"
)

// fakeSession is a test CallSession recording what each call path did to it:
// which targets were forwarded or dialed, whether the leg was answered, and
// what was spoken. It is the shared double for every test in this package.
type fakeSession struct {
	mu sync.Mutex

	callID string

	ttsSpoken []string

	hangupCalls   atomic.Int32
	terminatedVal atomic.Bool

	// Answer-model state. answeredVal records the 200 OK; forwarded and dialed
	// record which SIP path the call took.
	answeredVal atomic.Bool
	answerCalls atomic.Int32
	rangEarly   atomic.Bool
	forwarded   []string
	dialed      []string

	// forwardErr / dialErr make the outbound leg fail, for the relay path.
	forwardErr error
	dialErr    error

	// forwardBlocks makes Forward hang until the call context is canceled,
	// modeling a target that rings and rings while the caller may CANCEL.
	forwardBlocks bool
	// forwardStarted closes once Forward has been entered, so a test can
	// deterministically CANCEL mid-forward.
	forwardStarted chan struct{}

	// Ring-group state. groupRounds records what ForwardGroup was asked to ring,
	// in order, which is how the strategy tests assert ring order. groupErr is
	// what it returns — set it to ErrGroupNoAnswer to exercise a group nobody
	// picks up.
	groupRounds [][]string
	groupErr    error
}

func newFakeSession() *fakeSession {
	return &fakeSession{
		callID:         "test-call",
		forwardStarted: make(chan struct{}),
	}
}

func (f *fakeSession) spoken() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.ttsSpoken...)
}

func (f *fakeSession) CallID() string           { return f.callID }
func (f *fakeSession) Destination() string      { return "1000" }
func (f *fakeSession) CallerID() string         { return "2000" }
func (f *fakeSession) Domain() string           { return "example.test" }
func (f *fakeSession) Context() context.Context { return context.Background() }

func (f *fakeSession) PlayAudio(ctx context.Context, file string) error {
	return f.Answer(ctx)
}

// Answer records the 200 OK. It is idempotent, like the real session, so the
// runner calling it before every utterance costs nothing after the first.
func (f *fakeSession) Answer(context.Context) error {
	f.answerCalls.Add(1)
	f.answeredVal.Store(true)
	return nil
}

func (f *fakeSession) HasAnswered() bool { return f.answeredVal.Load() }

// MarkRinging records the early 180 the INVITE handler sends before the first
// turn, so a later Forward knows not to send a second one.
func (f *fakeSession) MarkRinging() { f.rangEarly.Store(true) }

// Forward is the pre-answer routing path. It never answers: that is the whole
// invariant under test for a silent internal route.
func (f *fakeSession) Forward(ctx context.Context, target string, _ time.Duration) error {
	f.mu.Lock()
	f.forwarded = append(f.forwarded, target)
	blocks := f.forwardBlocks
	err := f.forwardErr
	f.mu.Unlock()

	if f.forwardStarted != nil {
		select {
		case <-f.forwardStarted:
		default:
			close(f.forwardStarted)
		}
	}

	if err != nil {
		return err
	}
	if blocks {
		// The target is ringing. Real Forward blocks here until the B-leg
		// answers or the call goes away.
		<-ctx.Done()
		return ctx.Err()
	}
	return nil
}

// ForwardGroup is the pre-answer ring-group path. It records the rounds it was
// given and answers (or not) according to groupErr, so a test can drive both the
// "someone picked up" and "nobody picked up" branches without a media stack.
func (f *fakeSession) ForwardGroup(_ context.Context, rounds [][]string, _ time.Duration) error {
	f.mu.Lock()
	f.groupRounds = append(f.groupRounds, rounds...)
	err := f.groupErr
	f.mu.Unlock()

	if err != nil {
		return err
	}
	// A member answered: a real ForwardGroup relays the 200 upstream.
	f.answeredVal.Store(true)
	f.answerCalls.Add(1)
	return nil
}

// rungRounds returns the rounds ForwardGroup was asked to ring, in order.
func (f *fakeSession) rungRounds() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([][]string, 0, len(f.groupRounds))
	for _, r := range f.groupRounds {
		out = append(out, append([]string(nil), r...))
	}
	return out
}

func (f *fakeSession) forwards() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.forwarded...)
}

func (f *fakeSession) dials() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.dialed...)
}

func (f *fakeSession) PlayTTS(ctx context.Context, text, _ string) error {
	if err := f.Answer(ctx); err != nil {
		return err
	}
	f.mu.Lock()
	f.ttsSpoken = append(f.ttsSpoken, text)
	f.mu.Unlock()
	return nil
}

func (f *fakeSession) StopAudio() error { return nil }

func (f *fakeSession) Dial(_ context.Context, target string, _ time.Duration) error {
	f.mu.Lock()
	f.dialed = append(f.dialed, target)
	err := f.dialErr
	f.mu.Unlock()
	return err
}

func (f *fakeSession) Hangup(string) error {
	f.hangupCalls.Add(1)
	f.terminatedVal.Store(true)
	return nil
}

func (f *fakeSession) IsTerminated() bool                   { return f.terminatedVal.Load() }
func (f *fakeSession) GetDialog() *dialog.Dialog            { return nil }
func (f *fakeSession) GetSessionID() string                 { return "rtp-sess" }
func (f *fakeSession) GetTransport() mediaclient.Transport  { return nil }
func (f *fakeSession) TerminateDialog(string, string) error { return nil }

// ForwardOutcome mirrors Forward but reports the outcome instead of relaying it,
// which is what a flow node calls.
func (f *fakeSession) ForwardOutcome(ctx context.Context, target string, timeout time.Duration) (DialOutcome, error) {
	err := f.Forward(ctx, target, timeout)
	if err == nil {
		return DialOutcome{Result: DialAnswered, Target: target}, nil
	}
	return classifyDialError(target, err), nil
}

// ForwardGroupOutcome mirrors ForwardGroup, reporting per-member detail.
func (f *fakeSession) ForwardGroupOutcome(ctx context.Context, rounds [][]string, memberTimeout time.Duration) (GroupOutcome, error) {
	err := f.ForwardGroup(ctx, rounds, memberTimeout)
	if err == nil {
		return GroupOutcome{DialOutcome: DialOutcome{Result: DialAnswered}}, nil
	}

	var members []DialOutcome
	for _, round := range rounds {
		for _, target := range round {
			members = append(members, classifyDialError(target, err))
		}
	}
	return GroupOutcome{DialOutcome: classifyDialError("group", err), Members: members}, nil
}
