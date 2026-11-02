## ADDED Requirements

### Requirement: The audio library reports each file's format and duration

Every file the player holds SHALL be listed with its duration and its audio format, and anything that will not play exactly as recorded SHALL be flagged.

#### Scenario: A conforming file is shown as conforming

- **WHEN** a file is 8 kHz, mono, 16-bit PCM
- **THEN** its row shows that format and its duration
- **AND** carries no warning

#### Scenario: A non-conforming file is flagged

- **WHEN** a file is not 8 kHz mono 16-bit PCM
- **THEN** its row is flagged, saying whether it will be resampled or will not play at all

#### Scenario: A file that cannot be probed is not accused

- **WHEN** the player reports no format for a file
- **THEN** the row leaves the format blank
- **AND** does not claim the file is malformed

### Requirement: Reporting what the flows reference is preserved

The existing join between what flows name and what the player holds SHALL be unchanged, including its refusal to report a file as missing when the player could not be reached.

#### Scenario: An unreachable player is not reported as missing files

- **WHEN** the player cannot be reached
- **THEN** the page says the listing is unavailable
- **AND** does not report referenced files as missing

#### Scenario: Referenced, missing and unreferenced remain distinguished

- **WHEN** the audio page is rendered with the player reachable
- **THEN** files the flows reference, files they reference that are absent, and files nobody references are still shown separately
