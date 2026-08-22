package llm

import (
	"fmt"
	"strings"
	"time"
)

// Provider names an LLM backend. It is selected by the prefix on --llm-model
// rather than by a flag of its own: two ways to say the same thing is how a
// deployment ends up with a provider and a model that disagree, and the model id
// is what an operator actually edits when they change providers.
type Provider string

const (
	ProviderOllama Provider = "ollama"
	ProviderOpenAI Provider = "openai"
)

// DefaultProvider is what a model identifier with no prefix means. It is Ollama
// because that is what every deployment configured before this existed passes,
// and those must keep working untouched.
const DefaultProvider = ProviderOllama

// Per-provider default endpoints, used when none is configured explicitly.
const (
	DefaultOllamaServer = "http://localhost:11434"
	DefaultOpenAIServer = "https://api.openai.com"
)

// Providers lists the valid prefixes, in the order error messages should name
// them.
func Providers() []Provider { return []Provider{ProviderOllama, ProviderOpenAI} }

func providerList() string {
	names := make([]string, 0, len(Providers()))
	for _, p := range Providers() {
		names = append(names, string(p))
	}
	return strings.Join(names, ", ")
}

// DefaultServerURL is the endpoint used for p when none was configured.
func DefaultServerURL(p Provider) string {
	switch p {
	case ProviderOpenAI:
		return DefaultOpenAIServer
	default:
		return DefaultOllamaServer
	}
}

// Per-provider default models, used when the identifier names a provider but no
// model ("--llm-model openai").
//
// These are a convenience with a cost worth stating: an operator who writes the
// short form has delegated the model choice to this file, so changing either
// constant changes the model, the latency and the per-minute cost of every
// deployment using it, without their config changing at all. Treat them as part
// of the public interface, and expect the resolved model on the startup banner to
// be the thing anyone actually reads.
const (
	DefaultOllamaModel = "qwen3:8b"
	DefaultOpenAIModel = "gpt-4o"
)

// DefaultModel is the model used for p when the identifier named no model.
func DefaultModel(p Provider) string {
	switch p {
	case ProviderOpenAI:
		return DefaultOpenAIModel
	default:
		return DefaultOllamaModel
	}
}

// knownProvider reports whether name is a provider we support, case-insensitively.
func knownProvider(name string) (Provider, bool) {
	p := Provider(strings.ToLower(strings.TrimSpace(name)))
	for _, known := range Providers() {
		if p == known {
			return p, true
		}
	}
	return "", false
}

// ParseModelRef splits a model identifier into its provider and model id.
//
// The split is on the FIRST separator, because a model id may contain more of
// them: "openai/meta-llama/llama-3.1-70b" is provider openai, model
// "meta-llama/llama-3.1-70b". A value with no separator is DefaultProvider,
// which is what every existing deployment passes.
//
// A value that is JUST a provider name — "openai", or "openai/" — selects that
// provider with its default model. The trap it avoids is that "openai" has no
// separator, so without a special case it would be read as an Ollama model named
// "openai" and fail at the probe with a message about a model nobody asked for.
// The cost is that an Ollama model actually named "openai" or "ollama" now needs
// the explicit form ("ollama/openai"), which is the same trade the prefix rule
// already makes elsewhere.
//
// An unrecognized prefix is an error rather than a guess. The tempting fallback
// — "treat it as part of an Ollama model name" — silently routes an entire
// deployment's calls to Ollama under the name "openia/gpt-4o" when someone
// fat-fingers the provider. Refusing to start is the cheaper failure. The cost
// is that a namespaced Ollama name (hf.co/user/repo) now needs an explicit
// prefix, so the error carries the exact replacement: that message IS the
// migration path.
func ParseModelRef(ref string) (Provider, string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", "", fmt.Errorf("no model configured: the supervisor needs a model (e.g. %q or %q)",
			"qwen3:8b", "openai/gpt-4o")
	}

	prefix, rest, hasSep := strings.Cut(ref, "/")
	if !hasSep {
		// A bare provider name names a provider, not a model. Without this it
		// would fall through to the no-separator rule below and be looked up as
		// an Ollama model literally called "openai" — silently wrong, which is
		// the outcome this whole function exists to avoid.
		if p, ok := knownProvider(ref); ok {
			return p, DefaultModel(p), nil
		}
		return DefaultProvider, ref, nil
	}

	if p, ok := knownProvider(prefix); ok {
		// "openai/" is the same statement as "openai": a provider with no model.
		if rest = strings.TrimSpace(rest); rest == "" {
			return p, DefaultModel(p), nil
		}
		return p, rest, nil
	}

	return "", "", fmt.Errorf(
		"model %q: unknown provider %q (valid: %s). "+
			"If %q is an Ollama model whose name contains a slash, write it as %q",
		ref, prefix, providerList(), ref, string(ProviderOllama)+"/"+ref)
}

// Config is the provider-agnostic client configuration.
//
// APIKey is passed in here and stored only inside the client that needs it. It
// deliberately has no home in the signaling server's own Config struct, which is
// echoed by the startup banner and passed around freely: a secret that never
// enters that struct cannot leak from it.
type Config struct {
	Provider  Provider      // empty means DefaultProvider
	ServerURL string        // empty means the provider's default
	Model     string        // model used when ChatNative is called with ""
	Timeout   time.Duration // HTTP client timeout; 0 means the package default
	KeepAlive string        // ollama only; ignored by openai
	APIKey    string        // openai only; never a flag, never logged
}

// New builds the client for cfg.Provider. It validates eagerly, so a
// misconfigured supervisor fails at boot rather than on the first caller.
func New(cfg Config) (Client, error) {
	if cfg.Provider == "" {
		cfg.Provider = DefaultProvider
	}
	if cfg.ServerURL == "" {
		cfg.ServerURL = DefaultServerURL(cfg.Provider)
	}
	cfg.ServerURL = strings.TrimRight(strings.TrimSpace(cfg.ServerURL), "/")

	switch cfg.Provider {
	case ProviderOllama:
		return NewOllamaClient(cfg), nil
	case ProviderOpenAI:
		return newOpenAIClient(cfg)
	default:
		return nil, fmt.Errorf("unknown LLM provider %q (valid: %s)", cfg.Provider, providerList())
	}
}
