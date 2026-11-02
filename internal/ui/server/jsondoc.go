package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Editing a JSON configuration document without rewriting it.
//
// These primitives were written for the flows editor and are now shared with
// the routing editor, so the reasoning is worth restating where both can see
// it: a configuration file is a document an operator wrote, not a dump of our
// data model. It carries comment keys, an order its author chose, and
// whitespace someone reviewed. Decoding it into structs and marshalling it back
// destroys all three — the comments silently, which is the worst of them,
// because nobody notices until the explanation of why a thing is shaped that
// way is gone.
//
// So nothing here decodes a value it is not rewriting. A document is walked to
// find where each key's value LIVES, and a write replaces byte ranges. A value
// that is never decoded is a value that cannot be reformatted, which is the
// whole guarantee: what an edit did not touch, it cannot damage.
//
// Byte spans rather than an ordered map[string]json.RawMessage: re-encoding even
// an ordered structure normalizes whitespace, and Go marshals plain maps in
// sorted key order. Either would rewrite a checked-in file the first time
// anyone opened an editor on it.

// span is a half-open byte range [start, end) of content.
type span struct{ start, end int }

// objectKeys is one JSON object's keys in order, with each value's byte span.
type objectKeys struct {
	keys  []string
	spans map[string]span
	// keyStarts is where each key token begins, and indents is the whitespace
	// in front of it. Together they let a new key be inserted in front of an
	// existing one, indented the way its neighbors are.
	keyStarts map[string]int
	indents   map[string]string
}

// objectSpans walks one JSON object and records where each value lives.
//
// Byte spans rather than decoded values: a value we never decode is a value we
// cannot mangle, and re-emitting a flow we did not edit would reformat it for
// no reason an operator asked for.
func objectSpans(raw []byte, within span) (objectKeys, error) {
	out := objectKeys{
		spans:     map[string]span{},
		keyStarts: map[string]int{},
		indents:   map[string]string{},
	}

	body := raw[within.start:within.end]
	dec := json.NewDecoder(bytes.NewReader(body))
	tok, err := dec.Token()
	if err != nil {
		return out, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return out, fmt.Errorf("expected an object")
	}

	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return out, err
		}
		key, ok := keyTok.(string)
		if !ok {
			return out, fmt.Errorf("expected an object key")
		}
		keyStart := int(dec.InputOffset()) - len(encodeKey(key))

		// The decoder reports where the key token ended; the value begins at
		// the next thing that is neither whitespace nor the colon.
		start := skipToValue(body, int(dec.InputOffset()))
		if start < 0 {
			return out, fmt.Errorf("key %q has no value", key)
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return out, fmt.Errorf("key %q: %w", key, err)
		}
		end := int(dec.InputOffset())

		if _, seen := out.spans[key]; !seen {
			out.keys = append(out.keys, key)
		}
		out.spans[key] = span{within.start + start, within.start + end}
		out.keyStarts[key] = within.start + keyStart
		out.indents[key] = indentBefore(body, keyStart)
	}
	if _, err := dec.Token(); err != nil {
		return out, err
	}
	return out, nil
}

// skipToValue finds the first byte of a value, given where its key ended.
func skipToValue(body []byte, from int) int {
	for i := from; i < len(body); i++ {
		switch body[i] {
		case ' ', '\t', '\r', '\n', ':':
			continue
		default:
			return i
		}
	}
	return -1
}

// encodeKey renders a key the way the document must have spelled it, so its
// start offset can be recovered from where its token ended. A key written with
// an escape sequence is longer than this and simply gets no indent hint, which
// costs nothing: it is only used to place an inserted neighbor.
func encodeKey(key string) string { return quote(key) }

// indentBefore returns the whitespace between the start of a key's line and the
// key itself.
func indentBefore(body []byte, keyStart int) string {
	if keyStart < 0 || keyStart > len(body) {
		return ""
	}
	i := keyStart
	for i > 0 && (body[i-1] == ' ' || body[i-1] == '\t') {
		i--
	}
	if i == 0 || body[i-1] != '\n' {
		return ""
	}
	return string(body[i:keyStart])
}

// edit is one byte range of a document replaced by new text. An insertion is an
// edit whose range is empty.
type edit struct {
	start, end int
	text       string
}

