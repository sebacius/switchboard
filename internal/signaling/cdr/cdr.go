// Package cdr writes durable call records.
//
// Scope is deliberately small. store.CDR and store.CDRRepository have existed
// with zero implementations and zero callers since before this change, and
// events.CallEndedEvent and a NATS subject exist beside them, unused. It would
// be easy to build all of it and ship nothing that answers a question. What is
// actually needed is one thing: a record that says WHY a caller ended up where
// they did, which means the path they took and the authorization verdicts along
// the way — not a schema for billing that nothing writes.
//
// So: one interface, one append-only JSONL implementation. No SQL repository,
// no event bus, no subjects.
package cdr

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Record is one completed call.
type Record struct {
	CallID    string `json:"call_id"`
	Tenant    string `json:"tenant"`
	Caller    string `json:"caller"`
	Callee    string `json:"callee"`
	Direction string `json:"direction"`

	// Flow and Path describe the traversal. Path is the readable form —
	// "greeting -> claims -> operator" — and Hops carries the timing.
	Flow string `json:"flow,omitempty"`
	Path string `json:"path,omitempty"`
	Hops []Hop  `json:"hops,omitempty"`

	// Outcome is how the call ended.
	Outcome string `json:"outcome"`

	// Decisions records authorization verdicts, so a denied destination is
	// auditable as a fraud signal rather than existing only as a log line.
	Decisions []Decision `json:"decisions,omitempty"`

	StartedAt  time.Time `json:"started_at"`
	EndedAt    time.Time `json:"ended_at"`
	DurationMs int64     `json:"duration_ms"`
}

// Hop is one step of a traversal.
type Hop struct {
	Node       string `json:"node"`
	Type       string `json:"type"`
	Exit       string `json:"exit"`
	DurationMs int64  `json:"duration_ms"`
	Detail     string `json:"detail,omitempty"`
}

// Decision is one authorization verdict.
type Decision struct {
	Target  string `json:"target"`
	Allowed bool   `json:"allowed"`
	Reason  string `json:"reason"`
}

// Sink receives completed records.
type Sink interface {
	Write(Record) error
}

// JSONLSink appends one JSON object per line.
//
// Append-only and line-delimited because the questions asked of a call record
// are answered by grep and jq far more often than by SQL, and a format that
// survives a crash mid-write matters more than one that supports joins.
type JSONLSink struct {
	mu   sync.Mutex
	path string
	file *os.File
}

// NewJSONLSink opens (or creates) the record file.
func NewJSONLSink(path string) (*JSONLSink, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create call record directory: %w", err)
		}
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open call record file: %w", err)
	}
	return &JSONLSink{path: path, file: f}, nil
}

// Write appends one record.
func (s *JSONLSink) Write(r Record) error {
	if r.DurationMs == 0 && !r.EndedAt.IsZero() {
		r.DurationMs = r.EndedAt.Sub(r.StartedAt).Milliseconds()
	}

	line, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("marshal call record: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.file.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("write call record: %w", err)
	}
	return nil
}

// Close releases the file.
func (s *JSONLSink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file == nil {
		return nil
	}
	err := s.file.Close()
	s.file = nil
	return err
}

// Discard is a sink that drops records, for deployments with no --cdr-path.
type Discard struct{}

// Write implements Sink.
func (Discard) Write(Record) error { return nil }
