package dialplan

import (
	"reflect"
	"testing"
)

// mustCompile lives in digitmap_test.go, next to the vocabulary it compiles.

// The vector is the accepted-set size at each position. A literal accepts one
// digit, N accepts eight, X accepts ten.
func TestSpecificityIsThePerPositionSetSize(t *testing.T) {
	cases := map[string][]int{
		"100":  {1, 1, 1},
		"1NX":  {1, 8, 10},
		"1Z":   {1, 9},
		"[24]": {2},
	}
	for raw, want := range cases {
		if got := mustCompile(t, raw).Specificity(); !reflect.DeepEqual(got, want) {
			t.Errorf("%q specificity = %v, want %v", raw, got, want)
		}
	}
}

// A trailing wildcard must lose to any real class, which is what makes 1XX beat
// 1. — otherwise a block pattern would swallow the literal carved out of it.
func TestTheWildcardIsWiderThanAnyRealClass(t *testing.T) {
	tail := mustCompile(t, "1.").Specificity()
	if len(tail) != 2 {
		t.Fatalf("specificity = %v, want a position for the tail", tail)
	}
	if tail[1] <= 10 {
		t.Errorf("the wildcard reports %d, which does not beat X's 10", tail[1])
	}
}

// The exported ordering must agree with the comparison the router uses. If these
// ever disagree, a UI shows a different winner than the one a caller reaches.
func TestRankingAgreesWithDominationForEveryComparablePair(t *testing.T) {
	raws := []string{"100", "1XX", "1NX", "1.", "2000", "[2-8]XX", "X", "*8"}
	ranked, err := RankPatterns(raws)
	if err != nil {
		t.Fatalf("RankPatterns: %v", err)
	}
	if len(ranked) != len(raws) {
		t.Fatalf("ranked %d patterns, want %d", len(ranked), len(raws))
	}

	order := map[string]int{}
	for _, r := range ranked {
		order[r.Pattern] = r.Order
	}
	for _, a := range raws {
		for _, b := range raws {
			if a == b {
				continue
			}
			if mustCompile(t, a).Dominates(mustCompile(t, b)) && order[a] > order[b] {
				t.Errorf("%q is more specific than %q but is ranked after it (%d vs %d)",
					a, b, order[a], order[b])
			}
		}
	}
}

// The narrowest pattern is tried first, and Order is 1-based so it reads as a
// position rather than an index.
func TestRankingPutsTheNarrowestFirst(t *testing.T) {
	ranked, err := RankPatterns([]string{"1.", "1XX", "100"})
	if err != nil {
		t.Fatalf("RankPatterns: %v", err)
	}
	want := []string{"100", "1XX", "1."}
	for i, r := range ranked {
		if r.Pattern != want[i] {
			t.Errorf("position %d = %q, want %q (full order: %v)", i+1, r.Pattern, want[i], ranked)
		}
		if r.Order != i+1 {
			t.Errorf("%q has Order %d, want %d", r.Pattern, r.Order, i+1)
		}
	}
}

// A table must not reshuffle under an operator between two renders of the same
// data, which means incomparable patterns need a deterministic tiebreak.
func TestRankingIsStable(t *testing.T) {
	raws := []string{"9XX", "8XX", "7XX", "6XX"}
	first, err := RankPatterns(raws)
	if err != nil {
		t.Fatalf("RankPatterns: %v", err)
	}
	for i := 0; i < 5; i++ {
		again, err := RankPatterns(raws)
		if err != nil {
			t.Fatalf("RankPatterns: %v", err)
		}
		if !reflect.DeepEqual(first, again) {
			t.Fatalf("ranking is not stable:\n%v\n%v", first, again)
		}
	}
}

// An editor needs every conflict, not the first one, so a table can be fixed in
// one pass rather than by saving five times.
func TestConflictsReportsEveryAmbiguousPair(t *testing.T) {
	// N and Z overlap on 2-9 with neither narrower (8 vs 9 at position 0 makes N
	// narrower — so use pairs that genuinely tie).
	raws := []string{"NX", "XN", "ZX", "XZ"}
	got, err := Conflicts(raws)
	if err != nil {
		t.Fatalf("Conflicts: %v", err)
	}
	if len(got) < 2 {
		t.Fatalf("reported %d conflicts, want every ambiguous pair: %v", len(got), got)
	}

	// Each reported pair really is ambiguous: overlapping, neither dominating.
	for _, c := range got {
		a, b := mustCompile(t, c.A), mustCompile(t, c.B)
		if !a.Overlaps(b) {
			t.Errorf("%q and %q were reported as conflicting but cannot both match", c.A, c.B)
		}
		if a.Dominates(b) || b.Dominates(a) {
			t.Errorf("%q and %q were reported as conflicting but one is more specific", c.A, c.B)
		}
	}
}

// A table where every overlapping pair has a strictly narrower member is fine,
// and must not be reported. This is the common shape: a literal carved out of a
// block.
func TestNoConflictWhenOneIsAlwaysNarrower(t *testing.T) {
	got, err := Conflicts([]string{"100", "1XX", "1."})
	if err != nil {
		t.Fatalf("Conflicts: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("reported %v, want none — each overlapping pair has a narrower member", got)
	}
}

// Disjoint patterns never compete, so they are never a conflict.
func TestNoConflictBetweenDisjointPatterns(t *testing.T) {
	got, err := Conflicts([]string{"1XX", "2XX", "3XX"})
	if err != nil {
		t.Fatalf("Conflicts: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("reported %v, want none", got)
	}
}

// The loader and the editor must not be able to disagree about what ambiguous
// means: compilation still refuses exactly what Conflicts reports.
func TestCompilationRefusesWhatConflictsReports(t *testing.T) {
	raws := []string{"NX", "XN"}
	found, err := Conflicts(raws)
	if err != nil {
		t.Fatalf("Conflicts: %v", err)
	}
	if len(found) == 0 {
		t.Fatal("expected these to conflict")
	}

	entries := map[string]Entry{}
	for _, raw := range raws {
		entries[raw] = Entry{Destination: "user/100"}
	}
	if _, err := CompileDigitMap(entries); err == nil {
		t.Error("CompileDigitMap accepted a table Conflicts reports as ambiguous")
	}
}

// A pattern that does not compile is reported as such rather than silently
// dropped from the ordering, which would show a table missing a row.
func TestRankingAndConflictsRefuseABadPattern(t *testing.T) {
	if _, err := RankPatterns([]string{"1XX", "1[9-2]"}); err == nil {
		t.Error("RankPatterns accepted an invalid pattern")
	}
	if _, err := Conflicts([]string{"1XX", "1[9-2]"}); err == nil {
		t.Error("Conflicts accepted an invalid pattern")
	}
}
