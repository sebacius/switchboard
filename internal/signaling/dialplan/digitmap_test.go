package dialplan

import (
	"strings"
	"testing"
)

func mustCompile(t *testing.T, raw string) *Pattern {
	t.Helper()
	p, err := CompilePattern(raw)
	if err != nil {
		t.Fatalf("CompilePattern(%q): %v", raw, err)
	}
	return p
}

func TestPatternMatching(t *testing.T) {
	cases := []struct {
		pattern string
		dialed  string
		want    bool
	}{
		{"110", "110", true},
		{"110", "111", false},
		{"110", "1100", false}, // fixed length is exact
		{"2XX", "234", true},
		{"2XX", "204", true},
		{"2XX", "134", false},
		{"2XX", "23", false},
		{"NXX", "234", true},
		{"NXX", "134", false}, // N excludes 0 and 1
		{"NXX", "034", false},
		{"ZXX", "134", true}, // Z excludes 0 only
		{"ZXX", "034", false},
		{"[147]XX", "134", true},
		{"[147]XX", "234", false},
		{"[2-8]XX", "534", true},
		{"[2-8]XX", "934", false},
		{"9.", "9123", true},
		{"9.", "91", true},
		{"9.", "9", false}, // the tail needs at least one digit
		{"9.", "8123", false},
		{"*7XX", "*701", true},
		{"*7XX", "*801", false},
		// 9 + a 10-digit NANP number: 11 positions exactly.
		{"9NXXNXXXXXX", "95555551212", true},
		{"9NXXNXXXXXX", "90555551212", false},  // N rejects the leading 0
		{"9NXXNXXXXXX", "915555551212", false}, // 1+ dialing needs its own pattern
		{"91NXXNXXXXXX", "915555551212", true},
	}

	for _, tc := range cases {
		got := mustCompile(t, tc.pattern).Matches(tc.dialed)
		if got != tc.want {
			t.Errorf("%q.Matches(%q) = %v, want %v", tc.pattern, tc.dialed, got, tc.want)
		}
	}
}

// Specificity is computed from how narrow each position is, so the ordering
// falls out of the vocabulary rather than being assigned.
func TestSpecificityOrdering(t *testing.T) {
	// Each pattern is strictly more specific than the one after it.
	ordered := []string{"234", "[24]XX", "[2-8]XX", "NXX", "ZXX", "XXX"}

	for i := 0; i < len(ordered)-1; i++ {
		narrow, wide := mustCompile(t, ordered[i]), mustCompile(t, ordered[i+1])
		if !narrow.Dominates(wide) {
			t.Errorf("%q should be more specific than %q", ordered[i], ordered[i+1])
		}
		if wide.Dominates(narrow) {
			t.Errorf("%q must not be more specific than %q", ordered[i+1], ordered[i])
		}
	}
}

// A literal beats a pattern, and a fixed-length pattern beats a wildcard.
func TestLiteralAndWildcardExtremes(t *testing.T) {
	literal := mustCompile(t, "9123")
	wildcard := mustCompile(t, "9.")
	if !literal.Dominates(wildcard) {
		t.Error("a literal must beat a trailing wildcard")
	}
	if wildcard.Dominates(literal) {
		t.Error("a trailing wildcard must never beat a literal")
	}
}

// The case that motivates vector comparison: NX and XN each win one position, so
// neither dominates. 22 matches both, and there is no defensible winner.
func TestCrossingPatternsAreAmbiguous(t *testing.T) {
	a, b := mustCompile(t, "NX"), mustCompile(t, "XN")

	if !a.Overlaps(b) {
		t.Fatal("NX and XN both match 22, so they overlap")
	}
	if a.Dominates(b) || b.Dominates(a) {
		t.Fatal("neither NX nor XN is more specific; a scalar score would wrongly rank them")
	}

	_, err := CompileDigitMap(Entries(map[string]string{"NX": "user/1", "XN": "user/2"}))
	if err == nil {
		t.Fatal("an ambiguous pair must be rejected at compile time")
	}
	for _, want := range []string{"NX", "XN", "neither is more specific"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should name both patterns and the reason, missing %q: %v", want, err)
		}
	}
}

// Equal-cardinality overlapping sets are ambiguous for the same reason.
func TestEqualCardinalityOverlapIsAmbiguous(t *testing.T) {
	_, err := CompileDigitMap(Entries(map[string]string{"[12]X": "user/1", "[23]X": "user/2"}))
	if err == nil {
		t.Fatal("[12]X and [23]X both match 2X with neither narrower; must be rejected")
	}
}

