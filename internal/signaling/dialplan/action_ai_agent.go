package dialplan

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sebas/switchboard/internal/signaling/llm"
	"github.com/sebas/switchboard/internal/signaling/parking"
)

// AIAgentParams defines configuration for ai_agent action
type AIAgentParams struct {
	Config           string `json:"config"`             // Config file name (without .md) or "${domain}"
	Voice            string `json:"voice"`              // TTS voice (default "alloy")
	Model            string `json:"model"`              // LLM model (default "llama3")
	Greeting         string `json:"greeting"`           // Optional initial greeting
	Mode             string `json:"mode"`               // "conversational" (default) or "routing"
	MaxTurns         int    `json:"max_turns"`          // Max conversation turns (default 10)
	SilenceTimeoutMs int    `json:"silence_timeout_ms"` // Silence detection (default 2000)
	MaxListenMs      int    `json:"max_listen_ms"`      // Max listen duration (default 15000)
	TenantsPath      string `json:"tenants_path"`       // Path to tenant configs (default "resources/tenants")
}

// AIAgentAction runs an AI voice conversation
type AIAgentAction struct {
	params          AIAgentParams
	llmClient       *llm.Client
	parkService     *parking.Service
	settingsContent string // cached settings.md content from factory
}

// AIAgentFactory holds the LLM client for creating actions.
// Exported to allow settings reload at runtime.
type AIAgentFactory struct {
	llmClient       *llm.Client
	parkService     *parking.Service
	settingsPath    string
	mu              sync.RWMutex
	settingsContent string // settings.md loaded at startup, refreshable via ReloadSettings
}

// NewAIAgentActionFactory creates a factory for ai_agent actions.
// settingsPath is read once at creation time and cached for all subsequent actions.
func NewAIAgentActionFactory(llmClient *llm.Client, parkService *parking.Service, settingsPath string) *AIAgentFactory {
	if settingsPath == "" {
		settingsPath = "resources/config"
	}

	factory := &AIAgentFactory{
		llmClient:   llmClient,
		parkService: parkService,
		settingsPath: settingsPath,
	}

	settingsFile := filepath.Join(settingsPath, "settings.md")
	if data, err := os.ReadFile(settingsFile); err == nil {
		factory.settingsContent = strings.TrimSpace(string(data))
		slog.Info("[AIAgent] Settings loaded at startup", "path", settingsFile)
	} else {
		slog.Warn("[AIAgent] Settings file not found, will use defaults", "path", settingsFile, "error", err)
	}

	return factory
}

// ReloadSettings re-reads settings.md from disk and updates the cached content.
func (f *AIAgentFactory) ReloadSettings() error {
	settingsFile := filepath.Join(f.settingsPath, "settings.md")
	data, err := os.ReadFile(settingsFile)
	if err != nil {
		return fmt.Errorf("read settings: %w", err)
	}
	f.mu.Lock()
	f.settingsContent = strings.TrimSpace(string(data))
	f.mu.Unlock()
	slog.Info("[AIAgent] Settings reloaded", "path", settingsFile)
	return nil
}

