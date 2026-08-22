# Tenant configuration — a worked example

A tenant is described by **two** files plus a block in `policy.json`, and the
split between them is the whole design:

| File | Holds | Read by |
| --- | --- | --- |
| `resources/tenants/<tenant>.routing.json` | **Data** — the entry mapping, extensions, DIDs, ring groups, and the names that may be dialled externally | routing, and capability narrowing |
| `resources/tenants/<tenant>.flows.json` | **Behaviour** — menus, prompts, transfers, conditional dialling | the flow engine |
| `resources/config/policy.json` → `tenants.<tenant>` | **Authorization** — what is permitted, never what a name means | the policy layer, on every dial |

One more file matters for inbound calls, and it is deliberately **not** a tenant
file: `resources/config/routes.json` says which tenant a number belongs to. See
§0 below — the fact that the same number appears in two files, answering two
different questions, is the part that most often confuses people.

Keeping them apart is what makes the system safe and predictable:

- The flow graph is **data, never authority**. Every destination it produces —
  including each ring group member individually — is adjudicated by `policy.json`
  exactly as any other dial. Anyone who can edit a flow file still cannot grant
  themselves a trunk.
- A name means one thing. Flows and the policy layer both read
  `symbolic_targets` from the same file, so a name cannot resolve one way for one
  and differently for the other.
- The two tenant files are loaded and validated **as one unit**, so a flow can
  never reference a ring group the other file just removed.

There is **no default tenant**. A call whose domain matches no tenant is rejected
(404), an unmapped DID is declined (603), and an unknown source is refused (403) —
all before the call is answered.

This example is documentation; it is not loaded by anything. `resources/tenants/`
ships only `devtenant`, a smaller fixture for local testing.

---

## 0. Whose number is it? — `routes.json`

An inbound DID is looked up **twice**, and the two lookups answer different
questions. This is the single most confusing thing in the configuration until
you see it stated plainly:

| Step | File | Question | Who may edit it |
|---|---|---|---|
| 1 | `resources/config/routes.json` | **Whose** number is this? DID → tenant | operator only, global |
| 2 | `<tenant>.routing.json` → `dids` | **What happens** to a call to it? DID → destination | the tenant |

They are separate files because a tenant can edit its own routing table through
the config API. If tenants declared their own DIDs, tenant A could add tenant
B's number to its own file and start receiving B's calls. The number-to-tenant
binding has to live where no tenant can write it. The apparent redundancy **is**
the security property.

```jsonc
// resources/config/routes.json — step 1, and nothing more
{
  "dids": {
    "+15558001200": "acme",       // one number
    "+1555800XXXX": "acme"        // ...and the block it sits in
  }
}
```

The tenant then decides what to do with the numbers it owns, in §1's `dids`
block. Following one call through both steps:

```
INVITE from a trunk peer, To: +15558001200
  step 1  routes.json          -> tenant "acme"          (unmapped -> 603 Declined)
  step 2  acme.routing.json    -> "flow/main-ivr"        (unmatched -> the operator)
          the flow runs
```

A call to `+15558009999` is still **acme's** call — the block in step 1 says so —
but matches nothing in step 2, so it reaches acme's operator rather than being
declined. That difference is exactly why the two steps are not one table.

Both steps use the same matcher, so they cannot disagree about whether a number
matches. Either E.164 form works, because a carrier signalling `15558001200`
must find a route written `+15558001200` — which form a given trunk sends is not
something you can know when you write the file. Patterns work, so a block is one
line. The most specific claim wins, so carving a single number out of a block
and handing it to another tenant does what you would expect. And two claims that
overlap with neither more specific **fail at startup naming both tenants**,
because whose calls those are would otherwise be undefined.

`switchboard-signaling validate` cross-checks the two files in both directions —
see §4.

---

## 1. The routing table — `<tenant>.routing.json`

This is the reference: every routing feature appears here.