// applyEdits rewrites only the ranges named, leaving every other byte alone.
// That is the whole point: what the editor did not change, it cannot reformat.
func applyEdits(src []byte, edits []edit) string {
	sort.Slice(edits, func(i, j int) bool { return edits[i].start < edits[j].start })
	var buf bytes.Buffer
	prev := 0
	for _, e := range edits {
		if e.start < prev {
			continue
		}
		buf.Write(src[prev:e.start])
		buf.WriteString(e.text)
		prev = e.end
	}
	buf.Write(src[prev:])
	return buf.String()
}

// detectIndent reads the document's indentation unit from its first indented
// line, defaulting to two spaces. A file written with four spaces should not
// come back half-converted because the editor had an opinion.
func detectIndent(content string) string {
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimLeft(line, " \t")
		if trimmed == "" || len(trimmed) == len(line) {
			continue
		}
		return line[:len(line)-len(trimmed)]
	}
	return "  "
}

// renderValue re-indents a JSON value at a given depth. Arrays of scalars stay
// on one line: a playlist or a coordinate pair reads worse broken across four.
func renderValue(raw json.RawMessage, pad, unit string) (string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return "null", nil
	}

	switch trimmed[0] {
	case '{':
		obj, err := objectSpans(trimmed, span{0, len(trimmed)})
		if err != nil {
			return "", err
		}
		if len(obj.keys) == 0 {
			return "{}", nil
		}
		inner := pad + unit
		var parts []string
		for _, key := range obj.keys {
			sp := obj.spans[key]
			value, err := renderValue(trimmed[sp.start:sp.end], inner, unit)
			if err != nil {
				return "", err
			}
			parts = append(parts, inner+quote(key)+": "+value)
		}
		return "{\n" + strings.Join(parts, ",\n") + "\n" + pad + "}", nil

	case '[':
		var items []json.RawMessage
		if err := json.Unmarshal(trimmed, &items); err != nil {
			return "", err
		}
		if len(items) == 0 {
			return "[]", nil
		}
		scalars := true
		for _, item := range items {
			if c := bytes.TrimSpace(item)[0]; c == '{' || c == '[' {
				scalars = false
				break
			}
		}
		if scalars {
			var parts []string
			for _, item := range items {
				parts = append(parts, compact(item))
			}
			return "[" + strings.Join(parts, ", ") + "]", nil
		}
		inner := pad + unit
		var parts []string
		for _, item := range items {
			value, err := renderValue(item, inner, unit)
			if err != nil {
				return "", err
			}
			parts = append(parts, inner+value)
		}
		return "[\n" + strings.Join(parts, ",\n") + "\n" + pad + "]", nil

	default:
		return compact(trimmed), nil
	}
}

// compact strips insignificant whitespace from a scalar.
func compact(raw json.RawMessage) string {
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return string(bytes.TrimSpace(raw))
	}
	return buf.String()
}

// quote writes a JSON string, escaped the way encoding/json would.
func quote(s string) string {
	out, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(out)
}

// --- Structural edits ---
//
// Adding and removing a key are where JSON's separators bite: a comma belongs
// BETWEEN values, so whether one has to be swallowed depends on where in the
// object the key sits. Getting that wrong produces a file that will not parse,
// and there are four positions to get wrong. So it is written once here, tested
// once, and shared by every document that has keys to add or remove.

// objectBraces locates an object's own braces inside a span.
//
// The closing brace is found by scanning back rather than assumed to be the
// last byte: a span from objectSpans ends exactly after the brace, but a
// whole-document span is built as {0, len(raw)} and carries the file's trailing
// newline. Assuming the last byte cost one corrupted document in testing, which
// is exactly the class of bug this file exists to make impossible.
func objectBraces(raw []byte, within span) (open, close int, err error) {
	body := raw[within.start:within.end]
	o := bytes.IndexByte(body, '{')
	if o < 0 {
		return 0, 0, fmt.Errorf("object at %d has no opening brace", within.start)
	}
	c := bytes.LastIndexByte(body, '}')
	if c < o {
		return 0, 0, fmt.Errorf("object at %d has no closing brace", within.start)
	}
	return within.start + o, within.start + c, nil
}

