package logger

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// TUIHandler is the interface for TUI log output
type TUIHandler interface {
	Write(level slog.Level, message string)
}

var (
	globalLevel  = slog.LevelDebug
	tuiHandler   TUIHandler
	handlerMutex sync.RWMutex
)

// JSONParsingWriter wraps an io.Writer and converts JSON logs to our format
type JSONParsingWriter struct {
	base io.Writer
}

// Write implements io.Writer and parses JSON logs
func (w *JSONParsingWriter) Write(p []byte) (int, error) {
	line := string(p)

	// Check if this is a JSON log line (from sipgo)
	if strings.HasPrefix(strings.TrimSpace(line), "{") {
		var logEntry map[string]interface{}
		if err := json.Unmarshal(p, &logEntry); err == nil {
			// Successfully parsed JSON, reformat it
			level := "info"
			if lv, ok := logEntry["level"]; ok {
				level = fmt.Sprint(lv)
			}

			message := "unknown"
			if msg, ok := logEntry["message"]; ok {
				message = fmt.Sprint(msg)
			}

			timestamp := time.Now().Format("15:04:05")
			if t, ok := logEntry["time"]; ok {
				// Try to parse the time field
				if ts, err := time.Parse(time.RFC3339, fmt.Sprint(t)); err == nil {
					timestamp = ts.Format("15:04:05")
				}
			}

			// Collect attributes (excluding standard fields)
			var attrs []string
			for k, v := range logEntry {
				if k != "level" && k != "message" && k != "time" && k != "caller" {
					attrs = append(attrs, fmt.Sprintf("%s=%v", k, v))
				}
			}

			formatted := fmt.Sprintf("[%s] [%s] %s", timestamp, strings.ToUpper(level), message)
			if len(attrs) > 0 {
				formatted += " " + strings.Join(attrs, " ")
			}
			formatted += "\n"

			return w.base.Write([]byte(formatted))
		}
	}

	// Not JSON or failed to parse, write as-is
	return w.base.Write(p)
}

// SetLevel sets the global log level
func SetLevel(levelStr string) {
	level := ParseLevel(levelStr)
	handlerMutex.Lock()
	defer handlerMutex.Unlock()
	globalLevel = level
}

// GetLevel returns the current log level as a string
func GetLevel() string {
	handlerMutex.RLock()
	defer handlerMutex.RUnlock()

	switch globalLevel {
	case slog.LevelDebug:
		return "debug"
	case slog.LevelInfo:
		return "info"
	case slog.LevelWarn:
		return "warn"
	case slog.LevelError:
		return "error"
	default:
		return "debug"
	}
}

// ParseLevel parses a string to an slog level
func ParseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelDebug
	}
}

// AddTUIHandler adds a TUI handler for log output
func AddTUIHandler(handler TUIHandler) {
	handlerMutex.Lock()
	defer handlerMutex.Unlock()
	tuiHandler = handler
}

// customHandler supports multiple outputs with level filtering
type customHandler struct {
	outs []io.Writer // Can write to multiple outputs (stdout, file, etc.)
	mu   sync.Mutex
}

// MultiLevelHandler allows different log levels for different outputs
type MultiLevelHandler struct {
	outputs map[io.Writer]slog.Level
	mu      sync.Mutex
}

// NewMultiLevelHandler creates a handler with different levels per output
func NewMultiLevelHandler(outputs map[io.Writer]slog.Level) *MultiLevelHandler {
	return &MultiLevelHandler{
		outputs: outputs,
	}
}

// Handle implements slog.Handler with per-output level filtering
func (h *MultiLevelHandler) Handle(ctx context.Context, record slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	// Check global level
	handlerMutex.RLock()
	if record.Level < globalLevel {
		handlerMutex.RUnlock()
		return nil
	}
	handlerMutex.RUnlock()

	// Format the log message
	timestamp := record.Time.Format("15:04:05")
	levelStr := record.Level.String()
	message := record.Message

	// Add attributes to message if any
	var attrs []string
	record.Attrs(func(a slog.Attr) bool {
		if a.Key != "time" && a.Key != "level" && a.Key != "msg" {
			attrs = append(attrs, a.Key+"="+a.Value.String())
		}
		return true
	})

	if len(attrs) > 0 {
		message = message + " " + strings.Join(attrs, " ")
	}

	// Write to outputs that meet their level requirement
	formattedLog := "[" + timestamp + "] [" + strings.ToUpper(levelStr) + "] " + message + "\n"
	for out, outLevel := range h.outputs {
		if record.Level >= outLevel && out != nil {
			_, _ = out.Write([]byte(formattedLog))
		}
	}

	return nil
}

