## ADDED Requirements

### Requirement: Calls are routed by a flow graph

The system SHALL route every call that is not deterministically resolved in one hop by
executing a **flow**: a directed graph of nodes, defined as data in the tenant's
configuration, evaluated without any language model, agent, or network egress.

A flow SHALL be entered from the tenant's entry mapping. Execution SHALL begin at the
flow's declared start node and SHALL proceed by taking the exit that the executed node
produced, until a terminal exit or a terminal node is reached.

#### Scenario: A menu selection traverses to the selected node

- **WHEN** a caller enters a flow whose start node is an `ivr` and presses a digit that
  the node declares an exit for
- **THEN** execution moves to the node named by that exit

#### Scenario: Routing works with no model reachable

- **WHEN** no LLM, agent, or external service of any kind is reachable
- **THEN** every flow still executes normally, because flow execution makes no request
  off the box

### Requirement: The node vocabulary is a closed set with a uniform shape

Every node SHALL have the same structure: a `type` from a closed set, an `entry` object
carrying type-specific input, and an `exits` map from declared exit name to node ID.

The node types SHALL be exactly `ivr`, `tts`, `play_audio`, `dial_user`,
`dial_external`, `transfer`, and `hangup`. A node of an unrecognized type SHALL be a
load error.

Each node type's set of exit names SHALL be fixed in code, not in configuration, so that
an exit which does not exist for that type and an exit which exists but is not wired are
both detectable at load.

#### Scenario: Unknown node type is rejected

- **WHEN** a flow declares a node whose `type` is not in the closed set
- **THEN** the configuration fails to load with an error naming the node and its type

#### Scenario: Unknown exit name is rejected

- **WHEN** a node declares an exit name that its type does not define
- **THEN** the configuration fails to load with an error naming the node and the exit

#### Scenario: Unwired exit is rejected

- **WHEN** a node omits a non-terminal exit that its type defines
- **THEN** the configuration fails to load, so what happens on every outcome is always
  written down rather than defaulted

### Requirement: Answering exits are terminal

An answering exit SHALL be terminal: the flow ends, the legs are bridged, and the flow
cursor is released. The terminal exits are `answered` on `dial_user` and `dial_external`,
and `accepted` on `transfer`.

A terminal exit SHALL NOT be declarable in configuration. Declaring one SHALL be a load
error, because the graph cannot express what happens after the call is bridged.

#### Scenario: Answered ends the flow

- **WHEN** a `dial_user` node's target answers
- **THEN** the two legs are bridged, the flow ends, and no further node is evaluated

#### Scenario: Declaring a terminal exit is rejected

- **WHEN** a `dial_user` node declares an `answered` exit in its `exits` map
- **THEN** the configuration fails to load with an error explaining that `answered` is
  terminal

### Requirement: The flow graph is data, never authority

Every destination a flow produces SHALL be adjudicated by the same per-tenant policy
that governs every other dial — that means each `dial_user`, `dial_external`, and
`transfer` target, and each ring group member individually. A denied destination SHALL
NOT be dialed.

`dial_external` SHALL accept symbolic targets only. A flow SHALL NOT be able to express
a raw external number, and no matched digit string SHALL be usable as a dial target.

Anyone able to edit a flow file SHALL NOT thereby be able to widen the tenant's reach.

#### Scenario: Denied destination takes the denied exit

- **WHEN** a `dial_external` node names a symbolic target the tenant's Class of Service
  denies
- **THEN** no INVITE leaves the system, the deny is logged, and execution follows the
  node's `denied` exit

#### Scenario: A flow file cannot grant reach

- **WHEN** a flow names a destination outside the tenant's allowlist
- **THEN** the dial is denied by policy, because the flow file is not authorization

#### Scenario: Group members are authorized individually

- **WHEN** a `dial_user` node targets a ring group
- **THEN** each member is adjudicated separately and denied members are not rung

### Requirement: Every flow is provably terminating

