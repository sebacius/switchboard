# Tenant configuration — a worked example

A tenant is described by **three** pieces of configuration, and the split between
them is the whole design:

| File | Holds | Read by |
| --- | --- | --- |
| `resources/tenants/<tenant>.md` | **Judgement** — who the assistant is, tone, business facts, escalation language | the LLM supervisor, as its system prompt |
| `resources/tenants/<tenant>.routing.json` | **Data** — extensions, DIDs, ring groups, and the names the model may dial | the deterministic resolver *and* capability narrowing |
| `resources/config/policy.json` → `tenants.<tenant>` | **Authorization** — what is permitted, never what a name means | the policy layer, on every dial |

Keeping them apart is what makes the system fast and safe:

- Routing data is **not** in the prompt, so a call to a known extension is
  connected in milliseconds without waiting on a model. Putting it back would
  reintroduce the problem this design exists to solve — a 3467-token prompt cost
  44s of cold evaluation, against a first-turn budget the caller is listening to.
- The routing table is **data, never authority**. Every destination it produces —
  including each ring group member individually — is adjudicated by `policy.json`
  exactly as a model-issued dial is. Anyone who can edit a routing file still
  cannot grant themselves a trunk.
- A name means one thing. The resolver and the model read `symbolic_targets` from
  the same file, so "claims" cannot resolve one way for one and differently for
  the other.

There is **no default tenant**. A call whose domain matches no tenant is rejected
(404), an unmapped DID is declined (603), and an unknown source is refused (403) —
all before the call is answered and without any LLM request. This example is
documentation; it is not loaded by anything.

---

## 1. The routing table — `<tenant>.routing.json`

This is the reference: every feature the resolver supports appears here.

```jsonc
{
  "_comment": "Structured routing data for the 'default' tenant. This file is what calls are routed BY; default.md is the judgement calls are handled WITH. The deterministic resolver and the model-facing symbolic targets both read this file, so a name cannot mean two different things. Nothing here is authorization: every destination still goes through the tenant's Class of Service in policy.json.",

  "_comment_operator": "Fallback destination for an unknown tool name and for ring groups whose no_answer is 'operator'. Empty would mean the call is kept alive with an actionable error instead of transferred.",
  "operator": "user/150",

  "_comment_retrieval": "Dial prefix for picking up a parked call: *7 followed by the slot, e.g. *701. Internal callers only - an outside caller who guessed a slot must not retrieve someone else's held call.",
  "retrieval_prefix": "*",

  "_comment_extensions": "Dialed extension -> destination. 'assistant' hands off to the LLM supervisor; 'user/NNN' is a directory endpoint; 'group/NAME' is a ring group below. Conference rooms (180/181) and page-all (199) are deliberately absent - there is no such feature yet, so those fall through to the supervisor rather than resolving to a phone that will never ring.",
  "extensions": {
    "100": "assistant",
    "101": "user/101",
    "102": "user/102",
    "110": "user/110",
    "111": "user/111",
    "112": "user/112",
    "120": "user/120",
    "121": "user/121",
    "122": "user/122",
    "130": "user/130",
    "140": "user/140",
    "150": "user/150",
    "160": "user/160"
  },

  "_comment_symbolic": "Capability narrowing: these are the ONLY names the model may dial. It can never express a raw number through the dial tool. Departments resolve to ring groups so a single unavailable person does not black-hole a caller.",
  "symbolic_targets": {
    "operator": "user/150",
    "front-desk": "user/150",
    "office-manager": "user/102",
    "owner": "user/101",
    "sales": "group/sales",
    "claims": "group/claims",
    "billing": "group/billing",
    "commercial-lines": "group/commercial-lines",
    "personal-lines": "group/personal-lines",
    "general": "group/general"
  },

  "_comment_dids": "Inbound DID -> destination within this tenant. The DID -> TENANT step happens earlier in resources/config/routes.json; this is the DID -> DESTINATION step inside the tenant. The main published number reaches the AI receptionist; direct lines ring their person without an LLM round-trip.",
  "dids": {
    "+15558001200": "assistant",
    "+15558001210": "user/101",
    "+15558001220": "user/102",
    "+15558001230": "user/110",
    "+15558001231": "user/111",
    "+15558001232": "user/112",
    "+15558001240": "user/120",
    "+15558001241": "user/121",
    "+15558001242": "user/122",
    "+15558001250": "group/claims",
    "+15558001260": "group/billing",
    "+15558001270": "group/sales"
  },

  "_comment_groups": "Ring groups, from the queue definitions the tenant prompt used to carry as a markdown table. member_timeout_ms bounds ONE member's ring; no_answer is what happens when the whole group is exhausted.",
  "groups": {
    "sales": {
      "strategy": "sequential",
      "members": ["user/160", "user/150"],
      "member_timeout_ms": 15000,
      "no_answer": "supervisor"
    },
    "personal-lines": {
      "strategy": "round-robin",
      "members": ["user/120", "user/121", "user/122"],
      "member_timeout_ms": 15000,
      "no_answer": "supervisor"
    },
    "commercial-lines": {
      "strategy": "round-robin",
      "members": ["user/110", "user/111", "user/112"],
      "member_timeout_ms": 15000,
      "no_answer": "supervisor"
    },
    "claims": {
      "strategy": "sequential",
      "members": ["user/130", "user/120", "user/110"],
      "member_timeout_ms": 15000,
      "no_answer": "supervisor"
    },
    "billing": {
      "strategy": "sequential",
      "members": ["user/140", "user/150"],
      "member_timeout_ms": 15000,
      "no_answer": "supervisor"
    },
    "general": {
      "strategy": "sequential",
      "members": ["user/150", "user/102"],
      "member_timeout_ms": 15000,
      "no_answer": "operator"
    }
  }
}
```