// Create is the ActionFactory function for creating ai_agent actions.
func (f *AIAgentFactory) Create(raw json.RawMessage) (Action, error) {
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
	if params.Mode == "" {
		params.Mode = "conversational"
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

	f.mu.RLock()
	settings := f.settingsContent
	f.mu.RUnlock()

	return &AIAgentAction{
		params:          params,
		llmClient:       f.llmClient,
		parkService:     f.parkService,
		settingsContent: settings,
	}, nil
}

func (a *AIAgentAction) Type() string { return "ai_agent" }

func (a *AIAgentAction) Execute(ctx context.Context, session CallSession) error {
	logger := slog.With("call_id", session.CallID(), "action", "ai_agent")
	logger.Info("[AIAgent] Starting AI conversation",
		"config", a.params.Config,
		"voice", a.params.Voice,
		"model", a.params.Model,
		"mode", a.params.Mode,
		"max_turns", a.params.MaxTurns,
	)

	// Check if LLM client is available
	if a.llmClient == nil || !a.llmClient.Ready() {
		logger.Error("[AIAgent] LLM client not configured")
		return fmt.Errorf("llm server not configured")
	}

	// Load system prompt from cached settings + per-call tenant config
	systemPrompt, err := a.loadSystemPrompt()
	if err != nil {
		logger.Warn("[AIAgent] Failed to load config, using default", "error", err)
		systemPrompt = "You are a helpful voice assistant. Keep your responses brief and conversational."
	}

	// Create conversation with system prompt and model from dialplan
	conv := a.llmClient.NewConversation(systemPrompt, a.params.Model)

	// Speak greeting (mode-aware default)
	greeting := a.params.Greeting
	if greeting == "" {
		if a.params.Mode == "routing" {
			greeting = "Thank you for calling."
		} else {
			greeting = "Hello, how can I help you today?"
		}
	}

	logger.Debug("[AIAgent] Speaking greeting", "text", greeting)
	if err := session.PlayTTS(ctx, greeting, a.params.Voice); err != nil {
		logger.Error("[AIAgent] Failed to speak greeting", "error", err)
		return fmt.Errorf("speak greeting: %w", err)
	}

	// Branch based on mode
	if a.params.Mode == "routing" {
		return a.executeRouting(ctx, session, conv, logger)
	}
	return a.executeConversational(ctx, session, conv, logger)
}

// executeRouting handles single-shot routing: ask LLM for a routing decision, speak, execute action, done.
// No listen loop — the LLM decides based on tenant instructions alone.
func (a *AIAgentAction) executeRouting(ctx context.Context, session CallSession, conv *llm.Conversation, logger *slog.Logger) error {
	logger.Info("[AIAgent] Routing mode — requesting LLM decision")

	response, err := conv.Say(ctx, "The caller has just connected and heard the greeting. Based on your instructions, respond with what to say and what action to take.")
	if err != nil {
		logger.Error("[AIAgent] LLM routing error", "error", err)
		return session.Hangup("ai_agent_llm_error")
	}

	logger.Info("[AIAgent] LLM routing response", "text", response)

	spokenText, action := parseResponse(response)

	if spokenText != "" {
		if err := session.PlayTTS(ctx, spokenText, a.params.Voice); err != nil {
			logger.Error("[AIAgent] Failed to speak routing response", "error", err)
		}
	}

	if action != nil {
		if err := validateAction(action); err != nil {
			logger.Warn("[AIAgent] Invalid routing action", "error", err)
			return session.Hangup("ai_agent_invalid_action")
		}
		logger.Info("[AIAgent] Executing routing action", "action", action.Name, "params", action.Params)
		return a.executeAction(ctx, session, action, logger)
	}

	// No explicit action in routing mode — default to hangup
	logger.Info("[AIAgent] Routing mode complete, no action — hanging up")
	return session.Hangup("ai_agent_routing_complete")
}

// executeConversational handles multi-turn conversation: listen → LLM → speak → repeat.
func (a *AIAgentAction) executeConversational(ctx context.Context, session CallSession, conv *llm.Conversation, logger *slog.Logger) error {
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

		// Parse response for spoken text and optional action
		spokenText, action := parseResponse(response)

		// Speak the text portion (if any)
		if spokenText != "" {
			if err := session.PlayTTS(ctx, spokenText, a.params.Voice); err != nil {
				logger.Error("[AIAgent] Failed to speak response", "error", err)
				return fmt.Errorf("speak response: %w", err)
			}
		}

		// Execute action if present
		if action != nil {
			logger.Info("[AIAgent] Action detected", "action", action.Name, "params", action.Params)

			if err := validateAction(action); err != nil {
				logger.Warn("[AIAgent] Invalid action from LLM", "error", err)
				continue
			}

			if err := a.executeAction(ctx, session, action, logger); err != nil {
				// Transfer failures: inform caller and continue conversation
				if action.Name == "transfer" {
					logger.Warn("[AIAgent] Transfer failed", "error", err)
					if err := session.PlayTTS(ctx, "I'm sorry, I wasn't able to complete that transfer. Is there anything else I can help with?", a.params.Voice); err != nil {
						logger.Warn("[AIAgent] Failed to speak transfer error", "error", err)
					}
					continue
				}
				logger.Error("[AIAgent] Action failed", "action", action.Name, "error", err)
				return fmt.Errorf("execute action %s: %w", action.Name, err)
			}
			// Action executed successfully — end the conversation loop
			return nil
		}
	}

	// Max turns reached
	logger.Info("[AIAgent] Max turns reached")
	if err := session.PlayTTS(ctx, "I've enjoyed our conversation, but I need to go now. Goodbye!", a.params.Voice); err != nil {
		logger.Warn("[AIAgent] Failed to say max turns goodbye", "error", err)
	}

	return nil
}

