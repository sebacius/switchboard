## ADDED Requirements

### Requirement: The node catalogue is exported from Go

The `dialplan` package SHALL export `NodeSchema()`, returning one entry per node type in the closed set returned by `NodeTypes()`. Each entry SHALL carry the type name, its declared exits, its terminal exits, whether the type accepts digit exits, and a descriptor for every field of that type's entry struct. No consumer SHALL need to restate the exit contract or the entry shape to render a palette or a property form.

#### Scenario: Every node type appears exactly once

- **WHEN** `NodeSchema()` is called
- **THEN** it returns one entry for each of `ivr`, `tts`, `play_audio`, `dial_user`, `dial_external`, `transfer`, `hangup`
- **AND** the set of type names equals the set returned by `NodeTypes()`
- **AND** the entries are in a stable, sorted order

#### Scenario: Declared exits match the exit contract

- **WHEN** the schema entry for a node type is inspected
- **THEN** its declared exits equal `DeclaredExits(t)` for that type
- **AND** an `ivr` entry declares `timeout`, `invalid`, and `retries_exceeded`
- **AND** a `dial_user` entry declares `no_answer`, `busy`, `rejected`, and `unavailable`
- **AND** a `hangup` entry declares no exits

#### Scenario: A new node type propagates without a front-end change

- **WHEN** a node type is added to the `dialplan` package's exit table with its entry struct
- **THEN** `NodeSchema()` includes it with its exits and fields
- **AND** no JavaScript, template, or hand-written catalogue requires editing for it to be offered in a palette

### Requirement: Terminal exits are reported and marked as unconnectable

The schema SHALL report each type's terminal exits separately from its declared exits, so a consumer can name the outcome without offering it as a connection point. A terminal exit ends the flow and is a load error when declared in configuration.

#### Scenario: Terminal exits are carried but separated

- **WHEN** the schema entry for `dial_user` or `dial_external` is inspected
- **THEN** its terminal exits contain `answered`
- **AND** `answered` does not appear among its declared exits

#### Scenario: Transfer reports its own terminal exit

- **WHEN** the schema entry for `transfer` is inspected
- **THEN** its terminal exits contain `accepted`
- **AND** its declared exits contain only `failed`

#### Scenario: The two lists agree with the package predicate

- **WHEN** any exit named in a type's terminal exits is passed to `IsTerminalExit` with that type
- **THEN** the result is true
- **AND** `IsTerminalExit` is false for every exit in that type's declared exits

### Requirement: Digit-accepting types are identified

The schema SHALL identify which node types accept digit exits, so a consumer can allow digit exits to be added and removed on those types only. A digit exit is any exit whose characters are drawn from `0-9`, `*`, and `#`.

#### Scenario: Only ivr accepts digit exits

- **WHEN** the schema is inspected
- **THEN** the `ivr` entry is marked as accepting digit exits
- **AND** no other entry is

### Requirement: Entry fields are described from the entry structs

Each schema entry SHALL carry a field descriptor per field of that type's entry struct, giving the JSON key, a kind, and a required hint. Descriptors SHALL be derived from the entry struct itself so that adding, removing, or renaming a field changes the schema without any other edit.

#### Scenario: Field keys match the struct's JSON tags

- **WHEN** the `tts` entry's field descriptors are inspected
- **THEN** they name `text`, `voice`, and `interruptible`
- **AND** each key matches the corresponding struct field's json tag

#### Scenario: Kinds distinguish the field types a form must render

- **WHEN** field descriptors are inspected across all types
- **THEN** `timeout_ms` and `max_retries` are described as integer fields
- **AND** `interruptible` is described as a boolean field
- **AND** `target` and `text` are described as string fields

#### Scenario: A prompt is marked as a prompt

- **WHEN** the `ivr` entry's descriptor for `prompt` is inspected
- **THEN** it is marked as a `PromptSpec`
- **AND** it carries descriptors for the prompt's own fields `text`, `voice`, `file`, and `files`

#### Scenario: The required hint follows the omitempty tag

- **WHEN** field descriptors are inspected
- **THEN** `tts.text` and `dial_user.target`, whose tags omit `omitempty`, are marked required
- **AND** `tts.voice` and `hangup.cause`, whose tags carry `omitempty`, are not
- **AND** the hint is documented as a form hint whose authority is the package's own entry validation
