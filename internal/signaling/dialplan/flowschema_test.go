package dialplan

import (
	"reflect"
	"testing"
)

// schemaFor returns one type's entry from the catalog, failing if it is
// absent — every assertion below depends on the type being there at all.
func schemaFor(t *testing.T, name string) NodeTypeSchema {
	t.Helper()
	for _, s := range NodeSchema() {
		if s.Type == name {
			return s
		}
	}
	t.Fatalf("node type %q is missing from the schema", name)
	return NodeTypeSchema{}
}

// fieldFor returns one field descriptor, failing if the entry does not have it.
func fieldFor(t *testing.T, fields []FieldSchema, key string) FieldSchema {
	t.Helper()
	for _, f := range fields {
		if f.Key == key {
			return f
		}
	}
	t.Fatalf("field %q is missing; got %v", key, keysOf(fields))
	return FieldSchema{}
}

func keysOf(fields []FieldSchema) []string {
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		out = append(out, f.Key)
	}
	return out
}

// The catalog must cover the closed set exactly. A type present in one and
// not the other is a type an operator can write but not draw, or draw but not
// write.
func TestNodeSchemaCoversEveryTypeExactlyOnce(t *testing.T) {
	schema := NodeSchema()
	seen := map[string]int{}
	for _, s := range schema {
		seen[s.Type]++
	}

	for _, name := range NodeTypes() {
		if seen[name] != 1 {
			t.Errorf("type %q appears %d times in the schema, want exactly 1", name, seen[name])
		}
		delete(seen, name)
	}
	for name := range seen {
		t.Errorf("schema describes %q, which is not a known node type", name)
	}
}

// Sorted, because a palette that reorders itself between renders makes an
// operator hunt for the node they used a moment ago.
func TestNodeSchemaIsSorted(t *testing.T) {
	schema := NodeSchema()
	for i := 1; i < len(schema); i++ {
		if schema[i-1].Type >= schema[i].Type {
			t.Fatalf("schema is not sorted: %q precedes %q", schema[i-1].Type, schema[i].Type)
		}
	}
}

// The exits a UI draws are the exits the validator demands. Deriving them from
// DeclaredExits rather than restating them is the whole point of the file.
func TestNodeSchemaExitsMatchTheContract(t *testing.T) {
	for _, s := range NodeSchema() {
		want := DeclaredExits(NodeType(s.Type))
		if !reflect.DeepEqual(s.Exits, want) {
			t.Errorf("%s: exits = %v, want %v", s.Type, s.Exits, want)
		}
	}

	if got := schemaFor(t, "ivr").Exits; !reflect.DeepEqual(got, []string{"invalid", "retries_exceeded", "timeout"}) {
		t.Errorf("ivr exits = %v", got)
	}
	if got := schemaFor(t, "dial_user").Exits; !reflect.DeepEqual(got, []string{"busy", "no_answer", "rejected", "unavailable"}) {
		t.Errorf("dial_user exits = %v", got)
	}
	if got := schemaFor(t, "hangup").Exits; len(got) != 0 {
		t.Errorf("hangup declares exits %v; a hangup ends the call and routes nowhere", got)
	}
}

// A terminal exit must be reported so a UI can name the outcome, and must stay
// out of the connectable list: declaring one is a load error.
func TestNodeSchemaSeparatesTerminalExits(t *testing.T) {
	for _, s := range NodeSchema() {
		nt := NodeType(s.Type)
		for _, exit := range s.TerminalExits {
			if !IsTerminalExit(nt, exit) {
				t.Errorf("%s: %q is reported as terminal but IsTerminalExit says otherwise", s.Type, exit)
			}
			for _, declared := range s.Exits {
				if declared == exit {
					t.Errorf("%s: terminal exit %q is also offered as a connectable exit", s.Type, exit)
				}
			}
		}
		for _, declared := range s.Exits {
			if IsTerminalExit(nt, declared) {
				t.Errorf("%s: connectable exit %q is terminal", s.Type, declared)
			}
		}
	}

	for _, name := range []string{"dial_user", "dial_external"} {
		if got := schemaFor(t, name).TerminalExits; !reflect.DeepEqual(got, []string{"answered"}) {
			t.Errorf("%s terminal exits = %v, want [answered]", name, got)
		}
	}
	transfer := schemaFor(t, "transfer")
	if !reflect.DeepEqual(transfer.TerminalExits, []string{"accepted"}) {
		t.Errorf("transfer terminal exits = %v, want [accepted]", transfer.TerminalExits)
	}
	if !reflect.DeepEqual(transfer.Exits, []string{"failed"}) {
		t.Errorf("transfer exits = %v, want [failed]", transfer.Exits)
	}
}

// Digit exits are a menu's data. Only ivr has them, and the schema has to say
// so or an editor would offer to add a digit to a hangup.
func TestOnlyIVRAcceptsDigitExits(t *testing.T) {
	for _, s := range NodeSchema() {
		want := s.Type == "ivr"
		if s.AcceptsDigitExits != want {
			t.Errorf("%s: AcceptsDigitExits = %v, want %v", s.Type, s.AcceptsDigitExits, want)
		}
	}
}

