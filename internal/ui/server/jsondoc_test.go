package server

import (
	"encoding/json"
	"strings"
	"testing"
)

// These test the primitives directly rather than through a document editor,
// because their whole value is that the separator handling exists once. A bug
// here is a file that will not parse, and there are four positions to get
// wrong, so all four are named.

// doc parses a whole document as one object, the way both editors start.
func doc(t *testing.T, content string) ([]byte, span, objectKeys) {
	t.Helper()
	raw := []byte(content)
	within := span{0, len(raw)}
	obj, err := objectSpans(raw, within)
	if err != nil {
		t.Fatalf("objectSpans: %v", err)
	}
	return raw, within, obj
}

// stillParses is the floor for every structural edit: whatever it produced has
// to be JSON. An edit that leaves a stray comma is the failure mode.
func stillParses(t *testing.T, out string) map[string]any {
	t.Helper()
	var into map[string]any
	if err := json.Unmarshal([]byte(out), &into); err != nil {
		t.Fatalf("the edit produced invalid JSON: %v\n%s", err, out)
	}
	return into
}

const threeKeys = `{
  "first": "a",
  "middle": "b",
  "last": "c"
}
`

// --- removeKey ---

func TestRemovingEachPositionLeavesValidJSON(t *testing.T) {
	for _, key := range []string{"first", "middle", "last"} {
		t.Run(key, func(t *testing.T) {
			raw, within, obj := doc(t, threeKeys)
			e, err := removeKey(raw, within, obj, key)
			if err != nil {
				t.Fatalf("removeKey: %v", err)
			}
			out := applyEdits(raw, []edit{e})
			got := stillParses(t, out)

			if _, still := got[key]; still {
				t.Errorf("%q survived the delete:\n%s", key, out)
			}
			if len(got) != 2 {
				t.Errorf("keys remaining = %d, want 2:\n%s", len(got), out)
			}
			// The two survivors keep their values, so a delete cannot be
			// mistaken for a rewrite of its neighbours.
			for k, want := range map[string]string{"first": "a", "middle": "b", "last": "c"} {
				if k == key {
					continue
				}
				if got[k] != want {
					t.Errorf("%q = %v, want %q", k, got[k], want)
				}
			}
		})
	}
}

// The only key is the case that most easily leaves a brace with a blank line
// and a stray comma inside it.
func TestRemovingTheOnlyKeyEmptiesTheObject(t *testing.T) {
	raw, within, obj := doc(t, "{\n  \"solo\": \"a\"\n}\n")
	e, err := removeKey(raw, within, obj, "solo")
	if err != nil {
		t.Fatalf("removeKey: %v", err)
	}
	out := applyEdits(raw, []edit{e})
	if got := stillParses(t, out); len(got) != 0 {
		t.Errorf("the object is not empty: %v", got)
	}
	if !strings.Contains(out, "{}") {
		t.Errorf("an emptied object should read as {}, got:\n%s", out)
	}
}