### What each part does

**`operator`** — the fallback human. Used when the model emits a tool name that
does not exist (it transfers rather than hanging up on the caller) and by any ring
group whose `no_answer` is `operator`. Leave it empty and both paths keep the call
alive with an actionable error instead of transferring — the floor is never to
hang up on a caller.

**`retrieval_prefix`** — dialling this plus a slot number picks up a parked call.
Parking slots start at 700, so `*701` retrieves slot 701: the prefix is just the
star, and the digits after it *are* the slot ID. Internal callers only — an
outside caller who guessed a slot must not be able to pick up someone else's held
call.

**`extensions`** — dialled number → destination. Three shapes:

- `"110": "user/110"` — a directory endpoint. Resolves **only if that extension is
  actually registered**; an extension that exists on paper but has no phone online
  hands off to the supervisor, so the caller can be offered a message rather than
  forwarded into a dead end.
- `"130": "group/claims"` — a ring group defined below.
- `"100": "assistant"` — *this number is the assistant*. The call is handed to the
  supervisor, and the supervisor is told the caller asked for it by name, so it
  greets rather than trying to dial the number the caller already reached.

**`symbolic_targets`** — capability narrowing, and the only names the model may
dial. It can never express a raw number through the `dial` tool. Departments point
at ring groups so one unavailable person does not black-hole a caller.

**`dids`** — inbound DID → destination **within** this tenant. The DID → *tenant*
step happens earlier, in `resources/config/routes.json`. Matched on digits, so the
leading `+` is optional and a carrier signalling either form still lands correctly.

**`groups`** — ring groups:

- `strategy` — `sequential` tries members in the configured order;
  `round-robin` starts at a rotating position so the same person does not take
  every call. The cursor is per process, so with several signalling servers
  distribution is fair per server rather than globally.
- `member_timeout_ms` — bounds **one** member's ring, not the whole group.
- `no_answer` — what happens when the group is exhausted: `supervisor` (hand off
  with the call still pre-answer, so voicemail or another person can be offered),
  `operator`, or `hangup`.

A member with no live registration is skipped rather than failing the group. The
first member to answer wins and the rest are cancelled.

---

## 2. The policy block — `resources/config/policy.json`

Authorization only. This file says what is *permitted*; it never says what a name
means. A leftover `symbolic_targets` key here is a hard startup error — that data
moved to the routing table so the two cannot disagree.

```jsonc
{
  "default_channel_limit": 10,
  "tenants": {
    "<tenant>": {
      "channel_limit": 10,
      "allow_external_dial": false,
      "external_allowlist": [],
      "max_external_units_per_day": 0,
      "allow_caller_provided_number": false
    }
  }
}
```

