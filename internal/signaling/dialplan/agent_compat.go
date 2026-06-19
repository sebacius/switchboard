package dialplan

import "github.com/sebas/switchboard/internal/signaling/agent"

// Transitional shim: CallSession and its constructor now live in the agent
// package (the LLM supervisor's seam between the call lifecycle and tool
// handlers). The dialplan and routing still reference them during the
// migration; these aliases keep both compiling until the dialplan is removed.
type (
	CallSession   = agent.CallSession
	SessionConfig = agent.SessionConfig
)

// NewSession constructs a CallSession, delegating to the agent package.
func NewSession(cfg SessionConfig) CallSession {
	return agent.NewSession(cfg)
}
