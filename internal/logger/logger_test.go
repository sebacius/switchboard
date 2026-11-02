package logger

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// A fixed instant so golden output does not depend on the clock. 03:37:29 is
// the timestamp from the sipgo lines these tests are modelled on.
var testTime = time.Date(2026, 9, 2, 3, 37, 29, 0, time.UTC)

func newTestSink(f Format) (*sink, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	return &sink{out: buf, format: f}, buf
}

func newTestForeign(s *sink, src string, lvl slog.Level) *ForeignWriter {
	w := &ForeignWriter{src: src, sink: s}
	w.SetLevel(lvl)
	return w
}

// withAppLevel sets the process-wide application level for one test.
func withAppLevel(t *testing.T, l slog.Level) {
	t.Helper()
	prev := appLevel.Load()
	appLevel.Store(int32(l))
	t.Cleanup(func() { appLevel.Store(prev) })
}

func record(level slog.Level, msg string, args ...any) slog.Record {
	r := slog.NewRecord(testTime, level, msg, 0)
	r.Add(args...)
	return r
}

// --- application stream -----------------------------------------------------

func TestTextFormatMatchesTheEstablishedShape(t *testing.T) {
	withAppLevel(t, slog.LevelDebug)
	s, buf := newTestSink(FormatText)
	h := &customHandler{sink: s}

	err := h.Handle(context.Background(),
		record(slog.LevelInfo, "[REGISTER] Success",
			"aor", "sip:100@devtenant.com:5060", "bindings", 1))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}

	want := "[03:37:29] [INFO] [REGISTER] Success aor=sip:100@devtenant.com:5060 bindings=1\n"
	if got := buf.String(); got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

func TestAppLevelFilters(t *testing.T) {
	withAppLevel(t, slog.LevelInfo)
	s, buf := newTestSink(FormatText)
	h := &customHandler{sink: s}

	if h.Enabled(context.Background(), slog.LevelDebug) {
		t.Error("debug should be disabled at info")
	}
	_ = h.Handle(context.Background(), record(slog.LevelDebug, "[REGISTER] Processing"))
	if buf.Len() != 0 {
		t.Errorf("debug record leaked at info level: %q", buf.String())
	}

	_ = h.Handle(context.Background(), record(slog.LevelWarn, "[REGISTER] Trouble"))
	if !strings.Contains(buf.String(), "[WARN]") {
		t.Errorf("warn record suppressed at info level: %q", buf.String())
	}
}

