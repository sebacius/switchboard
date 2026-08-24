package server

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/sebas/switchboard/internal/signaling/dialplan"
)

// A routing table is a document an operator wrote, and this edits it in place.
//
// The argument flowdoc.go makes for flows applies harder here. devtenant.routing
// .json carries three paragraphs of prose — including the one explaining WHY DID
// ownership is split across two files, which is the least obvious thing about
// the whole configuration model — and dialplan.RoutingTable has no field for any
// of it. The file decodes with a plain json.Unmarshal, no DisallowUnknownFields,
// so those keys are silently dropped on read. A structured editor that decoded
// the table and marshalled it back would delete every one of them, and nobody
// would notice until somebody needed the explanation.
//
// So nothing here decodes a value it is not rewriting. Each mutator locates the
// bytes of the one key it changes and replaces that range; everything else in
// the file is copied, not re-encoded. Editing groups cannot touch dids or the
// comment above it, even in principle.
//
// One further rule, learned from splicing: every mutator is
// parse -> single edit -> applyEdits -> return the whole new document. Offsets
// are never carried across two edits. Re-parsing costs microseconds and removes
// the entire class of bug where the second edit of a batch lands at a position
// the first one moved.

// The five shapes a routing table holds. Named because a caller addressing a
// section by string literal is a caller that can typo it.
const (
	sectionExtensions = "extensions"
	sectionDIDs       = "dids"
	sectionSymbolic   = "symbolic_targets"
	sectionGroups     = "groups"
)

// keyOperator and keyRetrievalPrefix are the two scalar keys.
const (
	keyOperator        = "operator"
	keyRetrievalPrefix = "retrieval_prefix"
)

// routingDoc is a parsed routing table that remembers where each key's bytes are.
type routingDoc struct {
	// content is the file exactly as read. Splicing works against it, so it is
	// the definition of "everything else is unchanged".
	content string
	// top is the document's own keys, in the order it lists them.
	top objectKeys
	// indent is the document's own indentation unit, so an edited section comes
	// back looking like the file it lives in rather than like our preference.
	indent string
}

// parseRoutingDoc reads a routing table and locates its top-level keys.
func parseRoutingDoc(content string) (*routingDoc, error) {
	raw := []byte(content)
	top, err := objectSpans(raw, span{0, len(raw)})
	if err != nil {
		return nil, fmt.Errorf("this file is not a JSON object: %w", err)
	}
	return &routingDoc{content: content, top: top, indent: detectIndent(content)}, nil
}

// Keys returns the document's top-level keys in document order, comments and all.
func (d *routingDoc) Keys() []string { return append([]string(nil), d.top.keys...) }

// --- Readers ---
//
// Everything is returned in DOCUMENT order rather than sorted. A table that
// re-sorts itself between renders moves rows out from under whoever is reading
// it, and the author's order is usually meaningful — they grouped the sales
// extensions together on purpose.

// entryRow is one row of extensions or dids.
type entryRow struct {
	// Key is the pattern or DID as written.
	Key string
	// Destination is where it leads.
	Destination string
	// Transform is the digit reshaping, zero when there is none.
	Transform dialplan.Transform
	// Bare records whether the value was written as a plain string, so an
	// unedited row can be left exactly as it was.
	Bare bool
	// Raw is the value's original bytes.
	Raw string
}

// groupRow is one ring group.
type groupRow struct {
	Name            string
	Strategy        string
	Members         []string
	MemberTimeoutMs int
	Raw             string
}

// kvRow is one symbolic target: a name and where it points.
type kvRow struct {
	Name   string
	Target string
}

// Scalar reads one of the two top-level string keys.
func (d *routingDoc) Scalar(key string) (string, bool) {
	sp, ok := d.top.spans[key]
	if !ok {
		return "", false
	}
	var out string
	if err := json.Unmarshal([]byte(d.content[sp.start:sp.end]), &out); err != nil {
		return "", false
	}
	return out, true
}

