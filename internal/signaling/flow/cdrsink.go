package flow

import (
	"log/slog"

	"github.com/sebas/switchboard/internal/signaling/cdr"
)

// CDRTrace adapts a cdr.Sink to the engine's TraceSink, so the flow package
// does not depend on the record format and the record format does not depend on
// the flow types.
type CDRTrace struct {
	Sink cdr.Sink
	Log  *slog.Logger
}

// Record implements TraceSink.
func (c CDRTrace) Record(t Trace) {
	if c.Sink == nil {
		return
	}

	hops := make([]cdr.Hop, 0, len(t.Hops))
	for _, h := range t.Hops {
		hops = append(hops, cdr.Hop{
			Node:       h.Node,
			Type:       h.Type,
			Exit:       h.Exit,
			DurationMs: h.DurationMs,
			Detail:     h.Detail,
		})
	}

	decisions := make([]cdr.Decision, 0, len(t.Decisions))
	for _, d := range t.Decisions {
		decisions = append(decisions, cdr.Decision{
			Target: d.Target, Allowed: d.Allowed, Reason: d.Reason,
		})
	}

	rec := cdr.Record{
		CallID:    t.CallID,
		Tenant:    t.Tenant,
		Flow:      t.Flow,
		Path:      t.Path,
		Hops:      hops,
		Outcome:   t.Outcome,
		Decisions: decisions,
		StartedAt: t.Started,
		EndedAt:   t.Ended,
	}

	if err := c.Sink.Write(rec); err != nil && c.Log != nil {
		// A record that cannot be written must not take the call with it.
		c.Log.Warn("failed to write call record", "call_id", t.CallID, "error", err)
	}
}