// Patterns that cannot both match are not ambiguous, however similar they look.
func TestNonOverlappingPatternsCoexist(t *testing.T) {
	for _, m := range []map[string]string{
		{"[12]X": "user/1", "[34]X": "user/2"}, // disjoint sets
		{"2XX": "user/1", "2XXX": "user/2"},    // different lengths
		{"NXX": "user/1", "1XX": "user/2"},     // N excludes 1
	} {
		if _, err := CompileDigitMap(Entries(m)); err != nil {
			t.Errorf("patterns that cannot collide must coexist: %v (%v)", err, m)
		}
	}
}

// A more specific pattern coexisting with a wider one is the normal case, and
// the specific one must win at lookup.
func TestMostSpecificWins(t *testing.T) {
	m, err := CompileDigitMap(Entries(map[string]string{
		"110": "user/110",
		"1XX": "flow/catchall",
		"XXX": "flow/anything",
	}))
	if err != nil {
		t.Fatalf("CompileDigitMap: %v", err)
	}

	cases := map[string]string{
		"110": "user/110",      // exact literal
		"120": "flow/catchall", // 1XX, narrower than XXX
		"920": "flow/anything", // only XXX
	}
	for dialed, want := range cases {
		got, ok := m.Lookup(dialed)
		if !ok {
			t.Errorf("Lookup(%q) found nothing, want %q", dialed, want)
			continue
		}
		if got != want {
			t.Errorf("Lookup(%q) = %q, want %q", dialed, got, want)
		}
	}

	if _, ok := m.Lookup("12"); ok {
		t.Error("a two-digit string must not match three-digit patterns")
	}
}

// The realistic table: extensions, a retrieval prefix, a DID block, and NANP
// outbound all coexisting.
func TestRealisticDigitMapCompiles(t *testing.T) {
	m, err := CompileDigitMap(Entries(map[string]string{
		"0":            "user/100",
		"1XX":          "flow/extensions",
		"*7XX":         "flow/retrieval",
		"9NXXNXXXXXX":  "flow/outbound",
		"+1555800XXXX": "flow/inbound-did",
	}))
	if err != nil {
		t.Fatalf("a realistic table must compile: %v", err)
	}
	if m.Len() != 5 {
		t.Errorf("expected 5 patterns, got %d", m.Len())
	}

	for dialed, want := range map[string]string{
		"0":            "user/100",
		"110":          "flow/extensions",
		"*701":         "flow/retrieval",
		"95555551212":  "flow/outbound",
		"+15558001234": "flow/inbound-did",
	} {
		got, ok := m.Lookup(dialed)
		if !ok || got != want {
			t.Errorf("Lookup(%q) = %q/%v, want %q", dialed, got, ok, want)
		}
	}
}

func TestCompileRejectsBadPatterns(t *testing.T) {
	cases := map[string]string{
		"":       "empty",
		"9.1":    "only appear at the end", // wildcard must be trailing
		"1[23":   "no closing",
		"1]":     "no opening",
		"1[]":    "empty set",
		"1[8-2]": "reversed",
		"1A2":    "not part of the digit-map vocabulary",
	}

	for raw, wantMsg := range cases {
		_, err := CompilePattern(raw)
		if err == nil {
			t.Errorf("CompilePattern(%q) should have failed", raw)
			continue
		}
		if !strings.Contains(err.Error(), wantMsg) {
			t.Errorf("CompilePattern(%q) error = %v, want it to mention %q", raw, err, wantMsg)
		}
	}
}

// Nothing in the vocabulary lets a configuration declare a priority. This is a
// guard against reintroducing the defect the digit map replaced.
func TestNoPriorityCanBeDeclared(t *testing.T) {
	if _, err := CompilePattern("20"); err != nil {
		t.Fatalf("a literal must still compile: %v", err)
	}
	// A pattern is a pattern; there is no numeric rank anywhere in the API.
	p := mustCompile(t, "2XX")
	if p.Raw() != "2XX" {
		t.Errorf("Raw() = %q, want the pattern as written", p.Raw())
	}
}

