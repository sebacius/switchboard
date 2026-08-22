package cdr

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The record has to answer "why did this caller end up there", which means the
// path, not just the outcome.
func TestRecordCarriesTheTraversal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "records", "cdr.jsonl")
	sink, err := NewJSONLSink(path)
	if err != nil {
		t.Fatalf("NewJSONLSink: %v", err)
	}
	defer sink.Close()

	start := time.Now().Add(-30 * time.Second)
	err = sink.Write(Record{
		CallID:  "call-1",
		Tenant:  "acme",
		Caller:  "102",
		Callee:  "100",
		Flow:    "main",
		Path:    "greeting -> claims -> operator",
		Outcome: "answered",
		Hops: []Hop{
			{Node: "greeting", Type: "ivr", Exit: "2", DurationMs: 4200, Detail: "digits 2"},
			{Node: "claims", Type: "dial_user", Exit: "busy", DurationMs: 1100, Detail: "busy (486 Busy Here)"},
		},
		Decisions: []Decision{{Target: "+19005551212", Allowed: false, Reason: "barred class"}},
		StartedAt: start,
		EndedAt:   time.Now(),
	})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read records: %v", err)
	}

	var got Record
	if err := json.Unmarshal(data[:len(data)-1], &got); err != nil {
		t.Fatalf("record is not valid JSON: %v", err)
	}

	if got.Path != "greeting -> claims -> operator" {
		t.Errorf("path = %q, want the traversal", got.Path)
	}
	if len(got.Hops) != 2 {
		t.Fatalf("expected 2 hops, got %d", len(got.Hops))
	}
	if got.Hops[1].Detail != "busy (486 Busy Here)" {
		t.Errorf("hop detail should explain the exit, got %q", got.Hops[1].Detail)
	}
	// The duration is computed rather than requiring the caller to do it.
	if got.DurationMs < 29000 {
		t.Errorf("duration = %dms, want roughly 30s", got.DurationMs)
	}
	// A denied destination is auditable rather than living only in a log line.
	if len(got.Decisions) != 1 || got.Decisions[0].Allowed {
		t.Errorf("the deny verdict should be recorded, got %+v", got.Decisions)
	}
}

// One JSON object per line, so the questions people actually ask are answerable
// with grep and jq.
func TestRecordsAreLineDelimited(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cdr.jsonl")
	sink, err := NewJSONLSink(path)
	if err != nil {
		t.Fatalf("NewJSONLSink: %v", err)
	}

	for _, id := range []string{"a", "b", "c"} {
		if err := sink.Write(Record{CallID: id, StartedAt: time.Now(), EndedAt: time.Now()}); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	sink.Close()

	data, _ := os.ReadFile(path)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d", len(lines))
	}
	for _, line := range lines {
		var r Record
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Errorf("each line must stand alone as JSON: %v", err)
		}
	}
}

// Appending rather than truncating: restarting the server must not lose the
// day's records.
func TestSinkAppends(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cdr.jsonl")

	first, _ := NewJSONLSink(path)
	first.Write(Record{CallID: "before-restart"})
	first.Close()

	second, _ := NewJSONLSink(path)
	second.Write(Record{CallID: "after-restart"})
	second.Close()

	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "before-restart") {
		t.Error("reopening the sink must not truncate existing records")
	}
	if !strings.Contains(string(data), "after-restart") {
		t.Error("the new record should be appended")
	}
}