// WithAttrs implements slog.Handler
func (h *MultiLevelHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return h
}

// WithGroup implements slog.Handler
func (h *MultiLevelHandler) WithGroup(name string) slog.Handler {
	return h
}

// Enabled implements slog.Handler
func (h *MultiLevelHandler) Enabled(ctx context.Context, level slog.Level) bool {
	h.mu.Lock()
	defer h.mu.Unlock()

	handlerMutex.RLock()
	defer handlerMutex.RUnlock()

	// Enabled if any output accepts this level
	for _, outLevel := range h.outputs {
		if level >= outLevel && level >= globalLevel {
			return true
		}
	}
	return false
}

// Handle implements slog.Handler
func (h *customHandler) Handle(ctx context.Context, record slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	// Check if we should log this level
	handlerMutex.RLock()
	if record.Level < globalLevel {
		handlerMutex.RUnlock()
		return nil
	}
	handlerMutex.RUnlock()

	// Format the log message
	timestamp := record.Time.Format("15:04:05")
	levelStr := record.Level.String()
	message := record.Message

	// Add attributes to message if any
	var attrs []string
	record.Attrs(func(a slog.Attr) bool {
		if a.Key != "time" && a.Key != "level" && a.Key != "msg" {
			attrs = append(attrs, a.Key+"="+a.Value.String())
		}
		return true
	})

	if len(attrs) > 0 {
		message = message + " " + strings.Join(attrs, " ")
	}

	// Write to all outputs
	if len(h.outs) > 0 {
		formattedLog := "[" + timestamp + "] [" + strings.ToUpper(levelStr) + "] " + message + "\n"
		for _, out := range h.outs {
			if out != nil {
				_, _ = out.Write([]byte(formattedLog))
			}
		}
	}

	// Send to TUI handler if available
	handlerMutex.RLock()
	if tuiHandler != nil {
		handlerMutex.RUnlock()
		tuiHandler.Write(record.Level, message)
	} else {
		handlerMutex.RUnlock()
	}

	return nil
}

// WithAttrs implements slog.Handler
func (h *customHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return h
}

// WithGroup implements slog.Handler
func (h *customHandler) WithGroup(name string) slog.Handler {
	return h
}

// Enabled implements slog.Handler
func (h *customHandler) Enabled(ctx context.Context, level slog.Level) bool {
	handlerMutex.RLock()
	defer handlerMutex.RUnlock()
	return level >= globalLevel
}

// InitLogger initializes the global logger with one or more output writers
func InitLogger(outputs ...io.Writer) {
	// Wrap outputs with JSON parser to reformat sipgo logs
	wrappedOutputs := make([]io.Writer, len(outputs))
	for i, out := range outputs {
		wrappedOutputs[i] = &JSONParsingWriter{base: out}
	}

	handler := &customHandler{
		outs: wrappedOutputs,
	}

	logger := slog.New(handler)
	slog.SetDefault(logger)
}

// InitLoggerWithLevels initializes logger with different levels for different outputs
func InitLoggerWithLevels(outputs map[io.Writer]slog.Level) {
	handler := NewMultiLevelHandler(outputs)
	logger := slog.New(handler)
	slog.SetDefault(logger)
}

// Convenience functions that use the default logger
func Debug(msg string, args ...any) {
	slog.Debug(msg, args...)
}

func Info(msg string, args ...any) {
	slog.Info(msg, args...)
}

func Warn(msg string, args ...any) {
	slog.Warn(msg, args...)
}

func Error(msg string, args ...any) {
	slog.Error(msg, args...)
}
