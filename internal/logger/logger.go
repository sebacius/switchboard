// Package logger owns this process's log output: one format, one sink, one
// ordered stream.
//
// Application code logs through the standard library's log/slog. Third-party
// libraries that insist on their own JSON logger — sipgo, via zerolog — are
// funnelled in through a ForeignWriter, so that everything a service prints
// shares a format and a mutex, and so that each stream keeps its own level.
//
// INVARIANT: nothing reachable from sink.emit may call slog, zerolog, or any
// other logger, and emit swallows write errors rather than reporting them. A
// log sink that logs its own failures recurses.
package logger

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// LevelOff sits above every real level, so nothing passes a filter set to it.
const LevelOff = slog.Level(math.MaxInt32)

// timeLayout is the wall clock every text line starts with.
const timeLayout = "15:04:05"

// Format selects how an event is rendered.
type Format int

const (
	FormatText Format = iota // [15:04:05] [INFO] [tag] message key=value
	FormatJSON               // one JSON object per line
)

// ParseFormat maps a config string to a Format. An unrecognized value returns
// FormatText along with an error, so a caller can complain without going dark.
func ParseFormat(s string) (Format, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "text":
		return FormatText, nil
	case "json":
		return FormatJSON, nil
	default:
		return FormatText, fmt.Errorf("unknown log format %q (want text or json)", s)
	}
}

// event is the one shape both streams are reduced to before formatting.
type event struct {
	t         time.Time
	level     slog.Level
	src       string // "app" for our own code, else the foreign stream's name
	component string // bracket tag; empty for app events, which carry their own
	msg       string
	attrs     []slog.Attr
}

// sink serializes every event onto one writer. The mutex is what keeps the app
// and foreign streams from interleaving mid-line, which is the point of routing
// both through here.
type sink struct {
	mu     sync.Mutex
	out    io.Writer
	format Format
	buf    []byte
}

func (s *sink) setOutput(w io.Writer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.out = w
}

func (s *sink) setFormat(f Format) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.format = f
}

// emit renders one event and writes it. Write errors are dropped on purpose:
// see the package invariant.
func (s *sink) emit(e event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.out == nil {
		return
	}
	s.buf = s.buf[:0]
	if s.format == FormatJSON {
		s.buf = appendJSON(s.buf, e)
	} else {
		s.buf = appendText(s.buf, e)
	}
	_, _ = s.out.Write(s.buf)
}

var (
	// appLevel filters our own slog stream. Atomic rather than mutex-guarded so
	// there is no lock ordering to get wrong between it and the sink.
	appLevel atomic.Int32

	defaultSink = &sink{format: FormatText}
)

func init() { appLevel.Store(int32(slog.LevelDebug)) }

// InitLogger points the default slog logger at this package's sink.
func InitLogger(out io.Writer) {
	defaultSink.setOutput(out)
	slog.SetDefault(slog.New(&customHandler{sink: defaultSink}))
}

// SetOutput redirects the sink. Mainly for tests.
func SetOutput(w io.Writer) { defaultSink.setOutput(w) }

// SetFormat switches between text and JSON rendering for every stream at once.
func SetFormat(f Format) { defaultSink.setFormat(f) }

// SetLevel sets the level for the application's own logs. An unrecognized value
// falls back to info and is reported — a typo should not silently turn the
// firehose on.
func SetLevel(levelStr string) error {
	level, err := ParseLevel(levelStr)
	appLevel.Store(int32(level))
	return err
}

// GetLevel returns the application log level as a string.
func GetLevel() string {
	return strings.ToLower(levelName(slog.Level(appLevel.Load())))
}

// ParseLevel parses a level name. It returns the fallback level and an error for
// anything it does not recognize.
func ParseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	case "off", "none", "silent":
		return LevelOff, nil
	default:
		return slog.LevelInfo, fmt.Errorf("unknown log level %q (want debug, info, warn, error or off)", s)
	}
}

// customHandler is the slog.Handler backing every slog call in the process.
//
// It holds the sink by pointer: WithAttrs copies the handler, and a struct
// carrying a mutex by value cannot be copied.
type customHandler struct {
	sink   *sink
	attrs  []slog.Attr
	groups []string
}

