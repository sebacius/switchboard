package siplog

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	zerologlog "github.com/rs/zerolog/log"

	"github.com/sebas/switchboard/internal/logger"
)

func TestParseLevelMapsTraceOntoTheDebugStream(t *testing.T) {
	cases := map[string]struct {
		lvl   slog.Level
		trace bool
		bad   bool
	}{
		"trace": {slog.LevelDebug, true, false},
		"debug": {slog.LevelDebug, false, false},
		"info":  {slog.LevelInfo, false, false},
		"warn":  {slog.LevelWarn, false, false},
		"":      {slog.LevelWarn, false, false},
		"error": {slog.LevelError, false, false},
		"off":   {logger.LevelOff, false, false},
		"loud":  {slog.LevelWarn, false, true},
	}
	for in, want := range cases {
		lvl, trace, err := parseLevel(in)
		if lvl != want.lvl || trace != want.trace || (err != nil) != want.bad {
			t.Errorf("parseLevel(%q) = %v, %v, %v; want %v, %v, bad=%v",
				in, lvl, trace, err, want.lvl, want.trace, want.bad)
		}
	}
}

// The whole design rests on this: sipgo's transport and transaction layers copy
// the zerolog global when the user agent is built. Configure must therefore run
// first, and the level must remain reachable afterwards through the shared
// writer — a level set on the snapshotted logger could never be changed.
func TestConfigureReachesLayersBuiltAfterIt(t *testing.T) {
	buf := &bytes.Buffer{}
	logger.SetOutput(buf)
	logger.SetFormat(logger.FormatText)
	t.Cleanup(func() { logger.SetOutput(nil) })

	if err := Configure("debug"); err != nil {
		t.Fatalf("Configure: %v", err)
	}

	// Build the user agent AFTER Configure, as main does.
	ua, err := sipgo.NewUA()
	if err != nil {
		t.Fatalf("NewUA: %v", err)
	}
	defer func() { _ = ua.Close() }()

	buf.Reset()
	ua.TransactionLayer().Close()
	if got := buf.String(); !strings.Contains(got, "[transactionLayer]") {
		t.Errorf("transaction layer did not reach our sink: %q", got)
	}

	// Retuning after construction must still reach that same layer.
	buf.Reset()
	if err := SetLevel("off"); err != nil {
		t.Fatalf("SetLevel: %v", err)
	}
	ua.TransportLayer().Close()
	if buf.Len() != 0 {
		t.Errorf("level change did not reach an already-built layer: %q", buf.String())
	}
}

func TestConfigureTogglesRawMessageDumps(t *testing.T) {
	t.Cleanup(func() { sip.SIPDebug = false })

	if err := Configure("debug"); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	if sip.SIPDebug {
		t.Error("SIPDebug should be off below trace")
	}
	if err := Configure("trace"); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	if !sip.SIPDebug {
		t.Error("SIPDebug should be on at trace")
	}
}

func TestConfigureReportsGarbageAndFallsBackToWarn(t *testing.T) {
	if err := Configure("shouty"); err == nil {
		t.Error("expected an error for an unknown level")
	}
	if Level() != slog.LevelWarn {
		t.Errorf("fallback level = %v, want warn", Level())
	}
}

// zerolog prints to stderr once per event when a write fails and no handler is
// installed, which would resurrect the raw stderr stream this package removes.
func TestConfigureInstallsAnErrorHandler(t *testing.T) {
	if err := Configure("warn"); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	// A write through the configured global must not reach os.Stderr even when
	// the sink is unwritable.
	logger.SetOutput(nil)
	t.Cleanup(func() { logger.SetOutput(nil) })
	zerologlog.Error().Str("caller", "test").Msg("boom")
}
