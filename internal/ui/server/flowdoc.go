package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// A flows file is a document an operator wrote, not a dump of our data model.
// It carries comments (`_comment`, `_comment_nodes`), an order its author chose,
// and whitespace someone reviewed. An editor that decodes it into structs and
// marshals it back destroys all three — the comments silently, which is the
// worst of the three because nobody notices until the explanation of why a flow
// is shaped that way is gone.
//
// So this reads the document, hands ONE flow to the editor, and writes back by
// replacing that flow's bytes in place. Everything outside those bytes — the
// other flows, the document's comments, its formatting — is not re-encoded and
// therefore cannot be damaged. Editing main-ivr cannot touch after-hours even
// in principle.
//
// Unknown keys are safe to carry because the loader ignores them: every flows
// load site uses a plain json.Unmarshal into dialplan.FlowSet, and the one
// strict decoder in the tree (DisallowUnknownFields) applies to a node's entry
// alone. That is also what makes `_layout` possible.

// flowsDoc is a parsed flows file that remembers where each flow's bytes are.
type flowsDoc struct {
	// content is the file exactly as read. Splicing works against it, so it is
	// the definition of "everything else is unchanged".
	content string
	// names are the flow names in the order the document lists them.
	names []string
	// flows maps a flow name to the byte span of its value in content.
	flows map[string]span
	// indent is the document's own indentation unit, so an edited flow comes
	// back looking like the file it lives in rather than like our preference.
	indent string
}

// span is a half-open byte range [start, end) of content.
type span struct{ start, end int }

// parseFlowsDoc reads a flows file and locates each flow's bytes.
func parseFlowsDoc(content string) (*flowsDoc, error) {
	doc := &flowsDoc{
		content: content,
		flows:   map[string]span{},
		indent:  detectIndent(content),
	}

	raw := []byte(content)
	top, err := objectSpans(raw, span{0, len(raw)})
	if err != nil {
		return nil, fmt.Errorf("this file is not a JSON object: %w", err)
	}

	flowsSpan, ok := top.spans["flows"]
	if !ok {
		// A flows file with no flows key routes nothing. Say that rather than
		// rendering an empty canvas as though the tenant had an empty graph.
		return nil, fmt.Errorf("this file has no \"flows\" key")
	}

	byName, err := objectSpans(raw, flowsSpan)
	if err != nil {
		return nil, fmt.Errorf("\"flows\" is not an object: %w", err)
	}
	doc.names = byName.keys
	doc.flows = byName.spans
	return doc, nil
}

// FlowNames returns the flows in document order.
func (d *flowsDoc) FlowNames() []string { return append([]string(nil), d.names...) }

// Has reports whether the document defines a flow.
func (d *flowsDoc) Has(name string) bool {
	_, ok := d.flows[name]
	return ok
}

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

// --- The editor's view of one flow ---

// flowGraph is one flow as the canvas sees it: a list of nodes, each with its
// position and its exits in a stable order.
type flowGraph struct {
	Name      string      `json:"name"`
	Start     string      `json:"start"`
	TimeoutMs int         `json:"timeout_ms,omitempty"`
	Nodes     []graphNode `json:"nodes"`
}

// graphNode is one node plus where it sits on the canvas.
type graphNode struct {
	ID    string          `json:"id"`
	Type  string          `json:"type"`
	Entry json.RawMessage `json:"entry,omitempty"`
	// Exits are ordered rather than a map so the ports on a node do not
	// reshuffle between renders, and so a digit menu keeps the order its author
	// wrote.
	Exits []graphExit `json:"exits"`
	X     int         `json:"x"`
	Y     int         `json:"y"`
}

// graphExit is one exit and where it leads. An empty target is an exit the
// operator has not wired yet: the canvas flags it, and the server refuses it on
// save, which is the only verdict that counts.
type graphExit struct {
	Name   string `json:"name"`
	Target string `json:"target"`
}

// flowObject is the parsed shape of one flow, used only while reading.
type flowObject struct {
	Start     string                     `json:"start"`
	TimeoutMs int                        `json:"timeout_ms"`
	Layout    map[string]json.RawMessage `json:"_layout"`
	Nodes     map[string]nodeObject      `json:"nodes"`
}