// Transforms are a CLOSED set with no interpolation. Template substitution is
// how a routing table becomes a small programming language, and the moment
// matched digits can be spliced into a destination, symbolic narrowing is
// bypassed.
func TestTransformsReshapeDialledDigits(t *testing.T) {
	cases := []struct {
		name      string
		transform Transform
		dialed    string
		want      string
	}{
		{"strip an outbound prefix", Transform{StripDigits: 1}, "95551212", "5551212"},
		{"strip a suffix", Transform{StripSuffixDigits: 2}, "1234", "12"},
		{"normalize to e164", Transform{Normalize: NormalizeE164}, "15558001234", "+15558001234"},
		{"e164 is idempotent", Transform{Normalize: NormalizeE164}, "+15558001234", "+15558001234"},
		{"digits only", Transform{Normalize: NormalizeDigits}, "+1 (555) 800-1234", "15558001234"},
		{"none leaves it alone", Transform{Normalize: NormalizeNone}, "95551212", "95551212"},
		{"strip then normalize", Transform{StripDigits: 1, Normalize: NormalizeE164}, "915558001234", "+15558001234"},
		{"stripping everything yields nothing", Transform{StripDigits: 9}, "911", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.transform.Apply(tc.dialed); got != tc.want {
				t.Errorf("Apply(%q) = %q, want %q", tc.dialed, got, tc.want)
			}
		})
	}
}

// A transform can only ever narrow or reshape what was dialed. It must never
// be able to lengthen the string, because that is the difference between
// normalising input and constructing a destination.
func TestTransformNeverLengthensBeyondAPlus(t *testing.T) {
	for _, tr := range []Transform{
		{StripDigits: 2}, {StripSuffixDigits: 3}, {Normalize: NormalizeDigits},
		{Normalize: NormalizeE164}, {StripDigits: 1, Normalize: NormalizeE164},
	} {
		got := tr.Apply("915558001234")
		// e164 adds exactly one '+' and nothing else.
		if len(got) > len("915558001234")+1 {
			t.Errorf("%+v lengthened the input: %q", tr, got)
		}
	}
}

func TestUnknownNormalizeIsRejected(t *testing.T) {
	err := Transform{Normalize: "e123"}.validate()
	if err == nil {
		t.Fatal("an unknown normalization must be rejected")
	}
	if !strings.Contains(err.Error(), "e164") {
		t.Errorf("the error should list the valid forms: %v", err)
	}
}

// The bare form stays valid, so a simple table needs no objects.
func TestEntryAcceptsBothForms(t *testing.T) {
	var bare Entry
	if err := bare.UnmarshalJSON([]byte(`"user/110"`)); err != nil {
		t.Fatalf("a bare destination must parse: %v", err)
	}
	if bare.Destination != "user/110" || bare.Transform != (Transform{}) {
		t.Errorf("bare entry = %+v", bare)
	}

	var obj Entry
	if err := obj.UnmarshalJSON([]byte(`{"flow":"outbound","strip_digits":1,"normalize":"e164"}`)); err != nil {
		t.Fatalf("the object form must parse: %v", err)
	}
	if obj.Destination != "flow/outbound" {
		t.Errorf("destination = %q, want flow/outbound", obj.Destination)
	}
	if obj.Transform.StripDigits != 1 || obj.Transform.Normalize != NormalizeE164 {
		t.Errorf("transform = %+v", obj.Transform)
	}
}

// An unknown field in an entry is a typo, and a typo that silently defaults is
// exactly what DisallowUnknownFields exists to prevent.
func TestEntryRejectsUnknownFields(t *testing.T) {
	var e Entry
	if err := e.UnmarshalJSON([]byte(`{"flow":"out","strip_digit":1}`)); err == nil {
		t.Fatal("a misspelled field must be rejected")
	}
}

// No interpolation anywhere: a destination is a literal, never a template.
func TestNoInterpolationInDestinations(t *testing.T) {
	m, err := CompileDigitMap(map[string]Entry{
		"9NXXNXXXXXX": {Destination: "flow/outbound", Transform: Transform{StripDigits: 1}},
	})
	if err != nil {
		t.Fatalf("CompileDigitMap: %v", err)
	}

	dest, digits, ok := m.LookupWithDigits("95555551212")
	if !ok {
		t.Fatal("the pattern should match")
	}
	// The destination is exactly what was written — the matched digits did not
	// become part of it.
	if dest != "flow/outbound" {
		t.Errorf("destination = %q, want the literal flow/outbound", dest)
	}
	if digits != "5555551212" {
		t.Errorf("transformed digits = %q, want the prefix stripped", digits)
	}
}