// removeKey returns the edit that deletes a key and its value from an object.
//
// obj must be the parse of the object at `within`. The span deleted depends on
// the key's position:
//
//   - not first: from the end of the PREVIOUS value through the end of this
//     one, which swallows the comma between them and the whitespace around it.
//   - first with a successor: from the start of this key's own indentation to
//     the start of the next key's, which swallows the trailing comma instead.
//   - the only key: the object's whole interior, leaving `{}`.
//
// Deleting from the end backwards would be simpler and is wrong: it would strip
// the newline the object's closing brace sits on.
func removeKey(raw []byte, within span, obj objectKeys, key string) (edit, error) {
	target, ok := obj.spans[key]
	if !ok {
		return edit{}, fmt.Errorf("key %q is not in this object", key)
	}

	idx := -1
	for i, k := range obj.keys {
		if k == key {
			idx = i
			break
		}
	}
	if idx < 0 {
		return edit{}, fmt.Errorf("key %q has a span but no position", key)
	}

	keyStart := obj.keyStarts[key]
	indent := obj.indents[key]

	switch {
	case len(obj.keys) == 1:
		// The interior, so an object emptied of its only key reads as `{}`
		// rather than as a brace with an orphaned blank line inside it.
		open, close, err := objectBraces(raw, within)
		if err != nil {
			return edit{}, err
		}
		return edit{start: open + 1, end: close, text: ""}, nil

	case idx > 0:
		prev := obj.spans[obj.keys[idx-1]]
		return edit{start: prev.end, end: target.end, text: ""}, nil

	default:
		next := obj.keys[idx+1]
		return edit{
			start: keyStart - len(indent),
			end:   obj.keyStarts[next] - len(obj.indents[next]),
			text:  "",
		}, nil
	}
}

// insertKey returns the edit that adds a key to an object, APPENDED after the
// object's last key.
//
// Appending rather than inserting is not a simplification, it is the point.
// In these files a comment key PRECEDES the key it explains — `_comment_dids`
// sits above `dids` — so inserting before an existing key can land between a
// comment and its subject, silently reattaching prose to the wrong thing.
// Appending can only ever land after the last value and before the closing
// brace, where there is nothing to come between.
//
// valueText is emitted as given, so a caller that wants a nested object
// rendered at the right depth renders it first with renderValue.
func insertKey(raw []byte, within span, obj objectKeys, key, valueText, unit string) (edit, error) {
	if _, exists := obj.spans[key]; exists {
		return edit{}, fmt.Errorf("key %q is already in this object", key)
	}

	open, close, err := objectBraces(raw, within)
	if err != nil {
		return edit{}, err
	}

	// The indent a sibling uses, so an inserted key lines up with its
	// neighbours rather than with our preference.
	indent := unit
	if len(obj.keys) > 0 {
		if got := obj.indents[obj.keys[len(obj.keys)-1]]; got != "" {
			indent = got
		}
	}

	if len(obj.keys) == 0 {
		// An empty object: replace its interior wholesale. `{}` becomes a
		// two-line object, and the closing brace needs the outer indent, which
		// is this object's own indent less one unit.
		outer := strings.TrimSuffix(indent, unit)
		return edit{
			start: open + 1,
			end:   close,
			text:  "\n" + indent + quote(key) + ": " + valueText + "\n" + outer,
		}, nil
	}

	last := obj.spans[obj.keys[len(obj.keys)-1]]
	return edit{
		start: last.end,
		end:   last.end,
		text:  ",\n" + indent + quote(key) + ": " + valueText,
	}, nil
}

// renameKey returns the edit that rewrites a key token, leaving its value's
// bytes and its position in the document alone.
//
// The guard is the reason this is a function rather than two lines at each call
// site. A key's start offset is recovered by subtracting the length of its
// re-encoded form from where its token ended, which is only correct for a key
// written WITHOUT escape sequences. That holds for every key in every
// configuration file here — they are extension numbers, DIDs, flow names — but
// it is an assumption, and splicing on an unchecked assumption about byte
// offsets corrupts a file rather than failing. So it is checked.
func renameKey(raw []byte, obj objectKeys, from, to string) (edit, error) {
	if from == to {
		return edit{}, fmt.Errorf("the new name is the old one")
	}
	if _, exists := obj.spans[to]; exists {
		return edit{}, fmt.Errorf("key %q is already in this object", to)
	}
	start, ok := obj.keyStarts[from]
	if !ok {
		return edit{}, fmt.Errorf("key %q is not in this object", from)
	}

	token := encodeKey(from)
	if start < 0 || start+len(token) > len(raw) || string(raw[start:start+len(token)]) != token {
		return edit{}, fmt.Errorf(
			"cannot locate the key %q in the document; it may be written with an escape sequence", from)
	}
	return edit{start: start, end: start + len(token), text: encodeKey(to)}, nil
}