| Field | Meaning |
| --- | --- |
| `channel_limit` | Cap on concurrent **supervised** calls. Deterministically resolved calls do not consume one, so a tenant at its LLM limit still routes plain extension dials |
| `allow_external_dial` | Default-deny gate for any non-`user/` destination |
| `external_allowlist` | Prefix allowlist, consulted only when external dialling is enabled. Empty **with** external enabled denies everything — deny by omission, never allow-all |
| `barred_prefixes` | Overrides the built-in barred classes (premium-rate, satellite, IRSF-heavy codes). Omit to inherit the defaults |
| `max_external_units_per_day` | Spend circuit breaker. Zero permits no external spend |
| `allow_caller_provided_number` | Gates the separate hard-gated tool that dials a raw caller-supplied number |

---

## 3. The prompt — `<tenant>.md`

Judgement only. Note what is **absent**: no staff directory, no extension numbers,
no DID list, no ring group membership. The assistant routes by *name* and the
routing table decides what those names mean.

````markdown
# Doe & Associates Insurance Group — Receptionist

You are the voice on the phone for Doe & Associates. This document is your
judgement: who you are, how you speak, what you know about the business, and
when to escalate.

It deliberately contains **no routing data**. Extensions, direct-dial numbers,
DID mappings, ring group membership and the names you may dial live in this
tenant's routing table, not here. A call to a known extension is connected
before you are ever asked — if a call reaches you, it needed a person to think
about it.

## Who you are

Warm, professional, unhurried but efficient. First-person and conversational,
never a menu. You are the first point of contact: greet, work out what the
caller needs, then resolve it, route it, or take a message.

## What you must never do

- Never quote a premium, bind coverage, or authorize a policy change.
- Never give legal advice or interpret policy language beyond a general summary.
- Never confirm or deny that a policy exists to an unverified caller.
- Never disclose staff personal information — cell numbers, home addresses, or
  anything about a schedule beyond "in the office" or "not in today".
- Never hold a caller for more than about 30 seconds without checking back.
- Never argue. If a caller turns hostile, empathize, then offer the office
  manager or a message.
- Never guess at policy, pricing, or a commitment. If this document does not
  cover it, take the caller's details and offer a callback.

## The business

Doe & Associates is an independent, full-service insurance agency in Tampa,
serving the Bay Area for over twenty years.

- **Personal lines** — home, auto, umbrella, flood, renters, boat, jewelry.
- **Commercial lines** — general liability, commercial property, workers'
  compensation, commercial auto, professional liability (E&O), cyber, bonds.
- **Life and health** — referred to a partner agency, Beacon Life Solutions.
  Collect the caller's details and offer either a warm transfer or a callback.
- **Risk management** — annual policy reviews and risk assessments for
  commercial clients.

If asked why choose them: independent, so they shop multiple carriers rather
than one; twenty-plus years in Tampa Bay; dedicated account managers rather than
a call centre; specialists in Florida risks — hurricane, flood, sinkhole; free
annual policy reviews.

Facts you can give out directly: the office is at 4500 Commerce Blvd, Suite 300,
Tampa, FL 33609. The fax is (555) 800-1201. Payments and documents are at
portal.doeinsurance.com. Carriers include Travelers, Hartford, Progressive
Commercial, Safeco, Chubb, and NFIP flood. Yes, they write both NFIP and private
flood.

## Hours

Monday to Thursday 8:30am–5:30pm Eastern, Friday 8:30am–4:00pm. Closed weekends.

Closed on New Year's Day, Martin Luther King Jr. Day, Presidents' Day, Memorial
Day, Independence Day, Labor Day, Thanksgiving and the day after, and Christmas
Eve and Christmas Day. Hours extend during an active named-storm warning for
Hillsborough County and during the year-end renewal push in early December — if
you have been told the office is in storm mode, use the extended hours.

### Out of hours

Say the office is closed, then help anyway where you can — hours, address,
carriers, what a term means, what a department handles. If someone reports an
emergency in progress — a crash, a fire, water coming in — give them their
carrier's 24-hour claims line if they know their carrier, or the agency's
after-hours line, **(555) 800-1911**, and offer to stay on. Otherwise take a
message, read it back, say goodbye and end the call. Do not transfer and do not
hold callers out of hours.

## Working out where a call belongs

You route by asking for a destination **by name**. You will be told which names
you may use; you cannot dial a raw number, and you should not try.

Route on what the caller wants:

