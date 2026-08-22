package config

import (
	"flag"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/sebas/switchboard/internal/signaling/llm"
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
	for _, k := range []string{"LLM_SERVER", "LLM_MODEL", "LLM_KEEP_ALIVE"} {
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

// The default command line is what every deployment configured before providers
// existed passes, and it must resolve exactly as it did then.
func TestDefaultsAreUnchangedForOllama(t *testing.T) {
	cfg, err := load(t)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.LLMProvider != llm.ProviderOllama {
		t.Errorf("provider = %q, want %q", cfg.LLMProvider, llm.ProviderOllama)
	}
	if cfg.LLMModel != "qwen3:8b" {
		t.Errorf("model = %q, want qwen3:8b", cfg.LLMModel)
	}
	if cfg.LLMServerURL != llm.DefaultOllamaServer {
		t.Errorf("server = %q, want %q", cfg.LLMServerURL, llm.DefaultOllamaServer)
	}
}

func TestProviderPrefixSelectsTheEndpoint(t *testing.T) {
	tests := []struct {
		name         string
		args         []string
		wantProvider llm.Provider
		wantModel    string
		wantServer   string
	}{
		{
			name:         "openai defaults to the hosted API",
			args:         []string{"--llm-model", "openai/gpt-4o"},
			wantProvider: llm.ProviderOpenAI, wantModel: "gpt-4o", wantServer: llm.DefaultOpenAIServer,
		},
		{
			name:         "explicit ollama keeps its own default",
			args:         []string{"--llm-model", "ollama/qwen3:8b"},
			wantProvider: llm.ProviderOllama, wantModel: "qwen3:8b", wantServer: llm.DefaultOllamaServer,
		},
		{
			name:         "an explicit endpoint overrides the provider default",
			args:         []string{"--llm-model", "openai/llama-3.3-70b", "--llm-server", "http://gateway.internal:4000"},
			wantProvider: llm.ProviderOpenAI, wantModel: "llama-3.3-70b", wantServer: "http://gateway.internal:4000",
		},
		{
			name:         "a bare provider name resolves endpoint and model together",
			args:         []string{"--llm-model", "openai"},
			wantProvider: llm.ProviderOpenAI, wantModel: llm.DefaultOpenAIModel, wantServer: llm.DefaultOpenAIServer,
		},
		{
			name:         "a bare ollama resolves the same way",
			args:         []string{"--llm-model", "ollama"},
			wantProvider: llm.ProviderOllama, wantModel: llm.DefaultOllamaModel, wantServer: llm.DefaultOllamaServer,
		},
		{
			name:         "a bare provider still honors an explicit endpoint",
			args:         []string{"--llm-model", "openai", "--llm-server", "http://gateway.internal:4000"},
			wantProvider: llm.ProviderOpenAI, wantModel: llm.DefaultOpenAIModel, wantServer: "http://gateway.internal:4000",
		},
		{
			name:         "a gateway model id keeps its own separators",
			args:         []string{"--llm-model", "openai/meta-llama/llama-3.1-70b"},
			wantProvider: llm.ProviderOpenAI, wantModel: "meta-llama/llama-3.1-70b", wantServer: llm.DefaultOpenAIServer,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := load(t, tt.args...)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.LLMProvider != tt.wantProvider {
				t.Errorf("provider = %q, want %q", cfg.LLMProvider, tt.wantProvider)
			}
			if cfg.LLMModel != tt.wantModel {
				t.Errorf("model = %q, want %q", cfg.LLMModel, tt.wantModel)
			}
			if cfg.LLMServerURL != tt.wantServer {
				t.Errorf("server = %q, want %q", cfg.LLMServerURL, tt.wantServer)
			}
		})
	}
}

// An operator whose OpenAI-compatible gateway happens to listen on Ollama's port
// must not be silently redirected to api.openai.com. That is why explicitness is
// decided by whether the flag was passed, not by comparing its value.
func TestExplicitEndpointIsHonouredEvenWhenItMatchesAnotherDefault(t *testing.T) {
	cfg, err := load(t, "--llm-model", "openai/gpt-4o", "--llm-server", llm.DefaultOllamaServer)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.LLMServerURL != llm.DefaultOllamaServer {
		t.Errorf("server = %q, want the explicitly configured %q", cfg.LLMServerURL, llm.DefaultOllamaServer)
	}
}

func TestEnvironmentCanSelectTheProviderAndEndpoint(t *testing.T) {
	t.Run("LLM_MODEL carries the prefix", func(t *testing.T) {
		cfg, err := loadWith(t, map[string]string{"LLM_MODEL": "openai/gpt-4o-mini"})
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.LLMProvider != llm.ProviderOpenAI || cfg.LLMModel != "gpt-4o-mini" {
			t.Errorf("got %q/%q, want openai/gpt-4o-mini", cfg.LLMProvider, cfg.LLMModel)
		}
	})

	// The environment is explicit by construction: nothing sets LLM_SERVER by
	// accident, so it must suppress the provider default the same way the flag does.
	t.Run("LLM_SERVER counts as explicit", func(t *testing.T) {
		cfg, err := loadWith(t,
			map[string]string{"LLM_SERVER": "http://gateway.internal:4000"},
			"--llm-model", "openai/gpt-4o")
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.LLMServerURL != "http://gateway.internal:4000" {
			t.Errorf("server = %q, want the environment's value", cfg.LLMServerURL)
		}
	})
}

// A bad model identifier must stop startup before the banner prints values it
// could not resolve.
func TestBadModelIdentifierIsAStartupError(t *testing.T) {
	tests := []struct {
		name string
		ref  string
		want string
	}{
		{"unknown provider", "anthropic/claude-3", "ollama, openai"},
		{"namespaced ollama model", "hf.co/user/repo", "ollama/hf.co/user/repo"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := load(t, "--llm-model", tt.ref)
			if err == nil {
				t.Fatalf("Load(%q) succeeded, want a startup error", tt.ref)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %v, want it to mention %q", err, tt.want)
			}
		})
	}
}

// Silently discarding an explicitly-set flag costs someone an afternoon.
func TestKeepAliveWarnsWhenTheProviderIgnoresIt(t *testing.T) {
	cfg, err := load(t, "--llm-model", "openai/gpt-4o", "--llm-keep-alive", "1h")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.KeepAliveIgnoredWarning == "" {
		t.Error("--llm-keep-alive was set for a provider that ignores it, with no warning")
	}

	cfg, err = load(t, "--llm-model", "openai/gpt-4o")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.KeepAliveIgnoredWarning != "" {
		t.Errorf("warned about a flag that was never set: %q", cfg.KeepAliveIgnoredWarning)
	}

	cfg, err = load(t, "--llm-keep-alive", "1h")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.KeepAliveIgnoredWarning != "" {
		t.Errorf("warned about a flag Ollama actually honors: %q", cfg.KeepAliveIgnoredWarning)
	}
}