// nodeObject is the parsed shape of one node.
type nodeObject struct {
	Type  string            `json:"type"`
	Entry json.RawMessage   `json:"entry"`
	Exits map[string]string `json:"exits"`
}

// Graph builds the editor's model of one flow, resolving every node's position.
func (d *flowsDoc) Graph(name string) (flowGraph, error) {
	sp, ok := d.flows[name]
	if !ok {
		return flowGraph{}, fmt.Errorf("this file has no flow named %q", name)
	}
	raw := []byte(d.content)[sp.start:sp.end]

	var parsed flowObject
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return flowGraph{}, fmt.Errorf("flow %q: %w", name, err)
	}

	// Node order comes from the document, so a flow reads on the canvas in the
	// order its author listed it when nothing else decides.
	order, err := nodeOrder(raw)
	if err != nil {
		return flowGraph{}, fmt.Errorf("flow %q: %w", name, err)
	}

	graph := flowGraph{Name: name, Start: parsed.Start, TimeoutMs: parsed.TimeoutMs}
	for _, id := range order {
		node := parsed.Nodes[id]
		graph.Nodes = append(graph.Nodes, graphNode{
			ID:    id,
			Type:  node.Type,
			Entry: node.Entry,
			Exits: exitsOf(node),
		})
	}

	applyLayout(&graph, parsed.Layout)
	return graph, nil
}

// nodeOrder returns the node IDs in the order the flow lists them.
func nodeOrder(flowRaw []byte) ([]string, error) {
	obj, err := objectSpans(flowRaw, span{0, len(flowRaw)})
	if err != nil {
		return nil, err
	}
	nodesSpan, ok := obj.spans["nodes"]
	if !ok {
		return nil, fmt.Errorf("flow has no \"nodes\"")
	}
	nodes, err := objectSpans(flowRaw, nodesSpan)
	if err != nil {
		return nil, fmt.Errorf("\"nodes\" is not an object: %w", err)
	}
	return nodes.keys, nil
}