// The regression that motivated this: WithAttrs used to return the receiver
// unchanged, so the tenant on every policy decision was silently dropped.
func TestWithAttrsAreEmitted(t *testing.T) {
	withAppLevel(t, slog.LevelDebug)
	s, buf := newTestSink(FormatText)

	log := slog.New(&customHandler{sink: s}).With("tenant", "devtenant", "component", "tool-policy")
	log.LogAttrs(context.Background(), slog.LevelInfo, "[POLICY] Denied", slog.String("target", "900"))

	got := buf.String()
	for _, want := range []string{"tenant=devtenant", "component=tool-policy", "target=900"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
}

func TestWithGroupPrefixesKeys(t *testing.T) {
	withAppLevel(t, slog.LevelDebug)
	s, buf := newTestSink(FormatText)

	log := slog.New(&customHandler{sink: s}).WithGroup("sip").With("code", 486)
	log.LogAttrs(context.Background(), slog.LevelInfo, "[INVITE] Rejected", slog.String("reason", "busy"))

	got := buf.String()
	for _, want := range []string{"sip.code=486", "sip.reason=busy"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
}

// An unquoted error turns one attr into several bogus key=value tokens.
func TestAttrValuesWithSpacesAreQuoted(t *testing.T) {
	withAppLevel(t, slog.LevelDebug)
	s, buf := newTestSink(FormatText)
	h := &customHandler{sink: s}

	_ = h.Handle(context.Background(), record(slog.LevelError, "[RTP] Dial failed",
		"error", errors.New("dial tcp 10.0.0.1:5060: connect: connection refused")))

	want := `error="dial tcp 10.0.0.1:5060: connect: connection refused"`
	if got := buf.String(); !strings.Contains(got, want) {
		t.Errorf("got %q, want it to contain %q", got, want)
	}
}

type resolvingValue struct{ v string }

func (r resolvingValue) LogValue() slog.Value { return slog.StringValue(r.v) }

func TestLogValuerIsResolved(t *testing.T) {
	withAppLevel(t, slog.LevelDebug)
	s, buf := newTestSink(FormatText)
	h := &customHandler{sink: s}

	_ = h.Handle(context.Background(),
		record(slog.LevelInfo, "[FLOW] Node", "node", resolvingValue{"main-ivr"}))

	if got := buf.String(); !strings.Contains(got, "node=main-ivr") {
		t.Errorf("LogValuer not resolved: %q", got)
	}
}

// --- foreign (sipgo/zerolog) stream ----------------------------------------

const sipgoLine = `{"level":"debug","caller":"transactionLayer","tx":"z9hG4bK2336289457__192.168.50.251__5060__REGISTER","time":"2026-09-02T03:37:29Z","message":"Server transaction destroyed"}`

func TestForeignLineRendersLikeOurOwn(t *testing.T) {
	s, buf := newTestSink(FormatText)
	w := newTestForeign(s, "sip", slog.LevelDebug)

	n, err := w.Write([]byte(sipgoLine + "\n"))
	if err != nil {
		t.Fatalf("Write returned an error: %v", err)
	}
	if n != len(sipgoLine)+1 {
		t.Errorf("Write reported %d bytes, want %d", n, len(sipgoLine)+1)
	}

	want := "[03:37:29] [DEBUG] [transactionLayer] Server transaction destroyed " +
		"tx=z9hG4bK2336289457__192.168.50.251__5060__REGISTER\n"
	if got := buf.String(); got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

// Most of sipgo's output, the packet dumps included, goes through the
// package-level zerolog functions and carries no caller at all.
func TestForeignWithoutCallerFallsBackToSrc(t *testing.T) {
	s, buf := newTestSink(FormatText)
	w := newTestForeign(s, "sip", slog.LevelDebug)

	w.Write([]byte(`{"level":"warn","time":"2026-09-02T03:37:29Z","message":"DNS resolution is slow","dur":"2.1s"}`))

	want := "[03:37:29] [WARN] [sip] DNS resolution is slow dur=2.1s\n"
	if got := buf.String(); got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

// sipgo emits a field literally named "msg" that is NOT the message.
func TestForeignMsgFieldDoesNotClobberTheMessage(t *testing.T) {
	s, buf := newTestSink(FormatText)
	w := newTestForeign(s, "sip", slog.LevelDebug)

	w.Write([]byte(`{"level":"info","caller":"transactionLayer","msg":"INVITE sip:100@x",` +
		`"time":"2026-09-02T03:37:29Z","message":"Unhandled sip request"}`))

	got := buf.String()
	if !strings.Contains(got, "] Unhandled sip request ") {
		t.Errorf("message was clobbered: %q", got)
	}
	if !strings.Contains(got, `msg="INVITE sip:100@x"`) {
		t.Errorf("msg field lost: %q", got)
	}
}

// SIPDebug dumps a whole SIP packet into the message.
func TestForeignMultilineMessageIsIndented(t *testing.T) {
	s, buf := newTestSink(FormatText)
	w := newTestForeign(s, "sip", slog.LevelDebug)

	raw, err := json.Marshal(map[string]any{
		"level":   "debug",
		"time":    "2026-09-02T03:37:29Z",
		"message": "UDP read 1.2.3.4:5060 <- 5.6.7.8:5060:\nREGISTER sip:x SIP/2.0\r\nVia: SIP/2.0/UDP x\r\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	w.Write(raw)

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) < 3 {
		t.Fatalf("expected a header plus indented body, got %q", buf.String())
	}
	if !strings.HasPrefix(lines[0], "[03:37:29] [DEBUG] [sip] UDP read") {
		t.Errorf("header line wrong: %q", lines[0])
	}
	for _, l := range lines[1:] {
		if !strings.HasPrefix(l, "    ") {
			t.Errorf("continuation line not indented: %q", l)
		}
	}
}

// The old JSON reformatter ran values through fmt.Sprint on a float64, turning
// a count into 1.234567e+06.
func TestForeignIntegersKeepTheirDigits(t *testing.T) {
	s, buf := newTestSink(FormatText)
	w := newTestForeign(s, "sip", slog.LevelDebug)

	w.Write([]byte(`{"level":"info","time":"2026-09-02T03:37:29Z","message":"Counted","calls":1234567}`))

	if got := buf.String(); !strings.Contains(got, "calls=1234567") {
		t.Errorf("integer mangled: %q", got)
	}
}

// Ranging a map reorders attrs on every line, which makes output undiffable.
func TestForeignAttrOrderIsDeterministic(t *testing.T) {
	line := []byte(`{"level":"info","time":"2026-09-02T03:37:29Z","message":"m","a":"1","b":"2","c":"3","d":"4","e":"5"}`)

	var first string
	for i := 0; i < 50; i++ {
		s, buf := newTestSink(FormatText)
		newTestForeign(s, "sip", slog.LevelDebug).Write(line)
		if i == 0 {
			first = buf.String()
			continue
		}
		if buf.String() != first {
			t.Fatalf("attr order varies:\n%q\n%q", first, buf.String())
		}
	}
	if !strings.Contains(first, "a=1 b=2 c=3 d=4 e=5") {
		t.Errorf("attrs not in document order: %q", first)
	}
}

// The whole point: the two streams filter independently.
func TestForeignLevelIsIndependentOfAppLevel(t *testing.T) {
	withAppLevel(t, slog.LevelError) // app almost silent...
	s, buf := newTestSink(FormatText)
	w := newTestForeign(s, "sip", slog.LevelDebug) // ...sip verbose

	w.Write([]byte(sipgoLine))
	if buf.Len() == 0 {
		t.Error("sip debug suppressed by the app level")
	}

	buf.Reset()
	withAppLevel(t, slog.LevelDebug) // app verbose...
	w.SetLevel(slog.LevelWarn)       // ...sip quiet
	w.Write([]byte(sipgoLine))
	if buf.Len() != 0 {
		t.Errorf("sip debug leaked past its own level: %q", buf.String())
	}
}

func TestForeignOffSuppressesEverything(t *testing.T) {
	s, buf := newTestSink(FormatText)
	w := newTestForeign(s, "sip", LevelOff)

	w.Write([]byte(sipgoLine))
	w.Write([]byte(`{"level":"error","time":"2026-09-02T03:37:29Z","message":"boom"}`))
	if buf.Len() != 0 {
		t.Errorf("output at level off: %q", buf.String())
	}
}

// Returning an error would make zerolog print to stderr once per event.
func TestForeignWriteNeverReportsFailure(t *testing.T) {
	s, _ := newTestSink(FormatText)
	s.out = failingWriter{}
	w := newTestForeign(s, "sip", slog.LevelDebug)

	for _, in := range [][]byte{
		[]byte(sipgoLine),
		[]byte("not json at all"),
		[]byte(`{"level":`), // truncated
		{},
		[]byte("{}"),
	} {
		n, err := w.Write(in)
		if err != nil {
			t.Errorf("Write(%q) returned %v", in, err)
		}
		if n != len(in) {
			t.Errorf("Write(%q) reported %d, want %d", in, n, len(in))
		}
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("disk on fire") }

// zerolog hands back a pooled buffer the moment Write returns.
func TestForeignDoesNotRetainTheCallerBuffer(t *testing.T) {
	s, buf := newTestSink(FormatText)
	w := newTestForeign(s, "sip", slog.LevelDebug)

	p := []byte(sipgoLine)
	w.Write(p)
	before := buf.String()

	for i := range p { // simulate the pool handing the buffer to someone else
		p[i] = 'x'
	}
	if buf.String() != before {
		t.Error("output changed after the caller reused its buffer")
	}
}

// --- JSON mode --------------------------------------------------------------

func TestJSONModeIsParseable(t *testing.T) {
	withAppLevel(t, slog.LevelDebug)
	s, buf := newTestSink(FormatJSON)
	h := &customHandler{sink: s}
	w := newTestForeign(s, "sip", slog.LevelDebug)

	_ = h.Handle(context.Background(), record(slog.LevelInfo, "[REGISTER] Success",
		"aor", "sip:100@devtenant.com:5060", "bindings", 1))
	w.Write([]byte(sipgoLine))

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d: %q", len(lines), buf.String())
	}

	var app map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &app); err != nil {
		t.Fatalf("app line is not JSON: %v (%s)", err, lines[0])
	}
	if app["src"] != "app" || app["msg"] != "[REGISTER] Success" {
		t.Errorf("unexpected app envelope: %v", app)
	}
	fields, _ := app["fields"].(map[string]any)
	if fields["aor"] != "sip:100@devtenant.com:5060" {
		t.Errorf("attrs not under fields: %v", app)
	}
	if _, collided := app["aor"]; collided {
		t.Error("attrs leaked into the envelope")
	}

	var sip map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &sip); err != nil {
		t.Fatalf("sip line is not JSON: %v (%s)", err, lines[1])
	}
	if sip["src"] != "sip" || sip["component"] != "transactionLayer" {
		t.Errorf("unexpected sip envelope: %v", sip)
	}
}

// Both collisions are real: sipgo emits a "msg" field, and our own resolver
// logs a "caller" attr meaning the calling party.
func TestJSONModeNamespacesCollidingKeys(t *testing.T) {
	withAppLevel(t, slog.LevelDebug)
	s, buf := newTestSink(FormatJSON)
	h := &customHandler{sink: s}

	_ = h.Handle(context.Background(), record(slog.LevelInfo, "[RESOLVE] Resolved",
		"caller", "sip:100@x", "callee", "sip:200@x"))

	var got map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &got); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if _, ok := got["component"]; ok {
		t.Error("app event should carry no component")
	}
	fields := got["fields"].(map[string]any)
	if fields["caller"] != "sip:100@x" {
		t.Errorf("caller attr not namespaced under fields: %v", got)
	}
}

func TestJSONNumbersAreNotStrings(t *testing.T) {
	s, buf := newTestSink(FormatJSON)
	newTestForeign(s, "sip", slog.LevelDebug).Write(
		[]byte(`{"level":"info","time":"2026-09-02T03:37:29Z","message":"m","calls":1234567}`))

	var got map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &got); err != nil {
		t.Fatalf("not JSON: %v (%s)", err, buf.String())
	}
	fields := got["fields"].(map[string]any)
	if n, ok := fields["calls"].(float64); !ok || n != 1234567 {
		t.Errorf("calls not a JSON number: %#v", fields["calls"])
	}
}

// --- concurrency ------------------------------------------------------------

// The single sink mutex is what keeps the two streams from interleaving
// mid-line. Run with -race.
func TestStreamsDoNotInterleave(t *testing.T) {
	withAppLevel(t, slog.LevelDebug)
	s, buf := newTestSink(FormatText)
	h := &customHandler{sink: s}
	w := newTestForeign(s, "sip", slog.LevelDebug)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				_ = h.Handle(context.Background(), record(slog.LevelInfo, "[APP] line"))
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				w.Write([]byte(sipgoLine))
			}
		}()
	}
	wg.Wait()

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 8*2*200 {
		t.Fatalf("expected %d lines, got %d", 8*2*200, len(lines))
	}
	for _, l := range lines {
		if !strings.HasPrefix(l, "[") || !strings.Contains(l, "] [") {
			t.Fatalf("torn line: %q", l)
		}
	}
}