// Entries reads extensions or dids.
func (d *routingDoc) Entries(section string) ([]entryRow, error) {
	obj, ok, err := d.sectionKeys(section)
	if err != nil || !ok {
		return nil, err
	}

	rows := make([]entryRow, 0, len(obj.keys))
	for _, key := range obj.keys {
		sp := obj.spans[key]
		raw := d.content[sp.start:sp.end]

		var entry dialplan.Entry
		if err := json.Unmarshal([]byte(raw), &entry); err != nil {
			// A row the loader would reject is still shown, so an operator can
			// see and fix it rather than finding it missing from the table.
			rows = append(rows, entryRow{Key: key, Raw: raw})
			continue
		}
		rows = append(rows, entryRow{
			Key:         key,
			Destination: entry.Destination,
			Transform:   entry.Transform,
			Bare:        strings.HasPrefix(strings.TrimSpace(raw), `"`),
			Raw:         raw,
		})
	}
	return rows, nil
}

// Groups reads the ring groups.
func (d *routingDoc) Groups() ([]groupRow, error) {
	obj, ok, err := d.sectionKeys(sectionGroups)
	if err != nil || !ok {
		return nil, err
	}

	rows := make([]groupRow, 0, len(obj.keys))
	for _, name := range obj.keys {
		sp := obj.spans[name]
		raw := d.content[sp.start:sp.end]

		var g dialplan.RingGroup
		if err := json.Unmarshal([]byte(raw), &g); err != nil {
			rows = append(rows, groupRow{Name: name, Raw: raw})
			continue
		}
		rows = append(rows, groupRow{
			Name:            name,
			Strategy:        string(g.Strategy),
			Members:         g.Members,
			MemberTimeoutMs: g.MemberTimeoutMs,
			Raw:             raw,
		})
	}
	return rows, nil
}

// Symbolic reads the symbolic targets.
func (d *routingDoc) Symbolic() ([]kvRow, error) {
	obj, ok, err := d.sectionKeys(sectionSymbolic)
	if err != nil || !ok {
		return nil, err
	}

	rows := make([]kvRow, 0, len(obj.keys))
	for _, name := range obj.keys {
		sp := obj.spans[name]
		var target string
		_ = json.Unmarshal([]byte(d.content[sp.start:sp.end]), &target)
		rows = append(rows, kvRow{Name: name, Target: target})
	}
	return rows, nil
}

// SectionRaw returns a section's bytes exactly as written.
//
// This and SpliceSection are the "Edit as JSON" escape hatch, for every section,
// implemented as the same splice the structured editors use. There is
// deliberately no second write path: a raw editor implemented separately is a
// second thing that can be wrong about how a file gets written.
func (d *routingDoc) SectionRaw(section string) (string, bool) {
	sp, ok := d.top.spans[section]
	if !ok {
		return "", false
	}
	return d.content[sp.start:sp.end], true
}

// sectionKeys parses one top-level section's keys, reporting absence as absence
// rather than as an error: a tenant with no ring groups is ordinary.
func (d *routingDoc) sectionKeys(section string) (objectKeys, bool, error) {
	sp, ok := d.top.spans[section]
	if !ok {
		return objectKeys{}, false, nil
	}
	obj, err := objectSpans([]byte(d.content), sp)
	if err != nil {
		return objectKeys{}, false, fmt.Errorf("%q is not an object: %w", section, err)
	}
	return obj, true, nil
}

// --- Writers ---

// SetScalar sets operator or retrieval_prefix, returning the whole new document.
//
// An empty value DELETES the key rather than writing "". The loader treats an
// absent operator and an empty one identically, and a file that says
// `"operator": ""` is a lie about intent — it reads as a decision where there
// was none.
//
// Stated cost: a `_comment` above a deleted key survives and now describes
// nothing. That is accepted, because the alternative is guessing which comment
// belongs to which key, and guessing wrong deletes prose.
func (d *routingDoc) SetScalar(key, value string) (string, error) {
	if key != keyOperator && key != keyRetrievalPrefix {
		return "", fmt.Errorf("%q is not a scalar routing key", key)
	}

	if value == "" {
		if _, exists := d.top.spans[key]; !exists {
			return d.content, nil
		}
		e, err := removeKey([]byte(d.content), span{0, len(d.content)}, d.top, key)
		if err != nil {
			return "", err
		}
		return applyEdits([]byte(d.content), []edit{e}), nil
	}

	if sp, exists := d.top.spans[key]; exists {
		return applyEdits([]byte(d.content), []edit{{start: sp.start, end: sp.end, text: quote(value)}}), nil
	}
	e, err := insertKey([]byte(d.content), span{0, len(d.content)}, d.top, key, quote(value), d.indent)
	if err != nil {
		return "", err
	}
	return applyEdits([]byte(d.content), []edit{e}), nil
}