// The field keys are the keys a saved entry must use. They come from the tags
// on the struct the decoder fills, so a rename cannot leave the form behind.
func TestEntryFieldsComeFromTheEntryStructs(t *testing.T) {
	if got := keysOf(schemaFor(t, "tts").Fields); !reflect.DeepEqual(got, []string{"text", "voice", "interruptible"}) {
		t.Errorf("tts fields = %v", got)
	}
	if got := keysOf(schemaFor(t, "dial_user").Fields); !reflect.DeepEqual(got, []string{"target", "timeout_ms"}) {
		t.Errorf("dial_user fields = %v", got)
	}
	if got := keysOf(schemaFor(t, "hangup").Fields); !reflect.DeepEqual(got, []string{"cause"}) {
		t.Errorf("hangup fields = %v", got)
	}

	// Every described key must actually decode, or a form would produce an
	// entry that DisallowUnknownFields rejects.
	for _, s := range NodeSchema() {
		target, err := entryTarget(NodeType(s.Type))
		if err != nil {
			t.Fatalf("%s: %v", s.Type, err)
		}
		st := reflect.TypeOf(target).Elem()
		for _, f := range s.Fields {
			if !hasJSONKey(st, f.Key) {
				t.Errorf("%s: schema names field %q, which %s does not decode", s.Type, f.Key, st.Name())
			}
		}
	}
}

// hasJSONKey reports whether a struct decodes the given JSON key.
func hasJSONKey(st reflect.Type, key string) bool {
	for i := 0; i < st.NumField(); i++ {
		if name, _, ok := jsonName(st.Field(i)); ok && name == key {
			return true
		}
	}
	return false
}

// A form needs to know a number from a checkbox from a line of text.
func TestFieldKindsDistinguishWhatAFormMustRender(t *testing.T) {
	ivr := schemaFor(t, "ivr").Fields
	if got := fieldFor(t, ivr, "timeout_ms").Kind; got != FieldInt {
		t.Errorf("ivr.timeout_ms kind = %q, want %q", got, FieldInt)
	}
	if got := fieldFor(t, ivr, "max_retries").Kind; got != FieldInt {
		t.Errorf("ivr.max_retries kind = %q, want %q", got, FieldInt)
	}
	if got := fieldFor(t, ivr, "interruptible").Kind; got != FieldBool {
		t.Errorf("ivr.interruptible kind = %q, want %q", got, FieldBool)
	}
	if got := fieldFor(t, schemaFor(t, "dial_user").Fields, "target").Kind; got != FieldString {
		t.Errorf("dial_user.target kind = %q, want %q", got, FieldString)
	}
	if got := fieldFor(t, schemaFor(t, "tts").Fields, "text").Kind; got != FieldString {
		t.Errorf("tts.text kind = %q, want %q", got, FieldString)
	}
}

// A prompt is one shared type across three node types, and its own fields are
// mutually exclusive. A form that renders it as a single box produces prompts
// the validator refuses, so the schema exposes the parts.
func TestPromptIsDescribedAsAPrompt(t *testing.T) {
	prompt := fieldFor(t, schemaFor(t, "ivr").Fields, "prompt")
	if prompt.Kind != FieldPrompt {
		t.Fatalf("ivr.prompt kind = %q, want %q", prompt.Kind, FieldPrompt)
	}
	if got := keysOf(prompt.Fields); !reflect.DeepEqual(got, []string{"text", "voice", "file", "files"}) {
		t.Errorf("prompt fields = %v, want [text voice file files]", got)
	}
	if got := fieldFor(t, prompt.Fields, "files").Kind; got != FieldStringList {
		t.Errorf("prompt.files kind = %q, want %q", got, FieldStringList)
	}
}

// Required is read from omitempty. It is a hint — checkEntry is the authority —
// but the hint has to track the tags it claims to read.
func TestRequiredFollowsTheOmitemptyTag(t *testing.T) {
	tts := schemaFor(t, "tts").Fields
	if !fieldFor(t, tts, "text").Required {
		t.Error(`tts.text is tagged json:"text" with no omitempty and should be hinted required`)
	}
	if fieldFor(t, tts, "voice").Required {
		t.Error(`tts.voice is tagged omitempty and should not be hinted required`)
	}
	if !fieldFor(t, schemaFor(t, "dial_user").Fields, "target").Required {
		t.Error("dial_user.target should be hinted required")
	}
	if fieldFor(t, schemaFor(t, "hangup").Fields, "cause").Required {
		t.Error("hangup.cause is tagged omitempty and should not be hinted required")
	}
}

// SchemaFor is the same catalog looked up one type at a time, and refuses a
// type the closed set does not contain.
func TestSchemaForOneType(t *testing.T) {
	got, ok := SchemaFor(NodeIVR)
	if !ok {
		t.Fatal("SchemaFor(ivr) reported the type as unknown")
	}
	if !reflect.DeepEqual(got, schemaFor(t, "ivr")) {
		t.Error("SchemaFor(ivr) disagrees with the catalog entry")
	}
	if _, ok := SchemaFor(NodeType("voicemail")); ok {
		t.Error("SchemaFor accepted a type that is not in the closed set")
	}
}