func (h *customHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= slog.Level(appLevel.Load())
}

func (h *customHandler) Handle(_ context.Context, r slog.Record) error {
	if r.Level < slog.Level(appLevel.Load()) {
		return nil
	}
	e := event{
		t:     r.Time,
		level: r.Level,
		src:   "app",
		msg:   r.Message,
	}
	if e.t.IsZero() {
		e.t = time.Now()
	}
	// Attrs from With(...) are already group-prefixed; the record's are not.
	prefix := h.prefix()
	e.attrs = make([]slog.Attr, 0, len(h.attrs)+r.NumAttrs())
	e.attrs = append(e.attrs, h.attrs...)
	r.Attrs(func(a slog.Attr) bool {
		e.attrs = append(e.attrs, prefixAttr(prefix, a))
		return true
	})
	h.sink.emit(e)
	return nil
}

// WithAttrs returns a handler carrying the extra attrs. Returning the receiver
// unchanged — as this package used to — silently discards everything attached
// with slog.Logger.With, including the tenant on every policy decision.
func (h *customHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	prefix := h.prefix()
	next := *h
	next.attrs = make([]slog.Attr, 0, len(h.attrs)+len(attrs))
	next.attrs = append(next.attrs, h.attrs...)
	for _, a := range attrs {
		next.attrs = append(next.attrs, prefixAttr(prefix, a))
	}
	return &next
}

func (h *customHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	next := *h
	next.groups = append(append([]string{}, h.groups...), name)
	return &next
}

func (h *customHandler) prefix() string {
	if len(h.groups) == 0 {
		return ""
	}
	return strings.Join(h.groups, ".") + "."
}

func prefixAttr(prefix string, a slog.Attr) slog.Attr {
	if prefix == "" {
		return a
	}
	a.Key = prefix + a.Key
	return a
}

// ---------------------------------------------------------------------------
// Text rendering
// ---------------------------------------------------------------------------

func appendText(b []byte, e event) []byte {
	// A foreign library can put a whole SIP packet in one message. Keep the
	// first line prefixed so grep and tail still work, and indent the rest
	// rather than orphaning it from its timestamp.
	head, rest, multiline := strings.Cut(e.msg, "\n")

	b = append(b, '[')
	b = e.t.AppendFormat(b, timeLayout)
	b = append(b, "] ["...)
	b = append(b, levelName(e.level)...)
	b = append(b, ']')
	if e.component != "" {
		b = append(b, " ["...)
		b = append(b, e.component...)
		b = append(b, ']')
	}
	b = append(b, ' ')
	b = append(b, head...)
	for _, a := range e.attrs {
		b = appendTextAttr(b, "", a)
	}
	b = append(b, '\n')

	if multiline {
		for _, line := range strings.Split(strings.TrimRight(rest, "\n"), "\n") {
			b = append(b, "    "...)
			b = append(b, line...)
			b = append(b, '\n')
		}
	}
	return b
}

func appendTextAttr(b []byte, prefix string, a slog.Attr) []byte {
	a.Value = a.Value.Resolve()
	if a.Value.Kind() == slog.KindGroup {
		group := a.Value.Group()
		if len(group) == 0 {
			return b
		}
		inner := prefix
		if a.Key != "" {
			inner = prefix + a.Key + "."
		}
		for _, ga := range group {
			b = appendTextAttr(b, inner, ga)
		}
		return b
	}
	if a.Key == "" {
		return b
	}
	b = append(b, ' ')
	b = append(b, prefix...)
	b = append(b, a.Key...)
	b = append(b, '=')
	return appendTextValue(b, a.Value)
}

func appendTextValue(b []byte, v slog.Value) []byte {
	s := v.String()
	if needsQuote(s) {
		return strconv.AppendQuote(b, s)
	}
	return append(b, s...)
}