// SetEntry writes one row of extensions or dids.
func (d *routingDoc) SetEntry(section, key string, row entryRow) (string, error) {
	if section != sectionExtensions && section != sectionDIDs {
		return "", fmt.Errorf("%q does not hold entries", section)
	}
	text, err := renderEntryValue(row, d.indentFor(section))
	if err != nil {
		return "", err
	}
	return d.setKey(section, key, text)
}

// SetSymbolic writes one symbolic target.
func (d *routingDoc) SetSymbolic(name, target string) (string, error) {
	if target == "" {
		return "", fmt.Errorf("symbolic target %q names no destination", name)
	}
	return d.setKey(sectionSymbolic, name, quote(target))
}

// SetGroup writes one ring group.
func (d *routingDoc) SetGroup(name string, row groupRow) (string, error) {
	text, err := renderGroupValue(row, d.indentFor(sectionGroups), d.indent)
	if err != nil {
		return "", err
	}
	return d.setKey(sectionGroups, name, text)
}

// RenameKey renames one key of a section, leaving its value's bytes alone.
//
// The value is untouched and so is the key's POSITION. That is what stops an
// operator correcting a pattern from watching the table re-sort itself under
// them, which a re-emit would do.
func (d *routingDoc) RenameKey(section, from, to string) (string, error) {
	obj, ok, err := d.sectionKeys(section)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("this file has no %q", section)
	}
	e, err := renameKey([]byte(d.content), obj, from, to)
	if err != nil {
		return "", err
	}
	return applyEdits([]byte(d.content), []edit{e}), nil
}

// DeleteKey removes one key from a section.
func (d *routingDoc) DeleteKey(section, key string) (string, error) {
	sp, ok := d.top.spans[section]
	if !ok {
		return "", fmt.Errorf("this file has no %q", section)
	}
	obj, err := objectSpans([]byte(d.content), sp)
	if err != nil {
		return "", fmt.Errorf("%q is not an object: %w", section, err)
	}
	e, err := removeKey([]byte(d.content), sp, obj, key)
	if err != nil {
		return "", err
	}
	return applyEdits([]byte(d.content), []edit{e}), nil
}

// SpliceSection replaces a section's bytes with operator-supplied text.
func (d *routingDoc) SpliceSection(section, text string) (string, error) {
	sp, ok := d.top.spans[section]
	if !ok {
		return d.ensureSection(section, strings.TrimSpace(text))
	}
	return applyEdits([]byte(d.content), []edit{{start: sp.start, end: sp.end, text: strings.TrimSpace(text)}}), nil
}

// setKey sets one key of a map-valued section, adding the section if the file
// does not have it yet.
func (d *routingDoc) setKey(section, key, valueText string) (string, error) {
	sp, ok := d.top.spans[section]
	if !ok {
		// The section has to exist before a key can go in it. Two edits would
		// mean two offset computations against a document one of them moved, so
		// this returns the document with the empty section and the caller
		// re-parses — which is the rule for every mutator here.
		withSection, err := d.ensureSection(section, "{}")
		if err != nil {
			return "", err
		}
		next, err := parseRoutingDoc(withSection)
		if err != nil {
			return "", err
		}
		return next.setKey(section, key, valueText)
	}

	obj, err := objectSpans([]byte(d.content), sp)
	if err != nil {
		return "", fmt.Errorf("%q is not an object: %w", section, err)
	}

	if existing, present := obj.spans[key]; present {
		return applyEdits([]byte(d.content), []edit{{start: existing.start, end: existing.end, text: valueText}}), nil
	}
	e, err := insertKey([]byte(d.content), sp, obj, key, valueText, d.indent)
	if err != nil {
		return "", err
	}
	return applyEdits([]byte(d.content), []edit{e}), nil
}

