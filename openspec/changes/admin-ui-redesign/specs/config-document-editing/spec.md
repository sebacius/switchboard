## ADDED Requirements

### Requirement: A configuration file edited through the UI keeps what its author wrote

Writing a configuration file through the UI SHALL preserve every part of the document the edit did not concern — comment keys, key order, indentation and blank lines — by replacing only the bytes of what changed. A configuration file SHALL NOT be decoded into a data model and re-encoded.

#### Scenario: Comment keys survive an edit

- **WHEN** a routing table carrying `_comment`, `_comment_extensions` and `_comment_dids` is edited through the UI and saved
- **THEN** all three keys are present in the saved file with their original values

#### Scenario: Key order is unchanged

- **WHEN** a section in the middle of a routing table is edited and saved
- **THEN** the document's top-level keys appear in the same order as before
- **AND** the edited section's own keys keep their order except where the operator changed them

#### Scenario: Indentation is the document's own

- **WHEN** a file written with four-space indentation is edited and saved
- **THEN** the saved file is still four-space indented

#### Scenario: An untouched row keeps its exact bytes

- **WHEN** one row of a section is edited and saved
- **THEN** every other row in that section is byte-identical to before

### Requirement: Editing one section cannot affect another

An edit to one top-level section SHALL be incapable of modifying any other section or the prose around it.

#### Scenario: Editing groups does not touch DIDs

- **WHEN** a ring group is edited and saved
- **THEN** the DID section and the comment above it are byte-identical to before
- **AND** the difference between the files lies entirely within the groups section

#### Scenario: The saved file still loads

- **WHEN** any section is edited and saved
- **THEN** the resulting file passes the loader's own validation

### Requirement: A destination keeps the form it was written in

A routing entry that carries no digit transform SHALL be written back as a bare destination string, and one that carries a transform SHALL be written back as an object, matching how the loader itself serialises an entry.

#### Scenario: A simple entry stays a bare string

- **WHEN** an entry written as `"100": "user/100"` is saved without adding a transform
- **THEN** it is still written as a bare string

#### Scenario: An entry with a transform stays an object

- **WHEN** an entry carrying a digit transform is saved
- **THEN** it is written in object form with its transform intact

### Requirement: A section absent from a file can be added without disturbing its neighbours

Adding a top-level section a file does not yet have SHALL insert it without reformatting any existing key and without separating a comment key from the key it describes.

#### Scenario: Adding a section to a file that lacks it

- **WHEN** a tenant with no ring groups gains its first group
- **THEN** the file gains a groups section
- **AND** every pre-existing key, including every comment, is byte-identical to before

### Requirement: A key rename changes only the key

Renaming a key SHALL rewrite the key token alone, leaving the value's bytes and the key's position in the document unchanged.

#### Scenario: Renaming keeps the value verbatim

- **WHEN** an entry pattern is renamed
- **THEN** its destination and any formatting inside its value are unchanged
- **AND** its position among the other entries is unchanged

#### Scenario: A key that cannot be located is refused

- **WHEN** the bytes at a key's recorded position are not that key
- **THEN** the rename is refused with an explanation
- **AND** the document is not modified

### Requirement: Deleting a key leaves valid JSON wherever it sat

Removing a key SHALL handle the surrounding separators correctly whether the key is the first, a middle, the last, or the only key in its object.

#### Scenario: Deleting each position yields a loadable file

- **WHEN** the first, a middle, the last, or the only key of a section is deleted
- **THEN** the resulting file is valid JSON
- **AND** it passes the loader's validation

### Requirement: The routing and flows editors share one implementation

The byte-span machinery used to edit configuration documents SHALL exist once and be used by both the flows editor and the routing editor.

#### Scenario: The flows editor is unchanged by the extraction

- **WHEN** the shared primitives are extracted from the flows editor
- **THEN** the flows editor's existing behaviour is unchanged
- **AND** its existing tests pass without modification
