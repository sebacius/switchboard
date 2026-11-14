// Package siplog binds sipgo's zerolog output to this process's log sink.
//
// It exists as its own package to keep internal/logger free of any sipgo or
// zerolog import: the RTP manager has no SIP stack, and should not link one in
// just to configure logging.
//
// Ordering matters. sipgo's transport and transaction layers copy the zerolog
// package global when the user agent is constructed, so Configure must run
// before sipgo.NewUA or those layers keep writing raw JSON to stderr.
package siplog

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/emiago/sipgo/sip"
	"github.com/rs/zerolog"
	zerologlog "github.com/rs/zerolog/log"

	"github.com/sebas/switchboard/internal/logger"
)

// writer is shared by every logger sipgo snapshots, which is what makes the
// level reachable after construction.
var writer = logger.NewForeignWriter("sip")

// Configure routes sipgo's logging into the shared sink at the given level.
//
// Levels are one ordered dial rather than a level plus a separate trace switch:
// sipgo's packet dumps are emitted at debug through the same logger, so two
// knobs could contradict each other and one would have to silently win.
//
//	trace  raw SIP messages, plus everything below
//	debug  transaction and transport internals
//	info   connection lifecycle
//	warn   the default: only when something is actually wrong
//	error  failures only
//	off    nothing
//
// An unrecognized value falls back to warn and is returned as an error.
func Configure(level string) error {
	lvl, trace, err := parseLevel(level)

	// zerolog prints to stderr once per event when a write fails and no handler
	// is installed. Our writer never reports failure, but a nil handler is one
	// upstream change away from a flood.
	zerolog.ErrorHandler = func(error) {}

	writer.SetLevel(lvl)
	sip.SIPDebug = trace
	zerologlog.Logger = zerolog.New(writer).With().Timestamp().Logger()
	return err
}

// Level reports the level currently applied to the SIP stream.
func Level() slog.Level { return writer.Level() }

// SetLevel retunes the SIP stream after Configure. It reaches the layers sipgo
// already built, because they all share the one writer.
func SetLevel(level string) error {
	lvl, trace, err := parseLevel(level)
	writer.SetLevel(lvl)
	sip.SIPDebug = trace
	return err
}

func parseLevel(s string) (lvl slog.Level, trace bool, err error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "trace":
		return slog.LevelDebug, true, nil
	case "debug":
		return slog.LevelDebug, false, nil
	case "info":
		return slog.LevelInfo, false, nil
	case "", "warn", "warning":
		return slog.LevelWarn, false, nil
	case "error":
		return slog.LevelError, false, nil
	case "off", "none", "silent":
		return logger.LevelOff, false, nil
	default:
		return slog.LevelWarn, false, fmt.Errorf(
			"unknown SIP log level %q (want trace, debug, info, warn, error or off)", s)
	}
}