```jsonc
{
  "_comment": "Structured routing data for the 'acme' tenant. This is what calls are routed BY. Nothing here is authorization: every destination still goes through the tenant's Class of Service in policy.json.",

  // Where a call goes when nothing else claims it. Empty means such a call is
  // declined with 480 rather than hung up on.
  "operator": "user/150",

  // Dial prefix for picking up a parked call: *701 retrieves slot 701.
  // Evaluated BEFORE the entry mapping, so no pattern can shadow it, and
  // permitted for internal callers only.
  "retrieval_prefix": "*",

  // The entry mapping. Keys are literals or digit-map patterns; values are
  // "user/NNN" (an endpoint), "group/NAME" (a ring group), or "flow/NAME"
  // (enter a graph). A bare destination is sugar for a one-node dial, so simple
  // configurations never have to write a flow.
  "extensions": {
    "0":    "user/150",           // dialling 0 reaches the front desk
    "100":  "flow/main-ivr",      // the main menu
    "105":  "user/105",
    "130":  "group/claims",
    "150":  "user/150",

    // Patterns, because extension ranges cannot be enumerated. 2XX is any
    // 200-299 extension; the literals above still win where they overlap,
    // because a literal is narrower than X at that position.
    "2XX":  "flow/dept-lookup",

    // Parked-call retrieval is handled by retrieval_prefix, not here.
    "911":  "user/150"            // see the E911 note at the end — this is NOT
                                  // adequate emergency handling
  },

  // Capability narrowing: the ONLY names a flow may dial externally. A flow can
  // never express a raw number, so editing one cannot reach a premium-rate
  // line. These are also what dial_external's "target" must name.
  "symbolic_targets": {
    "front-desk":        "user/150",
    "sales":             "group/sales",
    "claims":            "group/claims",
    "answering-service": "+15558009999"
  },

  // Step 2: inbound DID -> destination WITHIN this tenant. Step 1 — which
  // tenant owns the number — already happened in routes.json, which is why the
  // same number appears in both files. Both "+1555..." and "1555..." match.
  // A number routed here by step 1 but absent from this map is still OUR call;
  // it simply matches nothing and reaches the operator.
  "dids": {
    "+15558001200": "flow/main-ivr",
    "+15558001250": "group/claims",
    "+15558001299": "user/150"
  },

  // Ring groups. A group does NOT carry its own no-answer destination: what
  // happens when nobody picks up belongs to whatever rang the group, so it is
  // written down in one place instead of two that can disagree.
  "groups": {
    "sales": {
      "strategy": "round-robin",       // rotate the starting member per call
      "members": ["user/110", "user/111", "user/112"],
      "member_timeout_ms": 12000
    },
    "claims": {
      "strategy": "sequential",        // always try members in order
      "members": ["user/130", "user/131"],
      "member_timeout_ms": 20000
    }
  }
}
```

### Entry patterns

Patterns use a digit-map vocabulary — **not** regular expressions:

| Symbol | Matches |
|--------|---------|
| `X` | 0-9 |
| `N` | 2-9 |
| `Z` | 1-9 |
| `[2-8]`, `[147]` | the listed digits or range |
| `.` | one or more further digits; **trailing only** |
| literals | `0-9`, `*`, `#`, `+` |

When more than one pattern matches, the most specific wins — and specificity is
**computed** from how narrow each position's accepted set is:

```
literal 1  ·  [147] 3  ·  [2-8] 7  ·  N 8  ·  Z 9  ·  X 10  ·  "." unbounded
```

`N` beats `Z` because 8 < 9, and `105` beats `1XX` because a literal is narrowest
of all. Nothing declares a priority integer — hand-maintained priorities were the
actual defect in the dialplan this replaces, drifting from intent and forcing
renumbering to insert a rule.

Comparison is per-position, so two patterns that overlap with neither strictly
narrower are a **load error naming both**:

```
error: acme: extensions
  patterns "NX" and "XN" can match the same digits and neither is more specific,
  so which one wins is undefined; make one of them narrower
```

Both match `22`. There is no defensible winner, so asking is the only honest
option. The restricted vocabulary exists precisely so this question is decidable —
it is not, for regular expressions.

---

## 2. The flows — `<tenant>.flows.json`

