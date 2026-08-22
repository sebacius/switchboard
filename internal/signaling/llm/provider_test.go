package llm

import (
	"strings"
	"testing"
)

// The split is on the FIRST separator, and a bare value stays Ollama. Both halves
// matter: the first makes gateway model ids (which contain separators) usable,
// the second is what keeps every deployment configured before this existed
// working untouched.
func TestParseModelRef(t *testing.T) {
	tests := []struct {
		name         string
		ref          string
		wantProvider Provider
		wantModel    string
		wantErr      bool
	}{
		{"bare model stays ollama", "qwen3:8b", ProviderOllama, "qwen3:8b", false},
		{"explicit ollama", "ollama/qwen3:8b", ProviderOllama, "qwen3:8b", false},
		{"openai", "openai/gpt-4o", ProviderOpenAI, "gpt-4o", false},
		{"model id keeps its own separators", "openai/meta-llama/llama-3.1-70b", ProviderOpenAI, "meta-llama/llama-3.1-70b", false},
		{"prefix is case folded", "OpenAI/gpt-4o", ProviderOpenAI, "gpt-4o", false},
		{"surrounding whitespace is trimmed", "  openai/gpt-4o  ", ProviderOpenAI, "gpt-4o", false},
		{"namespaced ollama model via the escape hatch", "ollama/hf.co/user/repo:Q4_K_M", ProviderOllama, "hf.co/user/repo:Q4_K_M", false},
		{"bare provider name gets its default model", "openai", ProviderOpenAI, DefaultOpenAIModel, false},
		{"bare ollama gets its default model", "ollama", ProviderOllama, DefaultOllamaModel, false},
		{"bare provider name is case folded", "OpenAI", ProviderOpenAI, DefaultOpenAIModel, false},
		{"trailing separator is the same statement", "openai/", ProviderOpenAI, DefaultOpenAIModel, false},
		{"empty prefix", "/gpt-4o", "", "", true},
		{"empty", "", "", "", true},
		{"unknown provider", "anthropic/claude-3", "", "", true},
		// The regression this change knowingly accepts: a namespaced Ollama name
		// no longer parses bare.
		{"namespaced ollama model bare", "hf.co/user/repo:Q4_K_M", "", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotP, gotM, err := ParseModelRef(tt.ref)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseModelRef(%q) = %q/%q, want an error", tt.ref, gotP, gotM)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseModelRef(%q): %v", tt.ref, err)
			}
			if gotP != tt.wantProvider || gotM != tt.wantModel {
				t.Errorf("ParseModelRef(%q) = %q/%q, want %q/%q", tt.ref, gotP, gotM, tt.wantProvider, tt.wantModel)
			}
		})
	}
}

// The error message IS the migration path for the one input this change breaks,
// so it is load-bearing rather than cosmetic. An operator running a namespaced
// Ollama model must be told the exact string that works.
func TestNamespacedOllamaModelErrorNamesTheFix(t *testing.T) {
	_, _, err := ParseModelRef("hf.co/user/repo:Q4_K_M")
	if err == nil {
		t.Fatal("a bare namespaced model must not be silently accepted")
	}
	if !strings.Contains(err.Error(), "ollama/hf.co/user/repo:Q4_K_M") {
		t.Errorf("error must name the working replacement, got: %v", err)
	}
}

// Adding a third provider without updating the message would leave operators
// guessing, so the message is pinned to the provider list.
func TestUnknownProviderErrorNamesTheValidOnes(t *testing.T) {
	_, _, err := ParseModelRef("anthropic/claude-3")
	if err == nil {
		t.Fatal("expected an error for an unknown provider")
	}
	for _, p := range Providers() {
		if !strings.Contains(err.Error(), string(p)) {
			t.Errorf("error must name provider %q, got: %v", p, err)
		}
	}
}

