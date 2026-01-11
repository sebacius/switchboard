package dialplan

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// DefaultDialTimeout is the default timeout for dial actions.
const DefaultDialTimeout = 30 * time.Second

// DialParams defines parameters for dial action.
type DialParams struct {
	Target  string `json:"target"`  // "user/1001" or "sip:user@host:port"
	Timeout int    `json:"timeout"` // Timeout in seconds (default: 30)
}

// DialAction initiates an outbound call and bridges on answer.
type DialAction struct {
	params DialParams
}

// NewDialAction creates a dial action from JSON config.
func NewDialAction(raw json.RawMessage) (Action, error) {
	var params DialParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, fmt.Errorf("parse dial params: %w", err)
	}
	if params.Target == "" {
		return nil, fmt.Errorf("dial: target required")
	}
	if params.Timeout <= 0 {
		params.Timeout = int(DefaultDialTimeout.Seconds())
	}
	return &DialAction{params: params}, nil
}

// Type returns "dial".
func (a *DialAction) Type() string {
	return "dial"
}

// Execute initiates the outbound call and bridges on answer.
// This blocks until the call ends (either side hangs up) or fails.
//
// Flow:
// 1. Resolve target (user/xxx -> location lookup, sip:// -> direct)
// 2. Send INVITE to resolved contact
// 3. Wait for answer (or timeout/rejection)
// 4. Bridge media between leg A and leg B
// 5. Block until BYE from either side
func (a *DialAction) Execute(ctx context.Context, session CallSession) error {
	timeout := time.Duration(a.params.Timeout) * time.Second

	// Create timeout context
	dialCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Dial the target - this handles:
	// - Target resolution (user/ prefix)
	// - INVITE to resolved contact
	// - Wait for answer
	// - Bridge media
	// - Wait for BYE
	if err := session.Dial(dialCtx, a.params.Target, timeout); err != nil {
		return err
	}

	return nil
}
