package dialplan

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Entry mappings need patterns: extension ranges, DID blocks, and outbound
// number plans cannot be enumerated. The vocabulary is deliberately NOT regular
// expressions.
//
// The reason is specificity. When two patterns both match, the more specific one
// must win, and "more specific" has to be well defined — which it is for digit
// maps and is not for regular expressions. So the vocabulary is restricted to
// per-position character classes, and specificity is COMPUTED from how narrow
// each position is:
//
//	literal 1 · [147] 3 · [2-8] 7 · N 8 · Z 9 · X 10 · trailing wildcard ∞
//
// This is principled rather than assigned. N automatically beats Z because 8 < 9,
// and a set of any size works without a new rule.
//
// Nothing declares a priority integer. Hand-maintained "priority": 20 was the
// actual defect in the dialplan this replaces: it drifted from intent, and
// inserting a rule meant renumbering its neighbors.
//
// Comparison is a per-position VECTOR, not a weighted scalar — a scalar collapses
// unlike patterns onto the same number and invites collisions. Two patterns that
// can match the same input with neither more specific are a config error naming
// both, never a silent tiebreak.

// wildcardCardinality stands in for the trailing wildcard's unbounded set. It is
// larger than any real class, so a wildcard position always loses.
const wildcardCardinality = 1 << 20

// class is one position of a pattern: which digits it accepts, and how many.
type class struct {
	// accept reports whether a digit matches this position.
	accept func(r rune) bool
	// card is the size of the accepted set — the specificity metric.
	card int
	// text is the source form, for error messages.
	text string
}

// Pattern is a compiled digit map.
type Pattern struct {
	raw     string
	elems   []class
	hasTail bool // ends with '.', absorbing any number of further digits
}

// Raw returns the pattern as written.
func (p *Pattern) Raw() string { return p.raw }

// CompilePattern compiles a digit-map pattern.
//
// Vocabulary: X = 0-9, N = 2-9, Z = 1-9, [..] sets and ranges, '.' as a trailing
// wildcard, and literal digits, '*', '#' and '+'.
func CompilePattern(raw string) (*Pattern, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("pattern is empty")
	}

	p := &Pattern{raw: raw}
	runes := []rune(raw)

	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch {
		case r == '.':
			// A wildcard anywhere but the end would make specificity ambiguous
			// and matching non-deterministic, which is the whole thing this
			// vocabulary exists to avoid.
			if i != len(runes)-1 {
				return nil, fmt.Errorf(
					"pattern %q: '.' may only appear at the end, where it absorbs the remaining digits", raw)
			}
			p.hasTail = true

		case r == 'X' || r == 'x':
			p.elems = append(p.elems, digitRange('0', '9', "X"))
		case r == 'N' || r == 'n':
			p.elems = append(p.elems, digitRange('2', '9', "N"))
		case r == 'Z' || r == 'z':
			p.elems = append(p.elems, digitRange('1', '9', "Z"))

		case r == '[':
			end := indexRune(runes[i:], ']')
			if end < 0 {
				return nil, fmt.Errorf("pattern %q: '[' at position %d has no closing ']'", raw, i)
			}
			set, err := compileSet(raw, string(runes[i+1:i+end]))
			if err != nil {
				return nil, err
			}
			p.elems = append(p.elems, set)
			i += end

		case r == ']':
			return nil, fmt.Errorf("pattern %q: ']' at position %d has no opening '['", raw, i)

		case (r >= '0' && r <= '9') || r == '*' || r == '#' || r == '+':
			p.elems = append(p.elems, literal(r))

		default:
			return nil, fmt.Errorf(
				"pattern %q: %q is not part of the digit-map vocabulary (X, N, Z, [sets], '.', "+
					"and literal digits * # +)", raw, string(r))
		}
	}

	if len(p.elems) == 0 && !p.hasTail {
		return nil, fmt.Errorf("pattern %q matches nothing", raw)
	}
	return p, nil
}