```jsonc
{
  "flows": {
    "main-ivr": {
      "start": "greeting",

      // Bounds the whole flow. A caller navigating menus is not idle, but the
      // call cannot be held open forever.
      "timeout_ms": 300000,

      "nodes": {
        "greeting": {
          "type": "ivr",
          "entry": {
            "prompt": {
              "text": "Thank you for calling Acme. Press 1 for sales, 2 for claims, or hold for the operator.",
              "voice": "alloy"
            },
            "timeout_ms": 5000,      // wait for the first digit, measured from
                                     // the END of the prompt
            "max_retries": 2,        // re-prompt up to twice before giving up
            "terminator": "#",
            "interruptible": true    // the first digit cuts the prompt short
          },
          "exits": {
            "1": "ring-sales",
            "2": "ring-claims",
            "timeout": "to-operator",
            "invalid": "to-operator",
            "retries_exceeded": "to-operator"
          }
        },

        "ring-sales": {
          "type": "dial_user",
          "entry": { "target": "group/sales", "timeout_ms": 20000 },
          // NOTE: "answered" is absent. It is terminal — the flow ends and the
          // legs bridge — so declaring it is a load error.
          "exits": {
            "no_answer":   "after-hours",
            "busy":        "after-hours",
            "rejected":    "to-operator",
            "unavailable": "to-operator"
          }
        },

        "ring-claims": {
          "type": "dial_user",
          "entry": { "target": "group/claims", "timeout_ms": 25000 },
          "exits": {
            "no_answer":   "to-operator",
            "busy":        "to-operator",
            "rejected":    "to-operator",
            "unavailable": "to-operator"
          }
        },

        "after-hours": {
          "type": "dial_external",
          // Symbolic ONLY. "answering-service" resolves through
          // symbolic_targets; a raw number here is a load error.
          "entry": { "target": "answering-service", "timeout_ms": 30000 },
          "exits": {
            "no_answer": "closing",
            "busy":      "closing",
            "denied":    "closing",   // Class of Service refused it
            "failed":    "closing"
          }
        },

        "to-operator": {
          "type": "dial_user",
          "entry": { "target": "user/150", "timeout_ms": 25000 },
          "exits": {
            "no_answer":   "closing",
            "busy":        "closing",
            "rejected":    "closing",
            "unavailable": "closing"
          }
        },

        "closing": {
          "type": "tts",
          "entry": { "text": "Sorry, nobody is available right now. Please call back during business hours.", "voice": "alloy" },
          "exits": { "done": "bye" }
        },

        "bye": {
          "type": "hangup",
          "entry": { "cause": "normal_clearing" }
        }
      }
    }
  }
}
```

### The node vocabulary

| type | entry | exits |
|---|---|---|
| `ivr` | `prompt`, `timeout_ms`, `max_retries`, `terminator`, `interruptible` | one per digit, `timeout`, `invalid`, `retries_exceeded` |
| `tts` | `text`, `voice`, `interruptible` | `done` |
| `play_audio` | `file`, `interruptible` | `done` |
| `dial_user` | `target`, `timeout_ms` | `answered`\*, `no_answer`, `busy`, `rejected`, `unavailable` |
| `dial_external` | `target` (symbolic only), `timeout_ms` | `answered`\*, `no_answer`, `busy`, `denied`, `failed` |
| `transfer` | `target`, `mode: "blind"` | `accepted`\*, `failed` |
| `hangup` | `cause` | terminal |

\* terminal — the flow ends, the legs bridge, and the cursor is released.

**Exit names are fixed in code, not configuration.** That is what makes a typo
like `no-answer` a startup error rather than a branch that silently never fires,
discovered during an outage. Every non-terminal exit must be wired, so what a
caller hears when the line is busy is always written down somewhere.

### Why the graph must be acyclic

Two rules, both enforced at load:

1. The **inter-node graph is acyclic**.
2. Repetition lives **inside** a node, bounded by a counter — `ivr.max_retries`
   is a self-loop handled within the node, contributing no edge.

Together they make every flow **provably terminating**. A priority-ordered
dialplan cannot offer this: a `Goto` loop is found in production, whereas a cycle
here is found at startup, with the path named:

```
error: acme: flows.main-ivr
  flow contains a cycle: greeting -> menu -> greeting. Flows must be acyclic so
  every call is guaranteed to terminate; repetition belongs inside a node,
  bounded by a counter such as ivr.max_retries
```

The cost is real and worth stating: "press 9 to return to the main menu" is a
cycle, and expressing it means duplicating the menu node. That is the price of
the guarantee.

### When the call is answered

Switchboard does not answer a call in order to route it.

- A dial reached **before** any media node **forwards** the INVITE. The caller
  hears the destination's own ringback and, if it fails, can still receive its
  real final response.
