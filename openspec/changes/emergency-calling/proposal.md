## Why

A PBX that cannot reliably dial 911 is a liability, and Switchboard currently
cannot. This is not a gap in polish; it is the one failure mode where the cost
is measured in something other than money.

Nothing anywhere special-cases an emergency number. Three separate paths each
break it on their own:

- A tenant with the documented-safe default `allow_external_dial: false` cannot
  dial 911 **at all**. That default is correct for toll-fraud and wrong here.
- A tenant with external dialing enabled but an empty `external_allowlist`
  cannot either, because the allowlist is deny-by-omission.
- `deterministic-call-flows` added digit-map patterns, and a perfectly
  reasonable `"9."` outbound entry silently swallows `911` — `9` followed by
  `11` matches it. That change shipped warning-only validator checks precisely
  because it made this worse and could not responsibly fix it in the same
  breath.

The legal position is specific rather than general. **Kari's Law** (47 U.S.C.
§ 623) requires direct 911 dialing with no prefix, and on-site notification
when a call is placed. **RAY BAUM'S Act** (47 CFR § 9.16) requires a
dispatchable location — a street address plus enough detail to find the caller
inside the building — conveyed with the call. Both apply to multi-line
telephone systems, which is what this is.

The deadline is not "before GA". It is **before this system carries production
traffic at all**, because the first real call it carries might be the one that
matters.

## What Changes

**Emergency routing becomes un-configurable.** A hard-coded emergency route
that bypasses Class of Service entirely. Not a tenant default, not a template,
not something an operator can override: a tenant able to edit the emergency
route is a tenant able to break it, and the failure is silent until someone
dials.

**Emergency numbers are matched before anything else.** Ahead of call
retrieval, ahead of the entry mapping, ahead of every pattern. No digit map can
shadow, capture or transform them.

**Prefix-stripped dialing is accepted, not required.** Kari's Law requires
`911` to work with no prefix; a site that trains people to dial `9` first must
keep working too, so `9911` routes as an emergency call while `911` needs no
prefix at all.

**Dispatchable location per registration.** A location record — civic address
plus a location-within-building detail — attached to an extension or a
registration, carried on the outbound emergency INVITE (PIDF-LO, or the
carrier's expected form).

**On-site notification.** A configured target notified when an emergency call is
placed: at minimum a call to a front-desk extension or a webhook, so someone in
the building knows to open the door for paramedics.

**Emergency calls bypass admission.** A tenant at its channel limit must still
be able to dial 911. The limit exists to bound resources; this is the one case
where the resource is spent regardless.

**Emergency calls are never denied by the spend breaker**, never subject to the
allowlist, and never blocked by a barred prefix.

**Records.** An emergency call is recorded with its location, the notification
result, and the outcome — the audit trail a regulator asks for.

**BREAKING (configuration):** a tenant that can reach the PSTN and has no
emergency route configured fails to load. Today it warns; a warning that is
ignored for six months is not a control.

## Capabilities

### New Capabilities
- `emergency-calling`: emergency number recognition ahead of all routing,
  un-configurable bypass of Class of Service and admission, dispatchable
  location, on-site notification, and the records for both.

### Modified Capabilities
- `tool-authorization`: Class of Service gains exactly one exception, stated
  explicitly rather than as an implicit hole — an emergency destination is
  never denied, never allowlisted, never barred, never spend-limited.
- `call-admission`: emergency calls are admitted regardless of the channel
  limit.
- `call-flows`: emergency numbers are matched before the entry mapping, and no
  flow, pattern or transform can intercept them.

## Impact

**Code:** emergency matching in front of `flow.Engine.Handle`; a bypass path in
`agent.Policy`; a location store keyed by extension or registration; PIDF-LO
construction in the outbound INVITE; a notification dispatcher.

**Configuration:** a new emergency block — the route, the notification target,
and per-extension locations. Validation upgraded from warning to error for a
PSTN-capable tenant without one.

**Operational:** emergency calling cannot be verified against a live PSTN
without placing a real 911 call, which must not be done casually. Verification
needs a carrier test number (933 with many US carriers, which reads back the
location the carrier received), a lab SIP trunk, or a carrier-provided staging
endpoint. **This is the single most important thing to get right in the change
and the hardest to test**, and the plan must say how before any code is written.

**Scope note:** this is deliberately US-centric because Kari's Law and RAY
BAUM'S Act are US statutes. The mechanism — un-configurable routing, location,
notification — generalises, but the number set and the location format do not.
Non-US emergency handling is a separate change.
