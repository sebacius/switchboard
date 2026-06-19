package agent

import (
	"context"

	"github.com/sebas/switchboard/internal/signaling/llm"
)

// ToolExecutor runs a single tool call against the call session and reports back
// the result string to feed into the conversation plus the loop disposition.
//
// It decouples the runner from the per-call tool registry, handlers, and
// authorization policy (built in a later group). The runner depends only on this
// narrow seam; the real implementation will adjudicate the call through the
// authorization policy before dispatching to a handler. Tests supply a fake.
type ToolExecutor interface {
	// Execute runs one tool call against the session, returning a result string
	// to feed back into the conversation plus the disposition the loop should
	// take. A non-nil err is an executor-internal failure (the runner records an
	// actionable tool-result message and continues); a tool that simply failed
	// its own operation should report that in result with a nil err.
	Execute(ctx context.Context, call llm.ToolCall, sess CallSession) (result string, disp Disposition, err error)
}
