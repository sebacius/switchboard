package dialplan

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/sebas/switchboard/internal/signaling/llm"
)

// AIAgentParams defines configuration for ai_agent action
type AIAgentParams struct {
	Config           string `json:"config"`             // Config file name (without .md) or "${domain}"
	Voice            string `json:"voice"`              // TTS voice (default "alloy")
	Model            string `json:"model"`              // LLM model (default "llama3")
	Greeting         string `json:"greeting"`           // Optional initial greeting
	MaxTurns         int    `json:"max_turns"`          // Max conversation turns (default 10)
	SilenceTimeoutMs int    `json:"silence_timeout_ms"` // Silence detection (default 2000)
	MaxListenMs      int    `json:"max_listen_ms"`      // Max listen duration (default 15000)
	TenantsPath      string `json:"tenants_path"`       // Path to tenant configs (default "resources/tenants")
}

// AIAgentAction runs an AI voice conversation
type AIAgentAction struct {
	params    AIAgentParams
	llmClient *llm.Client
}

// aiAgentFactory holds the LLM client for creating actions
type aiAgentFactory struct {
	llmClient *llm.Client
}

// NewAIAgentActionFactory creates a factory for ai_agent actions
func NewAIAgentActionFactory(llmClient *llm.Client) ActionFactory {
	factory := &aiAgentFactory{llmClient: llmClient}
	return factory.create
}

func (f *aiAgentFactory) create(raw json.RawMessage) (Action, error) {
	var params AIAgentParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, fmt.Errorf("parse ai_agent params: %w", err)
	}

	// Set defaults
	if params.Voice == "" {
		params.Voice = "alloy"
	}
	if params.Model == "" {
		params.Model = "llama3"
	}
	if params.MaxTurns == 0 {
		params.MaxTurns = 10
	}
	if params.SilenceTimeoutMs == 0 {
		params.SilenceTimeoutMs = 2000
	}
	if params.MaxListenMs == 0 {
		params.MaxListenMs = 15000
	}
	if params.TenantsPath == "" {
		params.TenantsPath = "resources/tenants"
	}

	return &AIAgentAction{
		params:    params,
		llmClient: f.llmClient,
	}, nil
}

func (a *AIAgentAction) Type() string { return "ai_agent" }

func (a *AIAgentAction) Execute(ctx context.Context, session CallSession) error {
	logger := slog.With("call_id", session.CallID(), "action", "ai_agent")
	logger.Info("[AIAgent] Starting AI conversation",
		"config", a.params.Config,
		"voice", a.params.Voice,
		"model", a.params.Model,
		"max_turns", a.params.MaxTurns,
	)

	// Check if LLM client is available
	if a.llmClient == nil || !a.llmClient.Ready() {
		logger.Error("[AIAgent] LLM client not configured")
		return fmt.Errorf("LLM server not configured")
	}

	// Load system prompt from config file
	systemPrompt, err := a.loadSystemPrompt(session)
	if err != nil {
		logger.Warn("[AIAgent] Failed to load config, using default", "error", err)
		systemPrompt = "You are a helpful voice assistant. Keep your responses brief and conversational."
	}

	// Create conversation with system prompt and model from dialplan
	conv := a.llmClient.NewConversation(systemPrompt, a.params.Model)

	// Speak greeting
	greeting := a.params.Greeting
	if greeting == "" {
		greeting = "Hello, how can I help you today?"
	}

	logger.Debug("[AIAgent] Speaking greeting", "text", greeting)
	if err := session.PlayTTS(ctx, greeting, a.params.Voice); err != nil {
		logger.Error("[AIAgent] Failed to speak greeting", "error", err)
		return fmt.Errorf("speak greeting: %w", err)
	}

	// Conversation loop
	for turn := 0; turn < a.params.MaxTurns; turn++ {
		// Check if call is still active
		if session.IsTerminated() {
			logger.Info("[AIAgent] Call terminated by remote")
			return nil
		}

		select {
		case <-ctx.Done():
			logger.Info("[AIAgent] Context canceled")
			return nil
		default:
		}

		// Listen for user input
		logger.Debug("[AIAgent] Listening for user input", "turn", turn+1)
		userText, err := session.Listen(ctx, a.params.MaxListenMs, a.params.SilenceTimeoutMs)
		if err != nil {
			logger.Error("[AIAgent] Listen failed", "error", err)
			return fmt.Errorf("listen: %w", err)
		}

		// Check if user said anything
		userText = strings.TrimSpace(userText)
		if userText == "" {
			logger.Debug("[AIAgent] No speech detected, prompting")
			if err := session.PlayTTS(ctx, "I didn't catch that. Could you please repeat?", a.params.Voice); err != nil {
				logger.Warn("[AIAgent] Failed to prompt for repeat", "error", err)
			}
			continue
		}

		logger.Info("[AIAgent] User said", "text", userText, "turn", turn+1)

		// Check for goodbye phrases
		if isGoodbye(userText) {
			logger.Info("[AIAgent] User said goodbye")
			if err := session.PlayTTS(ctx, "Goodbye! Have a great day.", a.params.Voice); err != nil {
				logger.Warn("[AIAgent] Failed to say goodbye", "error", err)
			}
			return nil
		}

		// Send to LLM
		logger.Debug("[AIAgent] Sending to LLM")
		response, err := conv.Say(ctx, userText)
		if err != nil {
			logger.Error("[AIAgent] LLM error", "error", err)
			if err := session.PlayTTS(ctx, "I'm having trouble thinking right now. Let me try again.", a.params.Voice); err != nil {
				logger.Warn("[AIAgent] Failed to speak error message", "error", err)
			}
			continue
		}

		logger.Info("[AIAgent] LLM response", "text", response, "turn", turn+1)

		// Speak response
		if err := session.PlayTTS(ctx, response, a.params.Voice); err != nil {
			logger.Error("[AIAgent] Failed to speak response", "error", err)
			return fmt.Errorf("speak response: %w", err)
		}
	}

	// Max turns reached
	logger.Info("[AIAgent] Max turns reached")
	if err := session.PlayTTS(ctx, "I've enjoyed our conversation, but I need to go now. Goodbye!", a.params.Voice); err != nil {
		logger.Warn("[AIAgent] Failed to say max turns goodbye", "error", err)
	}

	return nil
}

// loadSystemPrompt loads the system prompt from a tenant config file
func (a *AIAgentAction) loadSystemPrompt(session CallSession) (string, error) {
	configName := a.params.Config
	if configName == "" {
		configName = "default"
	}

	// Strip .md extension if already present to avoid double extension
	configName = strings.TrimSuffix(configName, ".md")

	// Build path to config file
	configPath := filepath.Join(a.params.TenantsPath, configName+".md")

	data, err := os.ReadFile(configPath)
	if err != nil {
		return "", fmt.Errorf("read config file %s: %w", configPath, err)
	}

	return string(data), nil
}

// isGoodbye checks if the user text indicates they want to end the call
func isGoodbye(text string) bool {
	text = strings.ToLower(text)
	goodbyePhrases := []string{
		"goodbye",
		"bye",
		"see you",
		"talk to you later",
		"that's all",
		"i'm done",
		"end call",
		"hang up",
		"nothing else",
		"that will be all",
	}

	for _, phrase := range goodbyePhrases {
		if strings.Contains(text, phrase) {
			return true
		}
	}
	return false
}