func indexRune(rs []rune, target rune) int {
	for i, r := range rs {
		if r == target {
			return i
		}
	}
	return -1
}

func literal(r rune) class {
	return class{accept: func(x rune) bool { return x == r }, card: 1, text: string(r)}
}

func digitRange(lo, hi rune, text string) class {
	return class{
		accept: func(x rune) bool { return x >= lo && x <= hi },
		card:   int(hi-lo) + 1,
		text:   text,
	}
}

// compileSet compiles a bracketed set such as [147] or [2-8].
func compileSet(raw, body string) (class, error) {
	if body == "" {
		return class{}, fmt.Errorf("pattern %q: empty set []", raw)
	}

	accepted := map[rune]bool{}
	rs := []rune(body)
	for i := 0; i < len(rs); i++ {
		// A range such as 2-8.
		if i+2 < len(rs) && rs[i+1] == '-' {
			lo, hi := rs[i], rs[i+2]
			if lo > hi {
				return class{}, fmt.Errorf("pattern %q: set range %c-%c is reversed", raw, lo, hi)
			}
			for r := lo; r <= hi; r++ {
				accepted[r] = true
			}
			i += 2
			continue
		}
		accepted[rs[i]] = true
	}

	return class{
		accept: func(x rune) bool { return accepted[x] },
		card:   len(accepted),
		text:   "[" + body + "]",
	}, nil
}

// Matches reports whether the pattern accepts a dialed string.
func (p *Pattern) Matches(dialed string) bool {
	rs := []rune(dialed)

	if p.hasTail {
		// The tail absorbs the rest, but must absorb at least one digit: "9." is
		// "nine then something", not "nine".
		if len(rs) <= len(p.elems) {
			return false
		}
	} else if len(rs) != len(p.elems) {
		return false
	}

	for i, elem := range p.elems {
		if !elem.accept(rs[i]) {
			return false
		}
	}
	return true
}

// cardinalities returns the per-position specificity vector, padded to n with
// the wildcard's cardinality so unequal-length patterns compare position by
// position.
func (p *Pattern) cardinalities(n int) []int {
	out := make([]int, n)
	for i := range out {
		switch {
		case i < len(p.elems):
			out[i] = p.elems[i].card
		case p.hasTail:
			out[i] = wildcardCardinality
		default:
			// Past the end of a fixed-length pattern: it accepts nothing here.
			out[i] = 0
		}
	}
	return out
}

// Dominates reports whether p is strictly more specific than q: no position is
// wider, and at least one is narrower.
func (p *Pattern) Dominates(q *Pattern) bool {
	n := maxInt(p.width(), q.width())
	pc, qc := p.cardinalities(n), q.cardinalities(n)

	strictlyBetter := false
	for i := 0; i < n; i++ {
		if pc[i] > qc[i] {
			return false
		}
		if pc[i] < qc[i] {
			strictlyBetter = true
		}
	}
	return strictlyBetter
}