// needsQuote reports whether a value would break the key=value shape if written
// bare. An unquoted error string turns one attr into five bogus tokens.
func needsQuote(s string) bool {
	if s == "" {
		return true
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; c <= ' ' || c == '"' || c == '=' || c == 0x7f {
			return true
		}
	}
	return false
}

func levelName(l slog.Level) string {
	switch {
	case l >= LevelOff:
		return "OFF"
	default:
		return strings.ToUpper(l.String())
	}
}

// ---------------------------------------------------------------------------
// JSON rendering
// ---------------------------------------------------------------------------

// appendJSON writes one object per event. Attrs live under "fields" rather than
// at the top level, because a foreign stream can and does carry keys that
// collide with the envelope: sipgo emits a field literally named "msg", and our
// own resolver logs a "caller" attr meaning the SIP calling party.
func appendJSON(b []byte, e event) []byte {
	b = append(b, `{"time":`...)
	b = strconv.AppendQuote(b, e.t.Format(time.RFC3339))
	b = append(b, `,"level":`...)
	b = strconv.AppendQuote(b, strings.ToLower(levelName(e.level)))
	b = append(b, `,"src":`...)
	b = strconv.AppendQuote(b, e.src)
	if e.component != "" {
		b = append(b, `,"component":`...)
		b = strconv.AppendQuote(b, e.component)
	}
	b = append(b, `,"msg":`...)
	b = strconv.AppendQuote(b, e.msg)

	if len(e.attrs) > 0 {
		before := len(b)
		b = append(b, `,"fields":{`...)
		n := 0
		for _, a := range e.attrs {
			b, n = appendJSONAttr(b, "", a, n)
		}
		if n == 0 {
			b = b[:before] // every attr was empty; drop the key entirely
		} else {
			b = append(b, '}')
		}
	}
	return append(b, "}\n"...)
}

func appendJSONAttr(b []byte, prefix string, a slog.Attr, n int) ([]byte, int) {
	a.Value = a.Value.Resolve()
	if a.Value.Kind() == slog.KindGroup {
		group := a.Value.Group()
		inner := prefix
		if a.Key != "" {
			inner = prefix + a.Key + "."
		}
		for _, ga := range group {
			b, n = appendJSONAttr(b, inner, ga, n)
		}
		return b, n
	}
	if a.Key == "" {
		return b, n
	}
	if n > 0 {
		b = append(b, ',')
	}
	b = strconv.AppendQuote(b, prefix+a.Key)
	b = append(b, ':')
	return appendJSONValue(b, a.Value), n + 1
}

func appendJSONValue(b []byte, v slog.Value) []byte {
	switch v.Kind() {
	case slog.KindString:
		return strconv.AppendQuote(b, v.String())
	case slog.KindInt64:
		return strconv.AppendInt(b, v.Int64(), 10)
	case slog.KindUint64:
		return strconv.AppendUint(b, v.Uint64(), 10)
	case slog.KindBool:
		return strconv.AppendBool(b, v.Bool())
	case slog.KindFloat64:
		f := v.Float64()
		if math.IsInf(f, 0) || math.IsNaN(f) {
			return strconv.AppendQuote(b, v.String())
		}
		return strconv.AppendFloat(b, f, 'g', -1, 64)
	case slog.KindDuration:
		return strconv.AppendQuote(b, v.Duration().String())
	case slog.KindTime:
		return strconv.AppendQuote(b, v.Time().Format(time.RFC3339Nano))
	default:
		boxed := v.Any()
		if err, ok := boxed.(error); ok {
			return strconv.AppendQuote(b, err.Error())
		}
		if raw, mErr := json.Marshal(boxed); mErr == nil {
			return append(b, raw...)
		}
		return strconv.AppendQuote(b, v.String())
	}
}

// ---------------------------------------------------------------------------
// Foreign streams
// ---------------------------------------------------------------------------

// Foreign field names, as emitted by zerolog's defaults.
const (
	foreignLevelField   = "level"
	foreignTimeField    = "time"
	foreignMessageField = "message"
	foreignCallerField  = "caller"
)

