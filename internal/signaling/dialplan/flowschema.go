package dialplan

import (
	"reflect"
	"strings"
)

// A flow editor has to render a palette of node types and a property form for
// whichever node is selected. Both are descriptions of what this package
// already defines, and the tempting shortcut — writing them out again in the
// editor — makes the contract exist in two places that drift apart silently.
// Add a field to IVREntry and a hand-written form still shows the old one; the
// first person to find out is an operator who cannot set a terminator.
//
// So the catalogue is derived here, from the same tables and the same structs
// the loader and the validator use. Adding a node type in Go is the whole
// change: the editor picks it up with no edit of its own.

// FieldKind is how a form should render one entry field.
type FieldKind string

const (
	// FieldString is a single line of text.
	FieldString FieldKind = "string"
	// FieldInt is a whole number, such as a timeout in milliseconds.
	FieldInt FieldKind = "int"
	// FieldBool is a checkbox.
	FieldBool FieldKind = "bool"
	// FieldStringList is an ordered list of strings, such as a playlist.
	FieldStringList FieldKind = "string_list"
	// FieldPrompt is a PromptSpec: what the caller hears, rendered from Fields
	// rather than as one control, because exactly one of text, file and files
	// may be set and a form that hides that produces a prompt the validator
	// rejects.
	FieldPrompt FieldKind = "prompt"
)

// FieldSchema describes one field of a node's entry.
type FieldSchema struct {
	// Key is the field's JSON name, which is what a saved entry must use.
	Key string `json:"key"`
	// Kind says how to render it.
	Kind FieldKind `json:"kind"`
	// Required is a FORM HINT, not a rule. It is read from the absence of
	// omitempty on the struct tag, which is how the entry structs already
	// distinguish `json:"text"` from `json:"voice,omitempty"`. The authority on
	// whether an entry is acceptable is checkEntry, which knows things a struct
	// tag cannot — that a tts node needs text, that a prompt needs exactly one
	// source. A hint that is wrong costs a stray asterisk; it can never save a
	// bad entry or refuse a good one.
	Required bool `json:"required"`
	// Fields are the nested fields of a prompt, empty for every other kind.
	Fields []FieldSchema `json:"fields,omitempty"`
}

// NodeTypeSchema describes one node type well enough for a UI to render a
// palette entry, a node with its ports, and a property form.
type NodeTypeSchema struct {
	// Type is the node type name as written in configuration.
	Type string `json:"type"`
	// Exits are the exits a node of this type MUST wire. They are fixed: an
	// editor may not add to them or remove from them.
	Exits []string `json:"exits"`
	// TerminalExits END the flow — the call is connected and the cursor is
	// released. They are reported so a UI can SAY what happens, and must never
	// be rendered as something to connect: declaring one is a load error,
	// because the graph cannot express what comes after a bridge.
	TerminalExits []string `json:"terminal_exits,omitempty"`
	// AcceptsDigitExits reports whether this type also takes digit exits, which
	// are data rather than a fixed name. An ivr menu's selections are the
	// reason: they are the flow's, not the type's, so an editor adds and
	// removes them freely while Exits stays fixed.
	AcceptsDigitExits bool `json:"accepts_digit_exits"`
	// Fields describe the type-specific entry.
	Fields []FieldSchema `json:"fields,omitempty"`
}

// NodeSchema returns the whole node catalogue, sorted by type name so a palette
// is stable between renders.
func NodeSchema() []NodeTypeSchema {
	types := NodeTypes()
	out := make([]NodeTypeSchema, 0, len(types))
	for _, name := range types {
		t := NodeType(name)
		out = append(out, NodeTypeSchema{
			Type:          name,
			Exits:         DeclaredExits(t),
			TerminalExits: append([]string(nil), terminalExits[t]...),
			// An ivr node is the only one whose exits carry a caller's
			// selection. Asking validExit rather than naming the type keeps
			// this true if that ever changes.
			AcceptsDigitExits: validExit(&Node{Type: t}, "1"),
			Fields:            entryFields(t),
		})
	}
	return out
}

// SchemaFor returns one type's schema.
func SchemaFor(t NodeType) (NodeTypeSchema, bool) {
	if !KnownNodeType(t) {
		return NodeTypeSchema{}, false
	}
	return NodeTypeSchema{
		Type:              string(t),
		Exits:             DeclaredExits(t),
		TerminalExits:     append([]string(nil), terminalExits[t]...),
		AcceptsDigitExits: validExit(&Node{Type: t}, "1"),
		Fields:            entryFields(t),
	}, true
}

// entryFields describes a node type's entry by reflecting over the very struct
// the decoder fills, so the form and the decoder can never disagree about what
// a field is called.
func entryFields(t NodeType) []FieldSchema {
	target, err := entryTarget(t)
	if err != nil {
		return nil
	}
	return structFields(reflect.TypeOf(target).Elem())
}

// structFields walks one struct's exported fields in declaration order, which
// is the order a form should show them: the entry structs are written with the
// important field first.
func structFields(st reflect.Type) []FieldSchema {
	if st.Kind() != reflect.Struct {
		return nil
	}
	out := make([]FieldSchema, 0, st.NumField())
	for i := 0; i < st.NumField(); i++ {
		f := st.Field(i)
		if !f.IsExported() {
			continue
		}
		key, opts, ok := jsonName(f)
		if !ok {
			continue
		}
		field := FieldSchema{
			Key:      key,
			Kind:     kindOf(f.Type),
			Required: !opts["omitempty"],
		}
		if field.Kind == FieldPrompt {
			field.Fields = structFields(f.Type)
		}
		out = append(out, field)
	}
	return out
}

// jsonName reads a field's JSON key and tag options, reporting false for a
// field the encoder skips.
func jsonName(f reflect.StructField) (string, map[string]bool, bool) {
	tag, ok := f.Tag.Lookup("json")
	if !ok {
		// No tag means encoding/json uses the Go name, which the entry structs
		// never rely on — but describing it is still more honest than dropping
		// a field a form would then be unable to set.
		return f.Name, map[string]bool{}, true
	}
	parts := strings.Split(tag, ",")
	name := parts[0]
	if name == "-" {
		// Node.ID is tagged this way: it is filled in at load and is not part
		// of the entry an operator writes.
		return "", nil, false
	}
	if name == "" {
		name = f.Name
	}
	opts := make(map[string]bool, len(parts)-1)
	for _, o := range parts[1:] {
		opts[o] = true
	}
	return name, opts, true
}

// promptType is compared by identity rather than by field shape, so a future
// struct that happens to look like a prompt is not mistaken for one.
var promptType = reflect.TypeOf(PromptSpec{})

// kindOf maps a Go type to how a form should render it.
func kindOf(t reflect.Type) FieldKind {
	if t == promptType {
		return FieldPrompt
	}
	switch t.Kind() {
	case reflect.String:
		return FieldString
	case reflect.Bool:
		return FieldBool
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return FieldInt
	case reflect.Slice:
		if t.Elem().Kind() == reflect.String {
			return FieldStringList
		}
	}
	// An unknown kind is described as text rather than omitted: a field a form
	// cannot render is still a field the operator may need to see.
	return FieldString
}
