package flowsim

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
)

// The engine explains itself through its logger, and for several outcomes that
// is the ONLY explanation: a destination denied by policy, an exit that is not
// wired, an entry naming a flow that does not exist. Discarding it — which is
// what the CLI harness did — leaves the operator with "outcome: hangup" and no
// reason. So a simulation captures the log instead of silencing it, and hands it
// back with the trace.

// maxLogRecords bounds one simulation's captured log. A traversal is bounded by
// MaxHops, so anything beyond this is a defect rather than a longer call.
const maxLogRecords = 200

// captureHandler collects log records instead of writing them anywhere.
type captureHandler struct {
	mu      *sync.Mutex
	records *[]string
	attrs   []slog.Attr
	group   string
}

func (h *captureHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	b.WriteString(r.Message)

	write := func(a slog.Attr) {
		b.WriteString(" ")
		if h.group != "" {
			b.WriteString(h.group)
			b.WriteString(".")
		}
		b.WriteString(a.Key)
		b.WriteString("=")
		fmt.Fprintf(&b, "%v", a.Value.Any())
	}
	for _, a := range h.attrs {
		write(a)
	}
	r.Attrs(func(a slog.Attr) bool {
		write(a)
		return true
	})

	h.mu.Lock()
	defer h.mu.Unlock()
	if len(*h.records) >= maxLogRecords {
		return nil
	}
	*h.records = append(*h.records, b.String())
	return nil
}

func (h *captureHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := *h
	next.attrs = append(append([]slog.Attr{}, h.attrs...), attrs...)
	return &next
}

func (h *captureHandler) WithGroup(name string) slog.Handler {
	next := *h
	next.group = name
	return &next
}

// captureLogger returns a logger that records rather than prints, and a reader
// for what it collected.
func captureLogger() (*slog.Logger, func() []string) {
	var (
		mu      sync.Mutex
		records []string
	)
	h := &captureHandler{mu: &mu, records: &records}
	read := func() []string {
		mu.Lock()
		defer mu.Unlock()
		out := make([]string, len(records))
		copy(out, records)
		return out
	}
	return slog.New(h), read
}

// simCounter names simulations apart. The flow engine tracks active calls by ID,
// so two concurrent simulations sharing one would collide in that map — and
// wall-clock time is not available to a resumable caller, so a counter it is.
var simCounter atomic.Uint64

// callSuffix returns a unique suffix for a simulated call ID.
func callSuffix() string {
	return fmt.Sprintf("%d", simCounter.Add(1))
}