// ForeignWriter adapts a third-party JSON log stream onto our sink. It is an
// io.Writer so a library that only accepts one can be pointed at it.
//
// It carries its own level, held in an atomic rather than on the producing
// logger, because sipgo snapshots its zerolog.Logger by value at NewUA time:
// a level set on that logger could never be changed afterwards, and swapping
// the zerolog global at runtime races the transport goroutines that read it.
// Every snapshot shares this one writer, so the atomic reaches all of them.
type ForeignWriter struct {
	src  string
	sink *sink
	lvl  atomic.Int32
}

// NewForeignWriter returns a writer that labels its events with src.
func NewForeignWriter(src string) *ForeignWriter {
	w := &ForeignWriter{src: src, sink: defaultSink}
	w.lvl.Store(int32(slog.LevelInfo))
	return w
}

// SetLevel sets the threshold for this stream, independent of the app level.
func (w *ForeignWriter) SetLevel(l slog.Level) { w.lvl.Store(int32(l)) }

// Level reports the current threshold for this stream.
func (w *ForeignWriter) Level() slog.Level { return slog.Level(w.lvl.Load()) }

// Write consumes exactly one JSON log event.
//
// It never reports an error. zerolog responds to a writer error by printing to
// os.Stderr once per event, which would resurrect the very unformatted stderr
// stream this package exists to eliminate — and would do it under flood.
func (w *ForeignWriter) Write(p []byte) (int, error) {
	w.consume(p)
	return len(p), nil
}

// consume parses and emits synchronously. It must not retain p: the caller's
// buffer returns to a pool the moment Write returns.
func (w *ForeignWriter) consume(p []byte) {
	trimmed := bytes.TrimSpace(p)
	if len(trimmed) == 0 {
		return
	}
	if trimmed[0] != '{' {
		w.emitRaw(string(trimmed))
		return
	}

	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		w.emitRaw(string(trimmed))
		return
	}

	e := event{src: w.src, level: slog.LevelInfo}
	// Decoding the token stream keeps attrs in document order; ranging a map
	// would reorder them on every line.
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return
		}
		key, ok := keyTok.(string)
		if !ok {
			return
		}
		var raw any
		if err := dec.Decode(&raw); err != nil {
			return
		}
		switch key {
		case foreignLevelField:
			e.level = parseForeignLevel(valueString(raw))
		case foreignTimeField:
			if ts, err := time.Parse(time.RFC3339, valueString(raw)); err == nil {
				e.t = ts
			}
		case foreignMessageField:
			e.msg = valueString(raw)
		case foreignCallerField:
			e.component = valueString(raw)
		default:
			e.attrs = append(e.attrs, slog.Attr{Key: key, Value: foreignValue(raw)})
		}
	}

	if e.level < slog.Level(w.lvl.Load()) {
		return
	}
	if e.t.IsZero() {
		e.t = time.Now()
	}
	if e.component == "" {
		// Most of sipgo's output goes through the package-level zerolog
		// functions and carries no caller at all, the packet dumps included.
		e.component = w.src
	}
	w.sink.emit(e)
}

func (w *ForeignWriter) emitRaw(s string) {
	if slog.LevelInfo < slog.Level(w.lvl.Load()) {
		return
	}
	w.sink.emit(event{
		t:         time.Now(),
		level:     slog.LevelInfo,
		src:       w.src,
		component: w.src,
		msg:       s,
	})
}

func valueString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case nil:
		return ""
	default:
		return fmt.Sprint(t)
	}
}

func foreignValue(v any) slog.Value {
	switch t := v.(type) {
	case string:
		return slog.StringValue(t)
	case bool:
		return slog.BoolValue(t)
	case nil:
		return slog.StringValue("")
	default:
		// json.Number keeps integers as written. Rendering them through a
		// float64 turns a call count into 1.234567e+06.
		return slog.AnyValue(t)
	}
}

// parseForeignLevel maps zerolog's level names onto slog's. zerolog's trace
// folds into debug: slog has no level below debug that renders with a readable
// name, and sipgo has no trace call sites.
func parseForeignLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "trace", "debug":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "warn", "warning":
		return slog.LevelWarn
	case "error", "fatal", "panic":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