// ensureSection adds a missing top-level section, appended at the document's
// tail.
//
// Appended, never inserted before an existing key: in these files a comment key
// PRECEDES the key it describes, so inserting before one can land between
// `_comment_dids` and `dids` and silently reattach that paragraph to something
// it does not explain.
func (d *routingDoc) ensureSection(section, valueText string) (string, error) {
	if _, exists := d.top.spans[section]; exists {
		return d.content, nil
	}
	e, err := insertKey([]byte(d.content), span{0, len(d.content)}, d.top, section, valueText, d.indent)
	if err != nil {
		return "", err
	}
	return applyEdits([]byte(d.content), []edit{e}), nil
}

// indentFor reports the indentation a value inside a section sits at.
func (d *routingDoc) indentFor(section string) string {
	if obj, ok, err := d.sectionKeys(section); err == nil && ok && len(obj.keys) > 0 {
		if got := obj.indents[obj.keys[0]]; got != "" {
			return got
		}
	}
	return d.indent + d.indent
}

// renderEntryValue writes an entry the way the loader itself writes one.
//
// Bare string when there is no transform, object form when there is. This
// mirrors dialplan.Entry.MarshalJSON, which exists for exactly this reason: a
// round trip must not rewrite every simple line into an object. It is a second
// implementation of that rule, so a test pins the two together.
//
// Note the one thing it does NOT preserve: an entry written as
// {"flow": "main-ivr"} comes back as {"destination": "flow/main-ivr"}. That is
// dialplan.Entry's own normalization, not this function's choice, and it happens
// only to a row an operator actually edited — an untouched row keeps its bytes.
func renderEntryValue(row entryRow, pad string) (string, error) {
	if row.Destination == "" {
		return "", fmt.Errorf("entry %q names no destination", row.Key)
	}
	if row.Transform == (dialplan.Transform{}) {
		return quote(row.Destination), nil
	}

	raw, err := json.Marshal(dialplan.Entry{Destination: row.Destination, Transform: row.Transform})
	if err != nil {
		return "", err
	}
	return renderValue(raw, pad, "  ")
}

// renderGroupValue writes a ring group at the document's own indentation.
func renderGroupValue(row groupRow, pad, unit string) (string, error) {
	if row.Strategy == "" {
		return "", fmt.Errorf("group %q names no strategy", row.Name)
	}
	if len(row.Members) == 0 {
		return "", fmt.Errorf("group %q has no members, so it can never answer", row.Name)
	}

	raw, err := json.Marshal(dialplan.RingGroup{
		Strategy:        dialplan.RingStrategy(row.Strategy),
		Members:         row.Members,
		MemberTimeoutMs: row.MemberTimeoutMs,
	})
	if err != nil {
		return "", err
	}
	return renderValue(raw, pad, unit)
}

// EntryKeys returns a section's keys, for the callers that only need the set —
// ranking patterns, or checking a reference.
func (d *routingDoc) EntryKeys(section string) []string {
	obj, ok, err := d.sectionKeys(section)
	if err != nil || !ok {
		return nil
	}
	return append([]string(nil), obj.keys...)
}

// ReferencesToFlow reports every place the routing table names a flow.
//
// This is the check that makes deleting a flow safe to offer: the routing table
// and the flows file load as ONE unit, so removing a flow an extension points at
// breaks the tenant. Paths are returned in the form an operator can find —
// `extensions."700"` — rather than as a count.
//
// Advisory only. The server refuses such a write anyway; this exists so the
// refusal is not the first the operator hears of it.
func (d *routingDoc) ReferencesToFlow(flow string) []string {
	target := "flow/" + flow
	var found []string

	for _, section := range []string{sectionExtensions, sectionDIDs} {
		rows, err := d.Entries(section)
		if err != nil {
			continue
		}
		for _, row := range rows {
			if row.Destination == target {
				found = append(found, fmt.Sprintf("%s.%q", section, row.Key))
			}
		}
	}
	if rows, err := d.Symbolic(); err == nil {
		for _, row := range rows {
			if row.Target == target {
				found = append(found, fmt.Sprintf("%s.%s", sectionSymbolic, row.Name))
			}
		}
	}
	if op, ok := d.Scalar(keyOperator); ok && op == target {
		found = append(found, keyOperator)
	}

	sort.Strings(found)
	return found
}