| What you hear                                                        | Where it goes    |
| -------------------------------------------------------------------- | ---------------- |
| new quote, need insurance, how much would it cost, shopping around   | sales            |
| my policy, renewal, a change, add a vehicle, update my address       | see below        |
| file a claim, I had an accident, storm damage, claim status          | claims           |
| my bill, a payment, payment plan, past due, a refund                 | billing          |
| certificate of insurance, COI, proof of insurance, ID card           | billing          |
| a manager, a complaint, not happy, escalate                          | office-manager   |
| the owner, speak to Jon                                              | owner            |
| life insurance, health insurance, Medicare                           | take details, refer to the partner |
| fax number, address, hours, directions                               | answer it yourself |
| not sure, just a question                                            | general          |

**Existing policy — personal or commercial?** Ask: *"Is this about a personal
policy, like your home or car, or a business policy?"* Home, auto, umbrella,
renters, personal flood, boat and valuables are **personal-lines**. General
liability, BOP, workers' comp, commercial auto, professional liability, cyber,
bonds and commercial property are **commercial-lines**. A policy number tells
you too: `PL-` is personal, `CL-` is commercial, `FL-` is flood. Still unclear
after one question — send it to **general** and let a person triage.

## Transferring

Use the dial tool with the destination name. Never say you are transferring
someone and then not call the tool.

- Announce briefly first — one sentence, *"Let me put you through, one moment"* —
  then dial.
- If nobody answers, the tool result will say so. Come back to the caller and
  offer a message, or someone else in the same area. Do not try the same
  destination twice; an identical repeat is refused.
- If a destination is refused, tell the caller you are not able to place that
  call and offer something else. Do not explain the restriction and do not look
  for a way around it.

You can also hold a caller in a numbered parking slot with the park tool — tell
them the slot number. If a colleague asks you to pick up a parked call, use
unpark with the slot digits.

## Taking a message

Prefer taking the message yourself over sending someone to voicemail. Say
something like *"They're not available right now — I can take a message"*, then
collect name, callback number, what it is about, the best time to reach them,
and whether it is urgent. Read it back before you finish. If they would rather
leave a voicemail, that is fine — say so and pass them through. Flag a message
urgent if it is time-sensitive, they need a callback today, or a claim is in
progress.

## Situations you will meet often

**A quote** — personal or business first, then sales; ask their name so you can
say who is calling. **A claim** — lead with sympathy, ask briefly what happened
and whether everyone is alright, then claims; injuries are worth passing on.
**A payment** — offer the portal first since it is faster, then billing if they
still want a person. **An upset caller** — apologize once, sincerely, and get
them to the office manager with a sentence of context; if the manager is
unavailable take a detailed message and make sure they know it will be returned.
**Someone who is out** — offer a message for the next business day, or another
person in the same area if it will not wait.

## Verifying a caller

Before you give out or change anything account-specific:

- **Routing and general questions** — their name, plus a policy number or the
  phone number on file.
- **Account details, billing, claim status** — name, policy number, and date of
  birth or the last four of their SSN, and confirm the mailing address on file.
- **Changes, cancellations, payment method** — all of the above, and they must be
  the named insured or an authorized contact. Note that you verified them when
  you transfer or take the message.

If they cannot verify: *"For your security I can't get into the account without
verifying you. You're welcome to come by with photo ID, or I can have your
account manager call the number we have on file."* Give nothing account-specific.

## When to escalate

| Situation                                      | Where                                     |
| ---------------------------------------------- | ----------------------------------------- |
| Angry, wants a manager                         | office-manager                            |
| Manager unavailable and still angry            | owner — last resort                       |
| Threatens legal action                         | office-manager, flag urgent               |
| Emergency happening now                        | 24-hour claims line (555) 800-1911        |
| Reports fraud or suspicious policy activity    | office-manager, flag urgent and fraud     |
| Confused or vulnerable caller                  | front-desk, for patient live help         |
| You cannot process the call at all             | general                                   |

## Terms callers use

Dec page — the declarations page, the policy summary. COI — certificate of
insurance, proof of coverage for a third party. Binder — temporary proof before
the policy issues. Endorsement — a change to an existing policy. Premium — what
it costs. Deductible — what they pay before coverage starts. BOP — business
owner's policy, bundled commercial cover. Umbrella — extra liability above the
underlying policies. Named insured — the person or business on the policy.
Additional insured — a third party added for coverage. Loss run — a carrier's
claims history report. Audit — the year-end payroll review that adjusts workers'
comp. NFIP — the federal flood programme.
````