func (p *Pattern) width() int {
	if p.hasTail {
		return len(p.elems) + 1
	}
	return len(p.elems)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// Overlaps reports whether two patterns can both match the same input. It is
// exact for this vocabulary, because every element is a single-character class
// and the wildcard is trailing-only — which is precisely why the vocabulary is
// restricted.
func (p *Pattern) Overlaps(q *Pattern) bool {
	// Every fixed position both patterns share must have an intersecting class.
	shared := minInt(len(p.elems), len(q.elems))
	for i := 0; i < shared; i++ {
		if !classesIntersect(p.elems[i], q.elems[i]) {
			return false
		}
	}

	switch {
	case !p.hasTail && !q.hasTail:
		// Both fixed: they must be the same length.
		return len(p.elems) == len(q.elems)
	case p.hasTail && q.hasTail:
		// Both open-ended: the shared prefix intersecting is enough.
		return true
	case p.hasTail:
		// p is open-ended: q must be long enough for p's fixed part, and
		// strictly longer, since the tail needs at least one digit.
		return len(q.elems) > len(p.elems)
	default:
		return len(p.elems) > len(q.elems)
	}
}

// classesIntersect reports whether two positions accept any digit in common.
// The alphabet is small enough to test exhaustively.
func classesIntersect(a, b class) bool {
	for _, r := range "0123456789*#+" {
		if a.accept(r) && b.accept(r) {
			return true
		}
	}
	return false
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// Transform normalises matched digits before they reach the cursor.
//
// The set is CLOSED, and there is no `${}` interpolation anywhere. Template
// substitution in a dialplan is how a routing table becomes a small programming
// language: the moment matched digits can be spliced into a destination,
// symbolic narrowing is bypassed and a config edit can reach an arbitrary
// number. These transforms only ever narrow or reshape what was dialed; they
// cannot construct a target.
type Transform struct {
	// StripDigits removes N leading digits — a "9" outbound prefix, say.
	StripDigits int `json:"strip_digits,omitempty"`
	// StripSuffixDigits removes N trailing digits.
	StripSuffixDigits int `json:"strip_suffix_digits,omitempty"`
	// Normalize is "e164", "digits" or "none".
	Normalize string `json:"normalize,omitempty"`
}

// Normalisation forms.
const (
	NormalizeNone   = "none"
	NormalizeDigits = "digits"
	NormalizeE164   = "e164"
)

// validate rejects a transform that names an unknown normalization.
func (t Transform) validate() error {
	switch t.Normalize {
	case "", NormalizeNone, NormalizeDigits, NormalizeE164:
	default:
		return fmt.Errorf("unknown normalize %q (want %q, %q or %q)",
			t.Normalize, NormalizeNone, NormalizeDigits, NormalizeE164)
	}
	if t.StripDigits < 0 || t.StripSuffixDigits < 0 {
		return fmt.Errorf("strip counts must not be negative")
	}
	return nil
}

// Apply reshapes dialed digits. It never lengthens the string, which is the
// property that keeps a transform from constructing a destination.
func (t Transform) Apply(dialed string) string {
	rs := []rune(dialed)

	if t.StripDigits > 0 {
		if t.StripDigits >= len(rs) {
			return ""
		}
		rs = rs[t.StripDigits:]
	}
	if t.StripSuffixDigits > 0 {
		if t.StripSuffixDigits >= len(rs) {
			return ""
		}
		rs = rs[:len(rs)-t.StripSuffixDigits]
	}

	out := string(rs)
	switch t.Normalize {
	case NormalizeDigits:
		out = keepDigits(out)
	case NormalizeE164:
		digits := keepDigits(out)
		if digits == "" {
			return ""
		}
		out = "+" + digits
	}
	return out
}

// keepDigits strips everything that is not 0-9.
func keepDigits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// DigitMap is a compiled set of patterns with their destinations.
type DigitMap struct {
	entries []mapEntry
}

type mapEntry struct {
	pattern   *Pattern
	dest      string
	transform Transform
}

// Entry is one entry-mapping value. It is either a bare destination string or
// an object naming a destination and a transform, so the simple case stays a
// single line of JSON.
type Entry struct {
	Destination string
	Transform   Transform
}

// UnmarshalJSON accepts either form:
//
//	"110":         "user/110"
//	"9NXXNXXXXXX": {"flow": "outbound", "strip_digits": 1}
func (e *Entry) UnmarshalJSON(data []byte) error {
	var bare string
	if err := json.Unmarshal(data, &bare); err == nil {
		e.Destination = bare
		return nil
	}

	var obj struct {
		Destination string `json:"destination,omitempty"`
		Flow        string `json:"flow,omitempty"`
		Transform
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&obj); err != nil {
		return fmt.Errorf("entry must be a destination string or an object: %w", err)
	}

	switch {
	case obj.Flow != "" && obj.Destination != "":
		return fmt.Errorf("entry sets both \"flow\" and \"destination\"; pick one")
	case obj.Flow != "":
		e.Destination = routingFlowPrefix + obj.Flow
	default:
		e.Destination = obj.Destination
	}
	if e.Destination == "" {
		return fmt.Errorf("entry names no destination")
	}

	e.Transform = obj.Transform
	return e.Transform.validate()
}

// MarshalJSON writes the bare form when there is no transform, so a round trip
// through the config API does not rewrite every simple entry into an object.
func (e Entry) MarshalJSON() ([]byte, error) {
	if e.Transform == (Transform{}) {
		return json.Marshal(e.Destination)
	}
	return json.Marshal(struct {
		Destination string `json:"destination"`
		Transform
	}{e.Destination, e.Transform})
}

// CompileDigitMap compiles an entry mapping and rejects ambiguity.
//
// Two patterns that can match the same input with neither more specific are a
// configuration error, not a tiebreak: there is no defensible winner, so asking
// the operator is the only honest option.
func CompileDigitMap(raw map[string]Entry) (*DigitMap, error) {
	m := &DigitMap{}

	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		p, err := CompilePattern(k)
		if err != nil {
			return nil, err
		}
		m.entries = append(m.entries, mapEntry{
			pattern:   p,
			dest:      raw[k].Destination,
			transform: raw[k].Transform,
		})
	}

	for i := 0; i < len(m.entries); i++ {
		for j := i + 1; j < len(m.entries); j++ {
			a, b := m.entries[i].pattern, m.entries[j].pattern
			if !a.Overlaps(b) {
				continue
			}
			if a.Dominates(b) || b.Dominates(a) {
				continue
			}
			return nil, &AmbiguousPatternsError{A: a.Raw(), B: b.Raw()}
		}
	}

	return m, nil
}

// AmbiguousPatternsError reports two patterns that can match the same input
// with neither more specific.
//
// It is a typed error so a caller can say something more useful than the
// generic message: a DID table knows that the two patterns are two TENANTS
// claiming the same number, which is what the operator actually needs told.
type AmbiguousPatternsError struct {
	A, B string
}

func (e *AmbiguousPatternsError) Error() string {
	return fmt.Sprintf(
		"patterns %q and %q can match the same digits and neither is more specific, "+
			"so which one wins is undefined; make one of them narrower", e.A, e.B)
}

// Lookup returns the destination for a dialed string: the most specific match,
// or false. Ambiguity was rejected at compile time, so the winner is unique.
func (m *DigitMap) Lookup(dialed string) (string, bool) {
	dest, _, ok := m.LookupWithDigits(dialed)
	return dest, ok
}

// LookupWithDigits also returns the dialed digits after the winning entry's
// transform, for a flow that wants to know what was dialed.
//
// The transformed value is data the flow can read; it is never a dial target.
// Pattern matching selects WHAT TO RUN, and only symbolic targets say WHERE TO
// DIAL — which is what stops a config edit reaching an arbitrary number.
func (m *DigitMap) LookupWithDigits(dialed string) (string, string, bool) {
	if m == nil {
		return "", "", false
	}

	var best *mapEntry
	for i := range m.entries {
		e := &m.entries[i]
		if !e.pattern.Matches(dialed) {
			continue
		}
		if best == nil || e.pattern.Dominates(best.pattern) {
			best = e
		}
	}
	if best == nil {
		return "", "", false
	}
	return best.dest, best.transform.Apply(dialed), true
}

// Len reports how many patterns the map holds.
func (m *DigitMap) Len() int {
	if m == nil {
		return 0
	}
	return len(m.entries)
}

// Entries builds an entry map from bare destinations. It keeps test fixtures
// and code-built tables readable, since the object form is only needed when a
// transform is involved.
func Entries(m map[string]string) map[string]Entry {
	if m == nil {
		return nil
	}
	out := make(map[string]Entry, len(m))
	for k, v := range m {
		out[k] = Entry{Destination: v}
	}
	return out
}