// loadSystemPrompt combines cached settings content with per-call tenant config.
func (a *AIAgentAction) loadSystemPrompt() (string, error) {
	var parts []string

	// Use cached settings content (loaded once at startup)
	if a.settingsContent != "" {
		parts = append(parts, a.settingsContent)
	}

	// Load tenant config per-call
	configName := a.params.Config
	if configName == "" {
		configName = "default"
	}
	configName = strings.TrimSuffix(configName, ".md")
	configPath := filepath.Join(a.params.TenantsPath, configName+".md")

	data, err := os.ReadFile(configPath)
	if err != nil {
		if len(parts) > 0 {
			// Settings loaded but no tenant config — still usable
			return strings.Join(parts, "\n\n"), nil
		}
		return "", fmt.Errorf("read config file %s: %w", configPath, err)
	}
	parts = append(parts, strings.TrimSpace(string(data)))

	return strings.Join(parts, "\n\n"), nil
}

// parsedAction represents an action extracted from an LLM response.
type parsedAction struct {
	Name   string            // e.g. "transfer", "hangup", "park"
	Params map[string]string // e.g. {"extension": "100"}
}

// isValidAction checks whether an action name is in the supported whitelist.
func isValidAction(name string) bool {
	switch name {
	case "transfer", "hangup", "park":
		return true
	default:
		return false
	}
}

// parseResponse splits an LLM response into spoken text and an optional action.
// Handles common LLM formatting variations: "ACTION:", "Action:", markdown bold/backticks, etc.
func parseResponse(response string) (spokenText string, action *parsedAction) {
	// Strip markdown formatting that LLMs commonly add around ACTION
	cleaned := response
	cleaned = strings.ReplaceAll(cleaned, "**", "")
	cleaned = strings.ReplaceAll(cleaned, "`", "")

	// Find ACTION marker (case-insensitive)
	upper := strings.ToUpper(cleaned)
	idx := strings.Index(upper, "ACTION:")
	if idx == -1 {
		return strings.TrimSpace(response), nil
	}

	spokenText = strings.TrimSpace(cleaned[:idx])
	actionBlock := strings.TrimSpace(cleaned[idx+len("ACTION:"):])

	lines := strings.Split(actionBlock, "\n")
	actionName := strings.ToLower(strings.TrimSpace(lines[0]))

	if actionName == "" || !isValidAction(actionName) {
		// Unknown or invalid action — speak only the text before ACTION, discard the block
		return spokenText, nil
	}

	action = &parsedAction{
		Name:   actionName,
		Params: make(map[string]string),
	}

	for _, line := range lines[1:] {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if k, v, ok := strings.Cut(line, ":"); ok {
			action.Params[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}

	return spokenText, action
}

// validateAction checks that a parsed action has required parameters.
func validateAction(action *parsedAction) error {
	switch action.Name {
	case "transfer":
		if action.Params["extension"] == "" {
			return fmt.Errorf("transfer action requires 'extension' parameter")
		}
	case "hangup", "park":
		// No required params
	default:
		return fmt.Errorf("unknown action: %s", action.Name)
	}
	return nil
}

// executeAction runs a validated action against the call session.
func (a *AIAgentAction) executeAction(ctx context.Context, session CallSession, action *parsedAction, logger *slog.Logger) error {
	switch action.Name {
	case "transfer":
		ext := action.Params["extension"]
		logger.Info("[AIAgent] Executing transfer", "extension", ext)
		return session.Dial(ctx, "user/"+ext, 30*time.Second)

	case "hangup":
		logger.Info("[AIAgent] Executing hangup")
		return session.Hangup("ai_agent_hangup")

	case "park":
		if a.parkService == nil {
			return fmt.Errorf("park service not configured")
		}
		logger.Info("[AIAgent] Executing park")
		req := parking.ParkRequest{
			SlotID:    action.Params["slot"],
			CallID:    session.CallID(),
			Dialog:    session.GetDialog(),
			SessionID: session.GetSessionID(),
			ParkedBy:  "ai_agent",
			MOHFiles:  []string{"hold_music.wav"},
		}
		if _, err := a.parkService.Park(ctx, req); err != nil {
			return fmt.Errorf("park call: %w", err)
		}
		// Block until unparked or caller hangs up
		<-ctx.Done()
		return nil

	default:
		return fmt.Errorf("unknown action: %s", action.Name)
	}
}