- Once a node plays media, the call is answered, and later dials **bridge** into
  media the system already owns.

So `"105": "user/105"` behaves exactly as a direct extension dial always has: no
200 OK from Switchboard, real ringback, and a real 486 if the extension is busy.

A dial that fails **inside** a flow relays nothing. What the caller hears is the
graph's decision, made by whatever node the failure exit leads to — because once
a 486 has reached the caller, their call is over and no later node can run.

---

## 3. Authorization — `policy.json`

```jsonc
{
  "default_channel_limit": 10,

  "tenants": {
    "acme": {
      // Per-tenant cap on concurrent calls. This is capacity control: every
      // call holds an RTP port, a media session and a goroutine for its life.
      // The slot is taken BEFORE the media session is created, so a tenant over
      // its limit is turned away without consuming a port.
      "channel_limit": 25,

      // Default-deny. With this false, no flow reaches any external
      // destination, whatever the routing file says.
      "allow_external_dial": true,

      // With external enabled, ONLY these prefixes pass.
      "external_allowlist": ["+1555800", "+1555900"],

      // Barred unconditionally, even if an allowlist entry would match.
      // Omitting this uses the built-in defaults (premium-rate, satellite,
      // high-risk ranges).
      "barred_prefixes": ["+1900", "+1976", "+882", "+883"],

      // Daily cap on external calls, per tenant, across the process lifetime.
      "max_external_units_per_day": 500
    }
  }
}
```

`policy.json` says what is **permitted**, never what a name **means**. A leftover
`symbolic_targets` key here is a hard startup error — it belongs in the routing
file, so a name cannot resolve differently depending on who asks.

Class of Service is checked at **load** as well as at call time, so a flow that
could never place its call is caught before a caller finds out:

```
error: acme: flows.main-ivr.nodes.after-hours.entry.target
  destination "+15558009999" is denied by this tenant's class of service
  (destination not in tenant allowlist), so this node could never place its call
```

That check is deliberately side-effect free, so validating a configuration cannot
spend the tenant's daily budget.

---

## 4. Checking it before a caller does

```bash
# Every problem at once, each with its path. Also cross-checks routes.json.
switchboard-signaling validate --routing-path resources/tenants

# Walk a flow against a fake call and print the traversal.
flow-smoke --tenant acme --dialed 100 --digits 2
```

```
dialing "100" as internal for tenant acme

flow "main-ivr", 3 hop(s)
  1. greeting      (ivr)         --2--> digits 2
  2. ring-claims   (dial_user)   --no_answer--> no_answer
  3. to-operator   (dial_user)   --answered--> connected to user/150

path: greeting -> ring-claims -> to-operator
outcome: answered
```

The same traversal is written to the call record when `--cdr-path` is set, which
is what makes "why did this caller end up with the operator" answerable after the
fact rather than a matter of speculation.

`validate` also cross-checks the two DID steps against each other:

```
error:   DID "+15551234567" routes to tenant "ghost", which has no routing file;
         a call to this number would be attributed to a tenant that does not
         exist and rejected with 404
warning: tenant handles DID "+15558001200" but routes.json does not route that
         number to it, so no call will ever arrive on it
```

The first is an error because the call fails. The second is a warning because
the configuration still loads — but it catches the likeliest mistake there is:
adding a number to the tenant file and forgetting the global one.

---

## 5. A warning about `911`

The `"911": "user/150"` line in the routing table above is **not** adequate
emergency handling, and is shown only because a real table would plausibly
contain something like it.

Nothing in Switchboard currently special-cases emergency numbers:

- A tenant with the default `allow_external_dial: false` cannot dial 911 **at
  all**.
- A tenant with external dialling enabled but an empty allowlist cannot either.
- A perfectly reasonable outbound pattern such as `"9."` silently swallows `911`,
  because `9` followed by `11` matches it.

Kari's Law requires direct 911 dialling with no prefix, plus on-site
notification. RAY BAUM'S Act requires a dispatchable location. Emergency routing
must bypass Class of Service entirely and be **un-configurable** — it cannot be
something a tenant is able to get wrong.

The validator warns when a pattern shadows an emergency number, or when a
PSTN-capable tenant has no emergency route. That is a guardrail, not a solution.
**Do not carry production traffic until emergency calling is implemented.**
