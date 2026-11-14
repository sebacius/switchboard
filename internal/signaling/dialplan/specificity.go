package dialplan

import (
	"fmt"
	"sort"
)

// Making the router's own ordering legible to something outside this package.
//
// The routing table's entry patterns have no priority number — specificity is
// COMPUTED from how narrow each position is, which is the whole reason the
// vocabulary is digit-map classes rather than regular expressions. That is the
// right design and it has one cost: an operator reading the file cannot see the
// order their patterns will actually be tried in, because it is nowhere in the
// file.
//
// So the order is exported. Not the comparison rebuilt somewhere else — the
// comparison itself, the same Dominates and Overlaps the loader uses. A second
// implementation of "more specific" in a template or in JavaScript would be a
// second opinion about which extension a caller reaches, and the failure mode is
// a UI that confidently shows the wrong winner.

// Specificity returns the per-position size of the accepted digit set, narrowest
// first meaning most specific.
//
// A VECTOR, never a score. digitmap.go's own reasoning applies: comparison is
// per-position precisely because a weighted scalar collapses unlike patterns
// onto the same number and invites collisions. Exporting a single number would
// hand a caller the thing this package refused to compute, and they would sort
// by it and get a different answer than the router.
//
// The trailing wildcard reports a cardinality larger than any real class, so a
// wildcard position always loses — which is what makes "1XX" beat "1.".
func (p *Pattern) Specificity() []int {
	return p.cardinalities(p.width())
}

// Rank is one pattern's place in match order.
type Rank struct {
	Pattern string `json:"pattern"`
	// Order is the display index, 1-based: 1 is tried first.
	Order int `json:"order"`
	// Specificity is the per-position accepted-set size.
	Specificity []int `json:"specificity"`
}

// RankPatterns puts a set of patterns in the order the router will try them,
// narrowest first.
//
// The honest limit, and callers should say so where they render this: Dominates
// is a PARTIAL order. For two patterns that can match the same input, this order
// is the real one. For two that cannot, their relative position is arbitrary —
// they never compete, so there is no answer to give. And where two overlap with
// neither dominating, that is not a ranking question at all: it is a
// configuration error, which Conflicts reports.
//
// Ties break on the raw string, so the order is stable across calls and a table
// does not reshuffle under an operator between renders.
func RankPatterns(raws []string) ([]Rank, error) {
	compiled := make([]*Pattern, 0, len(raws))
	for _, raw := range raws {
		p, err := CompilePattern(raw)
		if err != nil {
			return nil, fmt.Errorf("pattern %q: %w", raw, err)
		}
		compiled = append(compiled, p)
	}

	sort.SliceStable(compiled, func(i, j int) bool {
		a, b := compiled[i], compiled[j]
		av, bv := a.cardinalities(maxInt(a.width(), b.width())), b.cardinalities(maxInt(a.width(), b.width()))
		for k := range av {
			if av[k] != bv[k] {
				return av[k] < bv[k]
			}
		}
		return a.raw < b.raw
	})

	out := make([]Rank, 0, len(compiled))
	for i, p := range compiled {
		out = append(out, Rank{Pattern: p.raw, Order: i + 1, Specificity: p.Specificity()})
	}
	return out, nil
}

// Conflict is two patterns that can match the same input with neither more
// specific, so which one wins is undefined.
type Conflict struct {
	A string `json:"a"`
	B string `json:"b"`
}

// Conflicts reports EVERY ambiguous pair in a set of patterns.
//
// CompileDigitMap answers the same question and stops at the first pair, which
// is right for a loader: it is refusing to start and one reason is enough. An
// editor wants the opposite — tell me everything wrong with this table so I can
// fix it in one pass rather than saving five times.
//
// Both go through this function, so what an editor warns about and what the
// loader refuses cannot drift apart.
func Conflicts(raws []string) ([]Conflict, error) {
	compiled := make([]*Pattern, 0, len(raws))
	for _, raw := range raws {
		p, err := CompilePattern(raw)
		if err != nil {
			return nil, fmt.Errorf("pattern %q: %w", raw, err)
		}
		compiled = append(compiled, p)
	}
	return conflictsAmong(compiled), nil
}

// conflictsAmong is the pairwise check, over already-compiled patterns.
func conflictsAmong(patterns []*Pattern) []Conflict {
	var out []Conflict
	for i := 0; i < len(patterns); i++ {
		for j := i + 1; j < len(patterns); j++ {
			a, b := patterns[i], patterns[j]
			if !a.Overlaps(b) {
				continue
			}
			if a.Dominates(b) || b.Dominates(a) {
				continue
			}
			out = append(out, Conflict{A: a.Raw(), B: b.Raw()})
		}
	}
	return out
}
