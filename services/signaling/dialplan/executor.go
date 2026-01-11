package dialplan

import (
	"context"
	"fmt"
	"log/slog"
)

// Executor runs dialplan routes.
type Executor struct {
	dialplan *Dialplan
	registry *ActionRegistry
	logger   *slog.Logger
}

// NewExecutor creates a new executor.
func NewExecutor(dialplan *Dialplan, registry *ActionRegistry, logger *slog.Logger) *Executor {
	if logger == nil {
		logger = slog.Default()
	}
	if registry == nil {
		registry = DefaultRegistry()
	}

	return &Executor{
		dialplan: dialplan,
		registry: registry,
		logger:   logger,
	}
}

// Execute matches and runs the dialplan for an incoming call.
// Returns ErrNoRouteMatch if no route matches.
// Returns ExecutionError if an action fails (with partial execution info).
func (e *Executor) Execute(ctx context.Context, session CallSession) error {
	destination := session.Destination()

	// Find matching route
	route, found := e.dialplan.Match(destination)
	if !found {
		e.logger.Warn("[Dialplan] No route match",
			"call_id", session.CallID(),
			"destination", destination,
		)
		return ErrNoRouteMatch
	}

	return e.ExecuteRoute(ctx, session, route)
}

// ExecuteRoute runs a specific route's actions.
// Useful when you want to run a specific route without matching.
func (e *Executor) ExecuteRoute(ctx context.Context, session CallSession, route *Route) error {
	e.logger.Info("[Dialplan] Executing route",
		"route_id", route.ID,
		"route_name", route.Name,
		"call_id", session.CallID(),
		"actions", len(route.Actions),
	)

	for i, actionCfg := range route.Actions {
		// Check cancellation before each action
		if err := ctx.Err(); err != nil {
			return &ExecutionError{
				RouteID:        route.ID,
				CompletedSteps: i,
				TotalSteps:     len(route.Actions),
				FailedAction:   "context_check",
				Cause:          err,
			}
		}

		// Check if session is already terminated
		if session.IsTerminated() {
			return &ExecutionError{
				RouteID:        route.ID,
				CompletedSteps: i,
				TotalSteps:     len(route.Actions),
				FailedAction:   "session_check",
				Cause:          ErrSessionCanceled,
			}
		}

		// Create action from config
		action, err := e.registry.Create(actionCfg.Type, actionCfg.Params)
		if err != nil {
			return &ExecutionError{
				RouteID:        route.ID,
				CompletedSteps: i,
				TotalSteps:     len(route.Actions),
				FailedAction:   actionCfg.Type,
				Cause:          fmt.Errorf("create action: %w", err),
			}
		}

		e.logger.Debug("[Dialplan] Executing action",
			"action", action.Type(),
			"step", i+1,
			"total", len(route.Actions),
			"call_id", session.CallID(),
		)

		// Execute the action
		if err := action.Execute(ctx, session); err != nil {
			e.logger.Warn("[Dialplan] Action failed",
				"action", action.Type(),
				"step", i+1,
				"call_id", session.CallID(),
				"error", err,
			)
			return &ExecutionError{
				RouteID:        route.ID,
				CompletedSteps: i,
				TotalSteps:     len(route.Actions),
				FailedAction:   action.Type(),
				Cause:          err,
			}
		}

		e.logger.Debug("[Dialplan] Action completed",
			"action", action.Type(),
			"step", i+1,
			"call_id", session.CallID(),
		)
	}

	e.logger.Info("[Dialplan] Route completed",
		"route_id", route.ID,
		"call_id", session.CallID(),
	)

	return nil
}

// Registry returns the action registry for extension.
func (e *Executor) Registry() *ActionRegistry {
	return e.registry
}

// Dialplan returns the underlying dialplan for inspection.
func (e *Executor) Dialplan() *Dialplan {
	return e.dialplan
}
