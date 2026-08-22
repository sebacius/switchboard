package flow

import "time"

// A call record that says only "ended in voicemail" cannot answer the question
// people actually ask, which is why. The traversal — which nodes, in order, with
// the exit each produced — is the answer, and it costs one struct per call.

// Trace is one completed traversal.
type Trace struct {
	CallID  string    `json:"call_id"`
	Tenant  string    `json:"tenant"`
	Flow    string    `json:"flow"`
	Outcome string    `json:"outcome"`
	Path    string    `json:"path"`
	Hops    []Hop     `json:"hops"`
	Started time.Time `json:"started"`
	Ended   time.Time `json:"ended"`
}

// DurationMs is how long the call spent in the flow.
func (t Trace) DurationMs() int64 {
	return t.Ended.Sub(t.Started).Milliseconds()
}

// TraceSink receives completed traversals.
type TraceSink interface {
	Record(Trace)
}

// TraceFunc adapts a function to TraceSink.
type TraceFunc func(Trace)

// Record implements TraceSink.
func (f TraceFunc) Record(t Trace) { f(t) }
