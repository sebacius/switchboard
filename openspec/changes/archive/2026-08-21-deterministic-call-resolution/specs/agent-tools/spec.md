## MODIFIED Requirements

### Requirement: Argument validation and self-correction

Tool handlers SHALL validate arguments explicitly. An unknown tool SHALL NOT terminate the
call: the system SHALL transfer the caller deterministically to the tenant's configured
operator destination. A missing or invalid required argument SHALL return an actionable
error string into the conversation so the model can correct on the next turn. A tool call
identical to one that just failed SHALL be refused.

A tenant with no configured operator destination SHALL fall back to an actionable error
into the conversation rather than hanging up, so an unknown tool never drops a caller.

#### Scenario: Unknown tool transfers to the operator

- **WHEN** the model emits a tool name outside the call's registry
- **THEN** the caller is transferred to the tenant's operator destination and the call is
  not hung up

#### Scenario: Unknown tool with no operator configured keeps the call alive

- **WHEN** the model emits an unknown tool for a tenant with no operator destination
- **THEN** an actionable error naming the available tools is returned into the
  conversation and the call continues

#### Scenario: Missing required argument

- **WHEN** the model invokes `dial` without a target
- **THEN** the handler returns an actionable error into the conversation and the call continues

#### Scenario: Identical failing call is refused

- **WHEN** the model re-issues the same tool and arguments that just failed
- **THEN** the runner refuses the duplicate and nudges the model toward an alternative
