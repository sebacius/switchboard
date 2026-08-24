## ADDED Requirements

### Requirement: Pattern specificity is available outside the dialplan

The dialplan SHALL expose a pattern's per-position specificity, an ordering over a set of patterns, and the pairs of patterns that overlap with neither more specific — so that a user interface can show the order the router will use without restating the comparison.

#### Scenario: A pattern reports its specificity

- **WHEN** a compiled pattern is asked for its specificity
- **THEN** it reports the accepted-set size at each position

#### Scenario: A set of patterns can be ordered

- **WHEN** a set of entry patterns is ranked
- **THEN** they are returned narrowest first
- **AND** the order is stable for patterns that cannot be compared

#### Scenario: The ranking agrees with domination

- **WHEN** one pattern is strictly more specific than another
- **THEN** it is ordered before it

#### Scenario: No scalar score is exposed

- **WHEN** specificity is requested
- **THEN** it is a per-position vector rather than a single number, because a single number collapses unlike patterns onto the same value

### Requirement: Every ambiguous pair can be enumerated

Given a set of patterns, the dialplan SHALL report every pair that can match the same input with neither more specific, not only the first such pair.

#### Scenario: All conflicts are reported

- **WHEN** a pattern set contains more than one ambiguous pair
- **THEN** every pair is reported

#### Scenario: A set with no ambiguity reports none

- **WHEN** every overlapping pair in a set has a strictly more specific member
- **THEN** no conflicts are reported

### Requirement: Compiling a digit map uses the same conflict definition

Digit map compilation SHALL determine ambiguity through the same code that reports conflicts, so that what an editor warns about and what the loader refuses cannot diverge.

#### Scenario: Compilation still refuses an ambiguous table

- **WHEN** a digit map is compiled from patterns containing an ambiguous pair
- **THEN** compilation fails naming both patterns
- **AND** the behaviour is unchanged from before this capability existed
