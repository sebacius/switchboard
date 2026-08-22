## ADDED Requirements

### Requirement: DTMF digits are detected and delivered as call input

The system SHALL detect DTMF digits pressed by the caller and make them available to
flow execution as input. Digits SHALL be detected in the media path per RFC 4733
(telephone-event), and MAY additionally be accepted out of band via SIP INFO.

Before this capability the system had no DTMF producer of any kind, so digit-driven
routing was not possible.

#### Scenario: A pressed digit reaches the flow

- **WHEN** a caller presses `1` during an `ivr` node on a leg that negotiated
  telephone-event
- **THEN** the digit is detected and the node takes its `1` exit

#### Scenario: SIP INFO digits are accepted

- **WHEN** an endpoint sends DTMF as a SIP INFO request rather than in the media path
- **THEN** the digit is delivered to flow execution identically to a media-path digit

### Requirement: Telephone-event is negotiated honestly

The system SHALL determine the payload type the peer offered for `telephone-event` from
the offer's own media attributes rather than assuming a fixed value, and SHALL answer
using the payload type the offerer proposed.

The system SHALL NOT answer with `telephone-event` when the offer did not contain it.

When a leg has negotiated no telephone-event transport, digit collection on that leg
SHALL report that condition distinctly rather than reporting silence, and the flow
SHALL degrade by a declared exit rather than appearing to hang.

#### Scenario: A non-standard payload type is honoured

- **WHEN** an endpoint offers `telephone-event` on a dynamic payload type other than 101
- **THEN** the answer echoes that payload type and digits on it are detected

#### Scenario: An offer without telephone-event is answered without it

- **WHEN** an offer contains no `telephone-event` format
- **THEN** the answer contains none either

#### Scenario: A leg with no DTMF transport degrades by a declared exit

- **WHEN** an `ivr` node runs on a leg that negotiated no telephone-event transport
- **THEN** collection reports no available DTMF transport and the flow follows its
  retries-exhausted exit rather than waiting for digits that cannot arrive

### Requirement: Prompt and collection are one operation

Playing a prompt and collecting the digits that answer it SHALL be a single operation,
so that a digit pressed between the end of the prompt and the start of collection cannot
be lost.

A prompt MAY be declared interruptible, in which case the first digit SHALL stop
playback while the collection continues.

#### Scenario: A digit during the prompt is not lost

- **WHEN** a caller presses a digit while the prompt is still playing
- **THEN** the digit is counted toward the collection

#### Scenario: An interruptible prompt stops on the first digit

- **WHEN** a prompt is declared interruptible and the caller presses a digit
- **THEN** playback stops and collection continues

### Requirement: Digits buffer across nodes so callers can dial ahead

Digits SHALL be buffered continuously from the start of the call, and a collection
SHALL consume already-buffered digits before waiting for new ones, so a caller who knows
the menu can dial through it without losing input.

A node MAY explicitly discard buffered digits before collecting — for instance when
re-prompting after an error, where accepting type-ahead would be wrong.

#### Scenario: Dialing ahead through a menu works

- **WHEN** a caller presses `1` and then immediately `3` while the first menu's prompt
  is still playing
- **THEN** the first menu consumes `1` and the node it leads to consumes `3` without
  re-prompting

#### Scenario: A re-prompt can discard type-ahead

- **WHEN** a node re-prompts after invalid input and declares that buffered digits are
  discarded
- **THEN** digits pressed before the re-prompt do not satisfy the new collection

### Requirement: Collection is bounded by digit count, terminator, and timers

A collection SHALL end on the first of: a configured maximum digit count, a configured
terminator digit, expiry of a first-digit timeout, expiry of an inter-digit timeout, or
expiry of an overall timeout.

The reason a collection ended SHALL be reported distinctly, so a flow can tell no input
from a partial entry that timed out, and so an `ivr` node can route timeout separately
from invalid input.

#### Scenario: A terminator ends collection early

- **WHEN** a collection allows four digits with `#` as terminator and the caller enters
  `12#`
- **THEN** collection ends with `12` and reports that the terminator ended it

#### Scenario: No input is distinguishable from a partial entry

- **WHEN** a caller presses nothing at all, versus presses one digit of an expected three
  and stops
- **THEN** the two cases report different end reasons, so the flow can route them
  differently

#### Scenario: An unmapped digit is invalid, not a timeout

- **WHEN** a caller presses a digit for which the `ivr` node declares no exit
- **THEN** the node takes its `invalid` exit rather than its `timeout` exit
