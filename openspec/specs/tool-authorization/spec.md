# tool-authorization Specification

## Purpose

The deterministic policy layer wrapping a zero-authority model: Class of Service on dial, capability narrowing via symbolic targets, the per-tenant spend circuit breaker, and decision logging. Authorization never reads prompt content.

## Requirements

### Requirement: The model has zero authority; tool calls are authorized deterministically

Every consequential tool call the model emits SHALL be treated as an untrusted request that a
deterministic policy layer authorizes before execution. Authorization SHALL NOT depend on prompt
content. A denied tool call SHALL return a "not permitted" result into the conversation without
executing.

#### Scenario: Denied tool does not execute

- **WHEN** the policy layer denies a tool call
- **THEN** the action does not happen and a "not permitted" result is returned to the model

#### Scenario: Prompt manipulation cannot grant authority

- **WHEN** caller speech instructs the model to bypass restrictions
- **THEN** the policy layer's decision is unchanged because it does not read prompt content

### Requirement: Class of Service on dial

External dialing SHALL be governed by a per-tenant, per-direction Class of Service: external dial is
default-deny; permitted destinations are an allowlist of numbers/prefixes; barred classes (premium-rate,
satellite, high-risk country codes) SHALL be blocked.

#### Scenario: External dial denied by default

- **WHEN** a tenant has not enabled external dialing and a `dial` to an external destination is attempted
- **THEN** it is denied

#### Scenario: Barred class is blocked even when external is enabled

- **WHEN** external dialing is enabled but the destination is in a barred class (e.g. premium-rate)
- **THEN** the dial is denied

#### Scenario: Allowlisted destination is permitted

- **WHEN** external dialing is enabled and the destination matches the tenant allowlist
- **THEN** the dial is authorized

### Requirement: Capability narrowing of dial targets

The model SHALL emit symbolic dial targets (extension names, named forwards) that a deterministic
resolver maps to numbers; the model SHALL NOT be able to express an arbitrary external number through
the default dial tool. Dialing a caller-provided number SHALL require a separate, explicitly gated tool.

#### Scenario: Symbolic target resolves deterministically

- **WHEN** the model dials a named forward
- **THEN** the resolver maps it to the configured number; the model never supplied the raw number

#### Scenario: Arbitrary number requires the gated tool

- **WHEN** a caller-provided number must be dialed
- **THEN** it is only possible via the separate gated tool, subject to Class of Service

### Requirement: Per-tenant spend circuit breaker

The system SHALL enforce per-tenant spend/rate limits on external calls (e.g. max external minutes or
cost per day) and SHALL trip and alert when a limit is exceeded.

#### Scenario: Spend spike trips the breaker

- **WHEN** a tenant's external call spend exceeds its configured limit
- **THEN** further external dials are blocked and an alert is raised

### Requirement: Decision logging

Every tool call and its authorization verdict SHALL be logged to the call record so denied actions are
auditable as potential fraud signals.

#### Scenario: Denied dial is recorded

- **WHEN** an external dial is denied by policy
- **THEN** the call record contains the attempted tool call and the deny verdict
