package config

import (
	"flag"
	"io"
	"os"
	"testing"
)

// loadWith runs Load() with a fresh flag set, the given command line, and the
// given environment. Load reads the global flag set, which can only be parsed
// once per process, so each case needs its own.
func loadWith(t *testing.T, env map[string]string, args ...string) (*Config, error) {
	t.Helper()

	oldArgs, oldFlags := os.Args, flag.CommandLine
	t.Cleanup(func() { os.Args, flag.CommandLine = oldArgs, oldFlags })

	flag.CommandLine = flag.NewFlagSet("signaling", flag.ContinueOnError)
	flag.CommandLine.SetOutput(io.Discard)
	os.Args = append([]string{"signaling"}, args...)

	// Ambient environment must not decide the outcome of a config test.
	for _, k := range []string{
		"TTS_VOICE", "TENANTS_PATH", "ROUTING_PATH", "POLICY_CONFIG",
		"LOGLEVEL", "SIP_LOGLEVEL", "SIP_TRACE", "LOG_FORMAT",
	} {
		t.Setenv(k, "")
	}
	for k, v := range env {
		t.Setenv(k, v)
	}
	return Load()
}

func load(t *testing.T, args ...string) (*Config, error) {
	t.Helper()
	return loadWith(t, nil, args...)
}

// Routing and flow files live beside the rest of a tenant's configuration
// unless a deployment mounts them separately. An empty --routing-path is the
// common case and must not leave the resolved path empty.
func TestRoutingPathDefaultsToTenantsPath(t *testing.T) {
	cfg, err := load(t)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.TenantsPath != "resources/tenants" {
		t.Errorf("tenants path = %q, want resources/tenants", cfg.TenantsPath)
	}
	if cfg.RoutingPath != cfg.TenantsPath {
		t.Errorf("routing path = %q, want it to default to the tenants path %q",
			cfg.RoutingPath, cfg.TenantsPath)
	}
}

func TestRoutingPathCanBeMountedSeparately(t *testing.T) {
	cfg, err := load(t, "--routing-path", "/etc/switchboard/routing")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.RoutingPath != "/etc/switchboard/routing" {
		t.Errorf("routing path = %q, want the explicit value", cfg.RoutingPath)
	}
	if cfg.TenantsPath != "resources/tenants" {
		t.Errorf("tenants path = %q, want it left at its own default", cfg.TenantsPath)
	}
}

// The voice is the one piece of the old supervisor configuration that outlives
// it: flow prompts that do not name a voice fall back to this one.
func TestTTSVoiceDefaultsAndIsOverridable(t *testing.T) {
	cfg, err := load(t)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.TTSVoice != "alloy" {
		t.Errorf("voice = %q, want alloy", cfg.TTSVoice)
	}

	cfg, err = loadWith(t, map[string]string{"TTS_VOICE": "echo"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.TTSVoice != "echo" {
		t.Errorf("voice = %q, want the environment override echo", cfg.TTSVoice)
	}
}

// Routing must not depend on a language model, so no flag may reintroduce one.
// This asserts against the registered flag set rather than Load's error,
// because Load does not surface flag.Parse's error to its caller.
func TestNoSupervisorConfigurationSurvives(t *testing.T) {
	if _, err := load(t); err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, name := range []string{
		"llm-server", "llm-model", "llm-keep-alive",
		"turn-timeout", "first-turn-timeout", "settings-path",
	} {
		if f := flag.CommandLine.Lookup(name); f != nil {
			t.Errorf("--%s is still registered; the supervisor configuration should be gone", name)
		}
	}
	// The voice survives on purpose — flow prompts use it.
	if f := flag.CommandLine.Lookup("tts-voice"); f == nil {
		t.Error("--tts-voice should still be registered for flow prompts")
	}
}

// The SIP stack logs per-packet, so it defaults quieter than the application
// and is tuned separately.
func TestSIPLogLevelDefaultsToWarnAndIsOverridable(t *testing.T) {
	cfg, err := load(t)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.SIPLogLevel != "warn" {
		t.Errorf("default SIPLogLevel = %q, want warn", cfg.SIPLogLevel)
	}
	if cfg.LogFormat != "text" {
		t.Errorf("default LogFormat = %q, want text", cfg.LogFormat)
	}

	cfg, err = load(t, "--sip-loglevel", "debug")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.SIPLogLevel != "debug" {
		t.Errorf("flag SIPLogLevel = %q, want debug", cfg.SIPLogLevel)
	}

	cfg, err = loadWith(t, map[string]string{"SIP_LOGLEVEL": "error"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.SIPLogLevel != "error" {
		t.Errorf("env SIPLogLevel = %q, want error", cfg.SIPLogLevel)
	}
}

// SIP_TRACE is a shorthand for the loudest SIP level, and wins over a level set
// alongside it — the two would otherwise contradict each other.
func TestSIPTraceIsShorthandForTrace(t *testing.T) {
	for _, v := range []string{"1", "true", "yes", "on"} {
		cfg, err := loadWith(t, map[string]string{"SIP_TRACE": v})
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.SIPLogLevel != "trace" {
			t.Errorf("SIP_TRACE=%s gave %q, want trace", v, cfg.SIPLogLevel)
		}
	}
	for _, v := range []string{"", "0", "false", "off"} {
		cfg, err := loadWith(t, map[string]string{"SIP_TRACE": v})
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.SIPLogLevel == "trace" {
			t.Errorf("SIP_TRACE=%q should not enable trace", v)
		}
	}
	cfg, err := loadWith(t, map[string]string{"SIP_TRACE": "1", "SIP_LOGLEVEL": "warn"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.SIPLogLevel != "trace" {
		t.Errorf("SIP_TRACE alongside a level gave %q, want trace", cfg.SIPLogLevel)
	}
}

func TestLogFormatIsOverridable(t *testing.T) {
	cfg, err := load(t, "--log-format", "json")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.LogFormat != "json" {
		t.Errorf("flag LogFormat = %q, want json", cfg.LogFormat)
	}

	cfg, err = loadWith(t, map[string]string{"LOG_FORMAT": "json"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.LogFormat != "json" {
		t.Errorf("env LogFormat = %q, want json", cfg.LogFormat)
	}
}