// exitsOf returns a node's exits, sorted so a node's ports are stable. Digit
// exits sort ahead of named ones because that is the order a menu reads in.
func exitsOf(node nodeObject) []graphExit {
	out := make([]graphExit, 0, len(node.Exits))
	for name, target := range node.Exits {
		out = append(out, graphExit{Name: name, Target: target})
	}
	sort.Slice(out, func(i, j int) bool {
		di, dj := isDigits(out[i].Name), isDigits(out[j].Name)
		if di != dj {
			return di
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// isDigits reports whether an exit name is a DTMF selection rather than an
// outcome. It mirrors the dialplan's own rule; it decides display order only.
func isDigits(s string) bool {
	if s == "" {
		return false
	}
	return strings.IndexFunc(s, func(r rune) bool {
		return !strings.ContainsRune("0123456789*#", r)
	}) < 0
}

// applyLayout places every node: stored coordinates where they are usable,
// computed ones everywhere else.
//
// A stored entry that is malformed, or names a node that no longer exists, is
// treated as absent. A layout is a convenience, and a convenience must never be
// able to stop a flow being edited — let alone routed.
func applyLayout(graph *flowGraph, stored map[string]json.RawMessage) {
	placed := map[string]bool{}
	for i := range graph.Nodes {
		node := &graph.Nodes[i]
		raw, ok := stored[node.ID]
		if !ok {
			continue
		}
		var pair []int
		if err := json.Unmarshal(raw, &pair); err != nil || len(pair) != 2 {
			continue
		}
		node.X, node.Y = pair[0], pair[1]
		placed[node.ID] = true
	}

	missing := false
	for _, node := range graph.Nodes {
		if !placed[node.ID] {
			missing = true
			break
		}
	}
	if !missing {
		return
	}
	computeLayout(graph, placed)
}

// Canvas geometry. A column is wide enough for a node plus its exit labels; a
// row is tall enough for a dial node's four ports.
const (
	layoutOriginX = 60
	layoutOriginY = 40
	layoutColumnW = 300
	layoutRowH    = 190
)

// computeLayout places every node the stored layout did not, by BFS depth from
// the start node: depth is the column, order within the depth is the row.
//
// It terminates because the graph is acyclic, which the loader proved before
// this file was ever served — but the visited set makes that a property of this
// function rather than a bet on someone else's invariant.
func computeLayout(graph *flowGraph, placed map[string]bool) {
	byID := map[string]*graphNode{}
	for i := range graph.Nodes {
		byID[graph.Nodes[i].ID] = &graph.Nodes[i]
	}

	depth := map[string]int{}
	visited := map[string]bool{}
	queue := []string{}
	if _, ok := byID[graph.Start]; ok {
		queue = append(queue, graph.Start)
		depth[graph.Start] = 0
		visited[graph.Start] = true
	}

	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		node := byID[id]
		if node == nil {
			continue
		}
		for _, exit := range node.Exits {
			if exit.Target == "" || visited[exit.Target] {
				continue
			}
			if _, ok := byID[exit.Target]; !ok {
				continue
			}
			visited[exit.Target] = true
			depth[exit.Target] = depth[id] + 1
			queue = append(queue, exit.Target)
		}
	}

	// A node nothing reaches is a problem the validator reports, not a reason
	// to draw nothing. Park it in a column of its own past the graph.
	maxDepth := 0
	for _, d := range depth {
		if d > maxDepth {
			maxDepth = d
		}
	}
	for _, node := range graph.Nodes {
		if _, ok := depth[node.ID]; !ok {
			depth[node.ID] = maxDepth + 1
		}
	}

	rows := map[int]int{}
	for i := range graph.Nodes {
		node := &graph.Nodes[i]
		d := depth[node.ID]
		row := rows[d]
		rows[d]++
		if placed[node.ID] {
			continue
		}
		node.X = layoutOriginX + d*layoutColumnW
		node.Y = layoutOriginY + row*layoutRowH
	}
}

// --- Writing one flow back ---

// Splice writes the edited graph back and returns the whole file.
//
// It replaces byte ranges rather than re-encoding anything: the flow's start,
// its layout, and the individual nodes whose content actually changed. A node
// nobody touched keeps its exact bytes — its inline entry stays inline, the
// blank line above it stays where its author put it — because it is never
// re-rendered at all. Saving a flow after only dragging a node is therefore a
// one-key diff, which is what makes the editor safe to open out of curiosity.
func (d *flowsDoc) Splice(name string, graph flowGraph) (string, error) {
	sp, ok := d.flows[name]
	if !ok {
		return "", fmt.Errorf("flow %q is no longer in this file; it may have been "+
			"renamed or removed since the editor opened it", name)
	}

	original := []byte(d.content)[sp.start:sp.end]
	edits, err := d.flowEdits(original, graph)
	if err != nil {
		return "", err
	}
	return d.content[:sp.start] + applyEdits(original, edits) + d.content[sp.end:], nil
}

// flowEdits works out the smallest set of byte ranges that turn the flow on
// disk into the flow on the canvas.
func (d *flowsDoc) flowEdits(original []byte, graph flowGraph) ([]edit, error) {
	obj, err := objectSpans(original, span{0, len(original)})
	if err != nil {
		return nil, fmt.Errorf("flow %q: %w", graph.Name, err)
	}

	var edits []edit

	// The start node, if it moved.
	if sp, ok := obj.spans["start"]; ok {
		if want := quote(graph.Start); string(original[sp.start:sp.end]) != want {
			edits = append(edits, edit{sp.start, sp.end, want})
		}
	}

	// The layout. An anchor of "nodes" puts it next to what it describes rather
	// than at the end, where it would read as unrelated to anything.
	anchor := "nodes"
	if _, ok := obj.spans[anchor]; !ok && len(obj.keys) > 0 {
		anchor = obj.keys[len(obj.keys)-1]
	}
	pad := obj.indents[anchor]
	if pad == "" {
		pad = strings.Repeat(d.indent, 3)
	}
	layout := d.renderLayout(graph, pad)
	if sp, ok := obj.spans["_layout"]; ok {
		edits = append(edits, edit{sp.start, sp.end, layout})
	} else if at, ok := obj.keyStarts[anchor]; ok {
		edits = append(edits, edit{at, at, quote("_layout") + ": " + layout + ",\n" + pad})
	}

	nodeEdits, err := d.nodeEdits(original, obj, graph)
	if err != nil {
		return nil, err
	}
	return append(edits, nodeEdits...), nil
}

// nodeEdits rewrites the nodes that changed, and only those.
//
// Adding or removing a node changes the shape of the map rather than the
// content of one entry, so that case re-renders the map whole. It is a
// structural edit an operator made deliberately; the cases worth protecting
// from reformatting are the ones they did not think of as edits at all.
func (d *flowsDoc) nodeEdits(original []byte, flow objectKeys, graph flowGraph) ([]edit, error) {
	nodesSpan, ok := flow.spans["nodes"]
	if !ok {
		return nil, fmt.Errorf("flow %q has no \"nodes\"", graph.Name)
	}
	nodes, err := objectSpans(original, nodesSpan)
	if err != nil {
		return nil, fmt.Errorf("flow %q: \"nodes\" is not an object: %w", graph.Name, err)
	}

	if !sameNodeSet(nodes.keys, graph) {
		pad := flow.indents["nodes"]
		if pad == "" {
			pad = strings.Repeat(d.indent, 3)
		}
		rendered, err := d.renderNodes(original, graph, pad)
		if err != nil {
			return nil, err
		}
		return []edit{{nodesSpan.start, nodesSpan.end, rendered}}, nil
	}

	var edits []edit
	for _, node := range graph.Nodes {
		sp := nodes.spans[node.ID]
		same, err := unchanged(original[sp.start:sp.end], node)
		if err != nil {
			return nil, fmt.Errorf("node %q: %w", node.ID, err)
		}
		if same {
			continue
		}
		pad := nodes.indents[node.ID]
		if pad == "" {
			pad = strings.Repeat(d.indent, 4)
		}
		rendered, err := d.renderNode(node, pad)
		if err != nil {
			return nil, err
		}
		edits = append(edits, edit{sp.start, sp.end, rendered})
	}
	return edits, nil
}

// sameNodeSet reports whether the canvas holds exactly the nodes the file does.
func sameNodeSet(existing []string, graph flowGraph) bool {
	if len(existing) != len(graph.Nodes) {
		return false
	}
	have := map[string]bool{}
	for _, id := range existing {
		have[id] = true
	}
	for _, node := range graph.Nodes {
		if !have[node.ID] {
			return false
		}
	}
	return true
}

// unchanged compares a node on disk with the same node on the canvas by what
// they MEAN, so a difference in spacing or key order is not mistaken for an
// edit and does not cost the file a reformat.
func unchanged(raw []byte, node graphNode) (bool, error) {
	var before map[string]any
	if err := json.Unmarshal(raw, &before); err != nil {
		return false, err
	}
	after, err := nodeSemantics(node)
	if err != nil {
		return false, err
	}
	return reflect.DeepEqual(before, after), nil
}

// nodeSemantics renders a node the way encoding/json would see the file, for
// comparison only.
func nodeSemantics(node graphNode) (map[string]any, error) {
	out := map[string]any{"type": node.Type}

	if trimmed := bytes.TrimSpace(node.Entry); len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null")) {
		var entry any
		if err := json.Unmarshal(trimmed, &entry); err != nil {
			return nil, fmt.Errorf("entry: %w", err)
		}
		out["entry"] = entry
	}

	exits := map[string]any{}
	for _, exit := range node.Exits {
		if strings.TrimSpace(exit.Target) != "" {
			exits[exit.Name] = exit.Target
		}
	}
	if len(exits) > 0 {
		out["exits"] = exits
	}
	return out, nil
}

// renderLayout writes the node positions, pruning entries for nodes that no
// longer exist: coordinates for a deleted node are a puzzle for the next reader
// and mean nothing to anyone.
func (d *flowsDoc) renderLayout(graph flowGraph, pad string) string {
	if len(graph.Nodes) == 0 {
		return "{}"
	}
	inner := pad + d.indent

	var buf bytes.Buffer
	buf.WriteString("{\n")
	for i, node := range graph.Nodes {
		buf.WriteString(inner)
		buf.WriteString(quote(node.ID))
		buf.WriteString(fmt.Sprintf(": [%d, %d]", node.X, node.Y))
		if i < len(graph.Nodes)-1 {
			buf.WriteString(",")
		}
		buf.WriteString("\n")
	}
	buf.WriteString(pad)
	buf.WriteString("}")
	return buf.String()
}

// renderNodes writes the whole node map, keeping the document's node order and
// appending anything new at the end.
func (d *flowsDoc) renderNodes(original []byte, graph flowGraph, pad string) (string, error) {
	inner := pad + d.indent

	order := orderedNodeIDs(original, graph)
	byID := map[string]graphNode{}
	for _, node := range graph.Nodes {
		byID[node.ID] = node
	}

	var buf bytes.Buffer
	buf.WriteString("{\n")
	for i, id := range order {
		rendered, err := d.renderNode(byID[id], inner)
		if err != nil {
			return "", err
		}
		buf.WriteString(inner)
		buf.WriteString(quote(id))
		buf.WriteString(": ")
		buf.WriteString(rendered)
		if i < len(order)-1 {
			buf.WriteString(",")
		}
		buf.WriteString("\n")
	}
	buf.WriteString(pad)
	buf.WriteString("}")
	return buf.String(), nil
}

// orderedNodeIDs keeps the nodes the document already had in their original
// order, so adding one node does not reshuffle the file.
func orderedNodeIDs(original []byte, graph flowGraph) []string {
	present := map[string]bool{}
	for _, node := range graph.Nodes {
		present[node.ID] = true
	}

	var out []string
	seen := map[string]bool{}
	if existing, err := nodeOrder(original); err == nil {
		for _, id := range existing {
			if present[id] && !seen[id] {
				out = append(out, id)
				seen[id] = true
			}
		}
	}
	for _, node := range graph.Nodes {
		if !seen[node.ID] {
			out = append(out, node.ID)
			seen[node.ID] = true
		}
	}
	return out
}

// renderNode writes one node: its type, its entry, and the exits that lead
// somewhere.
func (d *flowsDoc) renderNode(node graphNode, pad string) (string, error) {
	inner := pad + d.indent

	var parts []string
	parts = append(parts, inner+quote("type")+": "+quote(node.Type))

	if trimmed := bytes.TrimSpace(node.Entry); len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null")) {
		entry, err := renderValue(node.Entry, inner, d.indent)
		if err != nil {
			return "", fmt.Errorf("node %q: entry: %w", node.ID, err)
		}
		parts = append(parts, inner+quote("entry")+": "+entry)
	}

	if exits := d.renderExits(node, inner); exits != "" {
		parts = append(parts, inner+quote("exits")+": "+exits)
	}

	return "{\n" + strings.Join(parts, ",\n") + "\n" + pad + "}", nil
}

// renderExits writes a node's wired exits.
//
// An exit with no target is OMITTED rather than written as "". Both are refused
// by the validator, but the message differs, and "dial_user node does not wire
// its busy exit; every outcome must name a node" tells an operator what to do
// where "exit names no target node" only tells them something is wrong.
func (d *flowsDoc) renderExits(node graphNode, pad string) string {
	inner := pad + d.indent

	var wired []graphExit
	for _, exit := range node.Exits {
		if strings.TrimSpace(exit.Target) != "" {
			wired = append(wired, exit)
		}
	}
	if len(wired) == 0 {
		return ""
	}

	var buf bytes.Buffer
	buf.WriteString("{\n")
	for i, exit := range wired {
		buf.WriteString(inner)
		buf.WriteString(quote(exit.Name))
		buf.WriteString(": ")
		buf.WriteString(quote(exit.Target))
		if i < len(wired)-1 {
			buf.WriteString(",")
		}
		buf.WriteString("\n")
	}
	buf.WriteString(pad)
	buf.WriteString("}")
	return buf.String()
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