The inter-node graph SHALL be acyclic. A cycle between nodes SHALL be a load error that
names the cycle path, not merely its existence.

Repetition SHALL be expressed only *inside* a node, bounded by a counter — an `ivr`
node's `max_retries` is a bounded self-loop handled within the node and contributes no
edge to the graph. Together these make every flow provably terminating, a guarantee no
priority-ordered dialplan can offer.

The executor SHALL additionally enforce a hard maximum hop count as a backstop, so a
defect in validation cannot produce a non-terminating call.

#### Scenario: An inter-node cycle is rejected at load

- **WHEN** a flow contains nodes whose exits form a cycle
- **THEN** the configuration fails to load with an error naming the cycle path

#### Scenario: Retry within a node is not a cycle

- **WHEN** an `ivr` node re-prompts after an invalid entry, up to its `max_retries`
- **THEN** this is not a graph cycle, and the flow still loads and still terminates

### Requirement: A flow has per-call state with nested budgets

Flow execution SHALL maintain per-call state — the current node, buffered digits,
per-node retry counts, and the traversal so far — for the life of the call.

Time SHALL be bounded by nested budgets: a whole-flow deadline containing a per-node
budget containing a per-playback scope, so a prompt can be cut without ending the node
and a node can end without ending the flow.

The per-call state SHALL be released when the call ends by any path, including a caller
who abandons mid-menu.

#### Scenario: Caller abandons mid-menu

- **WHEN** a caller hangs up while an `ivr` node is waiting for a digit
- **THEN** the blocking operation unwinds, no further node is evaluated, and the flow
  state for that call is released

#### Scenario: Whole-flow deadline ends the call

- **WHEN** a call exceeds the flow's overall deadline
- **THEN** the call is terminated and the flow state is released

### Requirement: A flow answers only when it must

The system SHALL NOT answer a call in order to route it. A dial reached before any
media node SHALL forward the INVITE without answering, so the caller hears the
destination's own ringback and can still receive its real final status.

Once any node plays media to the caller, the call SHALL be answered, and dials after
that point SHALL bridge into the media the system already owns.

#### Scenario: A one-node dial flow never answers

- **WHEN** a flow consists of a single `dial_user` node
- **THEN** the INVITE is forwarded without a 200 OK, and the caller receives the
  destination's own ringing and final response

#### Scenario: A menu-first flow answers before speaking

- **WHEN** a flow's start node plays a prompt
- **THEN** the call is answered before the prompt is played

### Requirement: Dial outcomes are distinguished, and the flow decides what the caller hears

A dial that does not answer SHALL yield a distinguishable outcome — at minimum no
answer, busy, rejected, and unavailable — and execution SHALL follow the exit matching
that outcome.

A dial executed as part of a flow SHALL NOT relay a final failure status to the caller
on its own. Relaying is the graph's decision, made by the node the failure exit leads
to. A flow that ends without relaying anything SHALL relay a final status on the flow's
behalf.

#### Scenario: Busy continues the flow instead of ending the call

- **WHEN** a `dial_user` target returns 486 Busy and the node wires `busy` to another
  node
- **THEN** no 486 is sent to the caller and execution continues at that node

#### Scenario: A hangup node relays its configured cause

- **WHEN** execution reaches a `hangup` node before the call was answered
- **THEN** the caller receives a final status corresponding to that node's cause

### Requirement: The call record contains the traversal

The system SHALL record the path a call took through its flow — the sequence of nodes
entered, the exit each produced, and the time spent at each hop — not merely the final
outcome. Authorization verdicts produced during the flow SHALL be recorded against the
same call.

#### Scenario: The path is answerable after the fact

- **WHEN** a caller ends up at a different destination than expected
- **THEN** the call record shows each node entered, in order, with the exit taken and
  the time at each hop

#### Scenario: A denied dial is recorded against the call

- **WHEN** a flow's dial is denied by policy
- **THEN** the deny verdict appears in the same call record as the traversal