func TestNewDefaultsEndpointPerProvider(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		want string
	}{
		{"ollama default", Config{Provider: ProviderOllama, Model: "qwen3:8b"}, DefaultOllamaServer},
		{"openai default", Config{Provider: ProviderOpenAI, Model: "gpt-4o", APIKey: "sk-test"}, DefaultOpenAIServer},
		{"empty provider means ollama", Config{Model: "qwen3:8b"}, DefaultOllamaServer},
		{"explicit endpoint wins for ollama", Config{Provider: ProviderOllama, ServerURL: "http://ollama.internal:11434"}, "http://ollama.internal:11434"},
		{"explicit endpoint wins for openai", Config{Provider: ProviderOpenAI, ServerURL: "http://gateway.internal:4000"}, "http://gateway.internal:4000"},
		{"trailing slash is normalized away", Config{Provider: ProviderOllama, ServerURL: "http://ollama.internal:11434/"}, "http://ollama.internal:11434"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := New(tt.cfg)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if got := c.ServerURL(); got != tt.want {
				t.Errorf("ServerURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

// A key is required for the hosted API and optional for anything else: a
// self-hosted gateway with no auth is a normal deployment, and demanding a
// placeholder would only teach operators to invent one.
func TestOpenAIKeyIsRequiredOnlyForTheHostedAPI(t *testing.T) {
	tests := []struct {
		name      string
		serverURL string
		apiKey    string
		wantErr   bool
	}{
		{"hosted by default, no key", "", "", true},
		{"hosted explicitly, no key", "https://api.openai.com", "", true},
		{"trailing slash does not defeat the check", "https://api.openai.com/", "", true},
		{"hosted with a key", "https://api.openai.com", "sk-test", false},
		{"gateway needs no key", "http://gateway.internal:4000", "", false},
		{"gateway with a key", "https://api.groq.com/openai", "sk-test", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := New(Config{
				Provider:  ProviderOpenAI,
				ServerURL: tt.serverURL,
				Model:     "gpt-4o",
				APIKey:    tt.apiKey,
			})
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected a startup error naming the environment variable")
				}
				if !strings.Contains(err.Error(), "OPENAI_API_KEY") {
					t.Errorf("error must name the variable to set, got: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("New: %v", err)
			}
		})
	}
}

// A credential must never reach an error string: errors get logged, wrapped and
// reported, and this is the cheapest place to pin that.
func TestCredentialsNeverAppearInErrors(t *testing.T) {
	const key = "sk-do-not-log-me"
	_, err := New(Config{Provider: "nonsense", APIKey: key, Model: "x"})
	if err == nil {
		t.Fatal("expected an error for an unknown provider")
	}
	if strings.Contains(err.Error(), key) {
		t.Fatalf("the API key leaked into an error: %v", err)
	}
}

func TestOllamaNeedsNoKey(t *testing.T) {
	if _, err := New(Config{Provider: ProviderOllama, Model: "qwen3:8b"}); err != nil {
		t.Fatalf("the Ollama provider must not require a key: %v", err)
	}
}

// "openai" has no separator, so without a special case it would fall through the
// back-compat rule and be looked up as an Ollama model literally named "openai".
// That failure is the confusing kind: the server is healthy and the probe
// complains about a model nobody asked for.
func TestBareProviderNameIsNotReadAsAnOllamaModel(t *testing.T) {
	for _, ref := range []string{"openai", "OpenAI", "openai/"} {
		p, m, err := ParseModelRef(ref)
		if err != nil {
			t.Fatalf("ParseModelRef(%q): %v", ref, err)
		}
		if p != ProviderOpenAI {
			t.Errorf("ParseModelRef(%q) provider = %q, want %q", ref, p, ProviderOpenAI)
		}
		if m != DefaultOpenAIModel {
			t.Errorf("ParseModelRef(%q) model = %q, want the provider default %q", ref, m, DefaultOpenAIModel)
		}
	}
}

// The short form delegates the model choice to a constant in this package, so an
// operator can only see what they got from the resolved model — which is why the
// banner prints it. Pin the defaults here so changing one is a deliberate edit
// with a visible diff, not a drive-by.
func TestProviderDefaultsArePinned(t *testing.T) {
	tests := []struct {
		provider   Provider
		wantModel  string
		wantServer string
	}{
		{ProviderOllama, "qwen3:8b", DefaultOllamaServer},
		{ProviderOpenAI, "gpt-4o", DefaultOpenAIServer},
	}
	for _, tt := range tests {
		if got := DefaultModel(tt.provider); got != tt.wantModel {
			t.Errorf("DefaultModel(%q) = %q, want %q", tt.provider, got, tt.wantModel)
		}
		if got := DefaultServerURL(tt.provider); got != tt.wantServer {
			t.Errorf("DefaultServerURL(%q) = %q, want %q", tt.provider, got, tt.wantServer)
		}
	}
}

// The short form must land on a fully resolved, usable configuration: right
// provider, right endpoint, right model, nothing left implicit.
func TestBareProviderResolvesEndpointAndModelTogether(t *testing.T) {
	p, m, err := ParseModelRef("openai")
	if err != nil {
		t.Fatalf("ParseModelRef: %v", err)
	}
	c, err := New(Config{Provider: p, Model: m, APIKey: "sk-test"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := c.ServerURL(); got != DefaultOpenAIServer {
		t.Errorf("ServerURL() = %q, want %q", got, DefaultOpenAIServer)
	}
	if c.Provider() != ProviderOpenAI {
		t.Errorf("Provider() = %q, want %q", c.Provider(), ProviderOpenAI)
	}
	if m != DefaultOpenAIModel {
		t.Errorf("model = %q, want %q", m, DefaultOpenAIModel)
	}
}