// --- level parsing ----------------------------------------------------------

func TestParseLevelReportsGarbageInsteadOfMaximisingVerbosity(t *testing.T) {
	lvl, err := ParseLevel("bogus")
	if err == nil {
		t.Error("expected an error for an unknown level")
	}
	if lvl != slog.LevelInfo {
		t.Errorf("fallback was %v, want info", lvl)
	}
	for in, want := range map[string]slog.Level{
		"debug": slog.LevelDebug,
		"INFO":  slog.LevelInfo,
		"warn":  slog.LevelWarn,
		"error": slog.LevelError,
		"off":   LevelOff,
	} {
		got, err := ParseLevel(in)
		if err != nil || got != want {
			t.Errorf("ParseLevel(%q) = %v, %v; want %v, nil", in, got, err, want)
		}
	}
}

func TestParseFormat(t *testing.T) {
	if f, err := ParseFormat("json"); f != FormatJSON || err != nil {
		t.Errorf("json: %v %v", f, err)
	}
	if f, err := ParseFormat(""); f != FormatText || err != nil {
		t.Errorf("empty: %v %v", f, err)
	}
	if f, err := ParseFormat("yaml"); f != FormatText || err == nil {
		t.Errorf("yaml should fall back to text with an error, got %v %v", f, err)
	}
}

func TestGetLevelRoundTrips(t *testing.T) {
	for _, name := range []string{"debug", "info", "warn", "error", "off"} {
		if err := SetLevel(name); err != nil {
			t.Fatalf("SetLevel(%q): %v", name, err)
		}
		if got := GetLevel(); got != name {
			t.Errorf("SetLevel(%q) then GetLevel() = %q", name, got)
		}
	}
	SetLevel("debug")
}