// A delete must not disturb the prose around the keys it did not touch. This is
// the property the whole file exists for.
func TestRemovingAKeyLeavesTheCommentsAlone(t *testing.T) {
	const withProse = `{
  "_comment": "why this file is shaped this way",

  "_comment_kept": "explains kept",
  "kept": "a",

  "_comment_gone": "explains gone",
  "gone": "b"
}
`
	raw, within, obj := doc(t, withProse)
	e, err := removeKey(raw, within, obj, "gone")
	if err != nil {
		t.Fatalf("removeKey: %v", err)
	}
	out := applyEdits(raw, []edit{e})
	stillParses(t, out)

	for _, want := range []string{
		`"_comment": "why this file is shaped this way"`,
		`"_comment_kept": "explains kept"`,
		`"kept": "a"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the delete damaged %s:\n%s", want, out)
		}
	}
}

func TestRemovingAKeyThatIsNotThereIsRefused(t *testing.T) {
	raw, within, obj := doc(t, threeKeys)
	if _, err := removeKey(raw, within, obj, "absent"); err == nil {
		t.Error("removing a key that does not exist should be refused")
	}
}

// --- insertKey ---

func TestInsertingAppendsAfterTheLastKey(t *testing.T) {
	raw, within, obj := doc(t, threeKeys)
	e, err := insertKey(raw, within, obj, "added", `"d"`, "  ")
	if err != nil {
		t.Fatalf("insertKey: %v", err)
	}
	out := applyEdits(raw, []edit{e})
	got := stillParses(t, out)

	if got["added"] != "d" {
		t.Errorf("added = %v, want d", got["added"])
	}
	if strings.Index(out, `"added"`) < strings.Index(out, `"last"`) {
		t.Errorf("the new key was not appended after the last one:\n%s", out)
	}
}

// The reason insertKey appends rather than prepends. A comment key sits ABOVE
// the key it explains, so inserting before an existing key can reattach prose
// to something it does not describe.
func TestInsertingNeverSeparatesACommentFromItsKey(t *testing.T) {
	const withProse = `{
  "_comment_dids": "step TWO for an inbound call",
  "dids": {}
}
`
	raw, within, obj := doc(t, withProse)
	e, err := insertKey(raw, within, obj, "groups", `{}`, "  ")
	if err != nil {
		t.Fatalf("insertKey: %v", err)
	}
	out := applyEdits(raw, []edit{e})
	stillParses(t, out)

	comment := strings.Index(out, `"_comment_dids"`)
	dids := strings.Index(out, `"dids"`)
	groups := strings.Index(out, `"groups"`)
	if !(comment < dids && dids < groups) {
		t.Errorf("the insert came between a comment and its key:\n%s", out)
	}
}

// An empty object has no last key to append after, and `{}` has no interior to
// append into. It is the one shape that needs its own answer.
func TestInsertingIntoAnEmptyObject(t *testing.T) {
	raw := []byte("{\n  \"flows\": {}\n}\n")
	top, err := objectSpans(raw, span{0, len(raw)})
	if err != nil {
		t.Fatalf("objectSpans: %v", err)
	}
	inner := top.spans["flows"]
	obj, err := objectSpans(raw, inner)
	if err != nil {
		t.Fatalf("objectSpans(inner): %v", err)
	}

	e, err := insertKey(raw, inner, obj, "only", `"x"`, "  ")
	if err != nil {
		t.Fatalf("insertKey: %v", err)
	}
	out := applyEdits(raw, []edit{e})

	var into struct {
		Flows map[string]string `json:"flows"`
	}
	if err := json.Unmarshal([]byte(out), &into); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if into.Flows["only"] != "x" {
		t.Errorf("flows = %v, want only=x:\n%s", into.Flows, out)
	}
}

func TestInsertingADuplicateKeyIsRefused(t *testing.T) {
	raw, within, obj := doc(t, threeKeys)
	if _, err := insertKey(raw, within, obj, "middle", `"z"`, "  "); err == nil {
		t.Error("inserting a key that already exists should be refused")
	}
}

// A document written with four spaces must not come back half-converted.
func TestInsertingFollowsTheDocumentsIndent(t *testing.T) {
	const fourSpace = "{\n    \"first\": \"a\"\n}\n"
	raw, within, obj := doc(t, fourSpace)
	e, err := insertKey(raw, within, obj, "second", `"b"`, detectIndent(fourSpace))
	if err != nil {
		t.Fatalf("insertKey: %v", err)
	}
	out := applyEdits(raw, []edit{e})
	stillParses(t, out)
	if !strings.Contains(out, "\n    \"second\"") {
		t.Errorf("the inserted key does not match the document's indent:\n%s", out)
	}
}

// --- renameKey ---

func TestRenamingChangesOnlyTheKey(t *testing.T) {
	const withValue = `{
  "old": {
    "nested": [1, 2, 3],
    "kept":   "spacing on purpose"
  }
}
`
	raw, _, obj := doc(t, withValue)
	e, err := renameKey(raw, obj, "old", "new")
	if err != nil {
		t.Fatalf("renameKey: %v", err)
	}
	out := applyEdits(raw, []edit{e})
	stillParses(t, out)

	if strings.Contains(out, `"old"`) {
		t.Errorf("the old key survived:\n%s", out)
	}
	// The value's own formatting is untouched, which is the guarantee: a rename
	// is a rename, not a reformat.
	if !strings.Contains(out, `"kept":   "spacing on purpose"`) {
		t.Errorf("the value was reformatted:\n%s", out)
	}
	if !strings.Contains(out, `"nested": [1, 2, 3]`) {
		t.Errorf("the nested value was reformatted:\n%s", out)
	}
}

func TestRenamingKeepsThePosition(t *testing.T) {
	raw, _, obj := doc(t, threeKeys)
	e, err := renameKey(raw, obj, "middle", "renamed")
	if err != nil {
		t.Fatalf("renameKey: %v", err)
	}
	out := applyEdits(raw, []edit{e})
	stillParses(t, out)

	first := strings.Index(out, `"first"`)
	renamed := strings.Index(out, `"renamed"`)
	last := strings.Index(out, `"last"`)
	if !(first < renamed && renamed < last) {
		t.Errorf("the renamed key moved:\n%s", out)
	}
}

func TestRenamingIsRefusedWhenItWouldCollideOrIsANoop(t *testing.T) {
	raw, _, obj := doc(t, threeKeys)
	if _, err := renameKey(raw, obj, "first", "last"); err == nil {
		t.Error("renaming onto an existing key should be refused")
	}
	if _, err := renameKey(raw, obj, "first", "first"); err == nil {
		t.Error("renaming a key to itself should be refused")
	}
	if _, err := renameKey(raw, obj, "absent", "x"); err == nil {
		t.Error("renaming a key that does not exist should be refused")
	}
}

// The guard exists because a key's start offset is recovered arithmetically,
// which is only valid for a key written without escapes. An escaped key must
// fail rather than splice at an offset that is off by the escape's length.
func TestRenamingAnEscapedKeyIsRefusedRatherThanCorrupting(t *testing.T) {
	raw, _, obj := doc(t, "{\n  \"a\\u0062c\": \"v\"\n}\n")
	if _, err := renameKey(raw, obj, "abc", "xyz"); err == nil {
		t.Error("a key written with an escape sequence should be refused, not spliced blindly")
	} else if !strings.Contains(err.Error(), "escape sequence") {
		t.Errorf("the refusal should explain why: %v", err)
	}
}

// A whole-document span is built as {0, len(raw)} and so carries the file's
// trailing newline, unlike a span from objectSpans which ends exactly after the
// closing brace. Both structural edits scan for the brace rather than assuming
// the last byte; these pin that, because assuming it corrupts the document.
func TestStructuralEditsFindTheBraceRatherThanAssumingTheLastByte(t *testing.T) {
	t.Run("insert into an empty document", func(t *testing.T) {
		raw := []byte("{}\n")
		within := span{0, len(raw)}
		obj, err := objectSpans(raw, within)
		if err != nil {
			t.Fatalf("objectSpans: %v", err)
		}
		e, err := insertKey(raw, within, obj, "first", `"a"`, "  ")
		if err != nil {
			t.Fatalf("insertKey: %v", err)
		}
		out := applyEdits(raw, []edit{e})
		if got := stillParses(t, out); got["first"] != "a" {
			t.Errorf("first = %v, want a:\n%s", got["first"], out)
		}
	})

	t.Run("delete the only key of a document", func(t *testing.T) {
		raw, within, obj := doc(t, "{\n  \"solo\": 1\n}\n")
		e, err := removeKey(raw, within, obj, "solo")
		if err != nil {
			t.Fatalf("removeKey: %v", err)
		}
		if got := stillParses(t, applyEdits(raw, []edit{e})); len(got) != 0 {
			t.Errorf("keys remaining = %v, want none", got)
		}
	})
}
