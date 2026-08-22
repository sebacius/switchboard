package dialplan

import (
	"fmt"
	"sort"
	"strings"
)

// Validation runs at LOAD, not at 2am. Every check here answers a question that
// would otherwise be answered by a caller hearing silence: does this exit go
// anywhere, can this graph terminate, does this target exist, is this pattern
// ambiguous.
//
// The whole set of problems is reported rather than the first, because a flow
// with thirty nodes and four mistakes should take one pass to fix, not four.

// Severity distinguishes a problem that must stop a load from one worth saying
// out loud.
type Severity string

const (
	// SeverityError fails the load. The previous configuration stays in force.
	SeverityError Severity = "error"
	// SeverityWarning is reported but does not fail the load.
	SeverityWarning Severity = "warning"
)

// Problem is one validation finding, addressed to whoever has to fix it.
type Problem struct {
	// Tenant owning the configuration.
	Tenant string `json:"tenant"`
	// Path locates the problem, e.g. "flows.main-ivr.nodes.greeting.exits.timeout".
	Path string `json:"path"`
	// Message says what is wrong and, where possible, what to write instead.
	Message string `json:"message"`
	// Severity decides whether this fails the load.
	Severity Severity `json:"severity"`
}

func (p Problem) String() string {
	return fmt.Sprintf("%s: %s: %s", p.Tenant, p.Path, p.Message)
}

// Problems is an ordered set of findings.
type Problems []Problem

// HasErrors reports whether anything here should fail a load.
func (ps Problems) HasErrors() bool {
	for _, p := range ps {
		if p.Severity == SeverityError {
			return true
		}
	}
	return false
}

// Err folds the errors into one error suitable for a load path, listing every
// one rather than the first.
func (ps Problems) Err() error {
	var msgs []string
	for _, p := range ps {
		if p.Severity == SeverityError {
			msgs = append(msgs, fmt.Sprintf("%s: %s", p.Path, p.Message))
		}
	}
	if len(msgs) == 0 {
		return nil
	}
	if len(msgs) == 1 {
		return fmt.Errorf("%s", msgs[0])
	}
	return fmt.Errorf("%d problems: %s", len(msgs), strings.Join(msgs, "; "))
}

// TargetClassifier adjudicates a resolved destination without side effects. It
// is the seam the Class-of-Service check uses, kept as an interface so this
// package does not depend on the policy engine — and so validation can never
// accidentally consume a tenant's spend budget, which is the whole reason the
// policy layer separates classification from consumption.
type TargetClassifier interface {
	// Classify reports whether a resolved target may be dialed, and why not.
	Classify(resolved string) (allowed bool, reason string)
}

// ValidateFlows checks a tenant's flows against its routing table and returns
// the first error as a Go error, for load paths that want fail-closed behaviour.
func ValidateFlows(tenant string, table *RoutingTable, set *FlowSet) error {
	return CheckFlows(tenant, table, set).Err()
}

// CheckFlows returns every problem in a tenant's flows.
func CheckFlows(tenant string, table *RoutingTable, set *FlowSet) Problems {
	return CheckFlowsWithPolicy(tenant, table, set, nil)
}

// CheckFlowsWithPolicy additionally runs every dial destination through the
// tenant's Class of Service, so a flow that could never place its call is caught
// at load rather than at 2am.
func CheckFlowsWithPolicy(tenant string, table *RoutingTable, set *FlowSet, cos TargetClassifier) Problems {
	ps := checkFlowsStructure(tenant, table, set)
	if cos != nil {
		ps = append(ps, checkFlowPolicy(tenant, table, set, cos)...)
	}
	return ps
}

// checkFlowPolicy runs each flow's dial destinations through Class of Service.
func checkFlowPolicy(tenant string, table *RoutingTable, set *FlowSet, cos TargetClassifier) Problems {
	var ps Problems
	if set == nil {
		return ps
	}

	for _, flowName := range set.Names() {
		flow := set.Flows[flowName]
		if flow == nil {
			continue
		}
		for _, id := range sortedNodeIDs(flow) {
			node := flow.Nodes[id]
			if node == nil {
				continue
			}
			path := "flows." + flowName + ".nodes." + id + ".entry.target"

			for _, resolved := range resolvedTargetsOf(table, node) {
				if allowed, reason := cos.Classify(resolved); !allowed {
					ps = append(ps, Problem{tenant, path, fmt.Sprintf(
						"destination %q is denied by this tenant's class of service (%s), so this "+
							"node could never place its call", resolved, reason), SeverityError})
				}
			}
		}
	}
	return ps
}

// resolvedTargetsOf returns the concrete destinations a node would dial. A ring
// group yields each member separately, because a member is adjudicated on its
// own merits rather than inheriting the group's verdict.
func resolvedTargetsOf(table *RoutingTable, node *Node) []string {
	var raw string
	switch e := node.DecodedEntry().(type) {
	case *DialUserEntry:
		raw = e.Target
	case *DialExternalEntry:
		raw = e.Target
	case *TransferEntry:
		raw = e.Target
	default:
		return nil
	}

	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}

	if name, isGroup := IsGroupTarget(raw); isGroup {
		if table == nil {
			return nil
		}
		group, ok := table.Groups[name]
		if !ok {
			return nil
		}
		return append([]string(nil), group.Members...)
	}

	// A symbolic name resolves through the tenant's own table, exactly as it
	// would at call time.
	if table != nil {
		if resolved, ok := table.SymbolicTargets[raw]; ok && resolved != "" {
			return []string{resolved}
		}
	}
	return []string{raw}
}

// checkFlowsStructure returns every structural problem in a tenant's flows.
func checkFlowsStructure(tenant string, table *RoutingTable, set *FlowSet) Problems {
	var ps Problems
	if set == nil {
		return ps
	}

	for _, name := range set.Names() {
		flow := set.Flows[name]
		base := "flows." + name
		if flow == nil {
			ps = append(ps, Problem{tenant, base, "flow is null", SeverityError})
			continue
		}
		ps = append(ps, checkFlow(tenant, base, table, flow)...)
	}
	return ps
}

// checkFlow runs every structural check over one flow.
func checkFlow(tenant, base string, table *RoutingTable, flow *FlowDef) Problems {
	var ps Problems

	if len(flow.Nodes) == 0 {
		return append(ps, Problem{tenant, base + ".nodes", "flow has no nodes", SeverityError})
	}

	// 1. Schema: every node decodes into its type's entry, unknown fields and all.
	for _, id := range sortedNodeIDs(flow) {
		node := flow.Nodes[id]
		path := base + ".nodes." + id
		if node == nil {
			ps = append(ps, Problem{tenant, path, "node is null", SeverityError})
			continue
		}
		if !KnownNodeType(node.Type) {
			ps = append(ps, Problem{tenant, path + ".type", fmt.Sprintf(
				"unknown node type %q (want one of %s)", node.Type, strings.Join(NodeTypes(), ", ")),
				SeverityError})
			continue
		}
		if err := node.decodeEntry(); err != nil {
			ps = append(ps, Problem{tenant, path, err.Error(), SeverityError})
			continue
		}
		ps = append(ps, checkEntry(tenant, path, table, node)...)
		ps = append(ps, checkExits(tenant, path, flow, node)...)
	}

	// 2. Start node.
	if strings.TrimSpace(flow.Start) == "" {
		ps = append(ps, Problem{tenant, base + ".start", "flow declares no start node", SeverityError})
		return ps
	}
	if _, ok := flow.Nodes[flow.Start]; !ok {
		ps = append(ps, Problem{tenant, base + ".start", fmt.Sprintf(
			"start node %q does not exist", flow.Start), SeverityError})
		return ps
	}

	// 3. Reachability. An unreachable node is either a mistake or dead weight,
	// and both are worth saying before it rots.
	reachable := reachableFrom(flow, flow.Start)
	for _, id := range sortedNodeIDs(flow) {
		if !reachable[id] {
			ps = append(ps, Problem{tenant, base + ".nodes." + id, fmt.Sprintf(
				"node %q is unreachable from start node %q", id, flow.Start), SeverityError})
		}
	}

	// 4. Acyclicity — the property that makes the flow provably terminating.
	if cycle := findCycle(flow); len(cycle) > 0 {
		ps = append(ps, Problem{tenant, base, fmt.Sprintf(
			"flow contains a cycle: %s. Flows must be acyclic so every call is guaranteed to "+
				"terminate; repetition belongs inside a node, bounded by a counter such as ivr.max_retries",
			strings.Join(cycle, " -> ")), SeverityError})
	}

	return ps
}

// checkExits enforces the exit contract: declared exits exist for the type,
// every non-terminal exit is wired, terminal exits are not declared, and every
// target names a real node.
func checkExits(tenant, path string, flow *FlowDef, node *Node) Problems {
	var ps Problems

	declared := map[string]bool{}
	for exit, target := range node.Exits {
		declared[exit] = true

		if IsTerminalExit(node.Type, exit) {
			ps = append(ps, Problem{tenant, path + ".exits." + exit, fmt.Sprintf(
				"%q is terminal for a %s node and must not be declared: the flow ends when the call "+
					"is connected, so there is nothing after it to route to", exit, node.Type),
				SeverityError})
			continue
		}
		if !validExit(node, exit) {
			ps = append(ps, Problem{tenant, path + ".exits." + exit, fmt.Sprintf(
				"%q is not an exit of a %s node (want one of %s)",
				exit, node.Type, strings.Join(expectedExits(node), ", ")), SeverityError})
			continue
		}
		if strings.TrimSpace(target) == "" {
			ps = append(ps, Problem{tenant, path + ".exits." + exit, "exit names no target node", SeverityError})
			continue
		}
		if _, ok := flow.Nodes[target]; !ok {
			ps = append(ps, Problem{tenant, path + ".exits." + exit, fmt.Sprintf(
				"exit leads to node %q, which does not exist", target), SeverityError})
		}
	}

	// Every non-terminal exit must be wired. No defaults: what happens when the
	// line is busy is always written down.
	for _, exit := range DeclaredExits(node.Type) {
		if !declared[exit] {
			ps = append(ps, Problem{tenant, path + ".exits", fmt.Sprintf(
				"%s node does not wire its %q exit; every outcome must name a node", node.Type, exit),
				SeverityError})
		}
	}

	// An ivr node additionally needs somewhere to send at least one digit, or it
	// is a menu that can only ever time out.
	if node.Type == NodeIVR && !hasDigitExit(node) {
		ps = append(ps, Problem{tenant, path + ".exits",
			"ivr node declares no digit exits, so no selection can ever be accepted", SeverityError})
	}

	return ps
}

// validExit reports whether an exit name is legal for a node. An ivr node also
// accepts digit exits, which are data rather than a fixed name.
func validExit(node *Node, exit string) bool {
	for _, e := range nodeExits[node.Type] {
		if e == exit {
			return true
		}
	}
	return node.Type == NodeIVR && isDigitExit(exit)
}

// isDigitExit reports whether an exit names a DTMF selection.
func isDigitExit(exit string) bool {
	if exit == "" {
		return false
	}
	for _, r := range exit {
		if !strings.ContainsRune("0123456789*#", r) {
			return false
		}
	}
	return true
}

func hasDigitExit(node *Node) bool {
	for exit := range node.Exits {
		if isDigitExit(exit) {
			return true
		}
	}
	return false
}

func expectedExits(node *Node) []string {
	out := DeclaredExits(node.Type)
	if node.Type == NodeIVR {
		out = append(out, "a digit such as \"1\"")
	}
	return out
}

// checkEntry validates the type-specific half of a node, including that every
// dial target resolves and could be dialed at all.
func checkEntry(tenant, path string, table *RoutingTable, node *Node) Problems {
	var ps Problems
	add := func(field, msg string) {
		p := path
		if field != "" {
			p += ".entry." + field
		}
		ps = append(ps, Problem{tenant, p, msg, SeverityError})
	}

	switch e := node.DecodedEntry().(type) {
	case *IVREntry:
		if err := e.Prompt.validate(); err != nil {
			add("prompt", err.Error())
		}
		if e.TimeoutMs < 0 {
			add("timeout_ms", "timeout must not be negative")
		}
		if e.MaxRetries < 0 {
			add("max_retries", "max_retries must not be negative")
		}
		if len(e.Terminator) > 1 || (e.Terminator != "" && !isDigitExit(e.Terminator)) {
			add("terminator", fmt.Sprintf("terminator %q must be a single DTMF digit", e.Terminator))
		}

	case *TTSEntry:
		if strings.TrimSpace(e.Text) == "" {
			add("text", "tts node has no text to speak")
		}

	case *PlayAudioEntry:
		if strings.TrimSpace(e.File) == "" {
			add("file", "play_audio node names no file")
		}

	case *DialUserEntry:
		ps = append(ps, checkDialTarget(tenant, path, table, e.Target, false)...)
		if e.TimeoutMs < 0 {
			add("timeout_ms", "timeout must not be negative")
		}

	case *DialExternalEntry:
		ps = append(ps, checkDialTarget(tenant, path, table, e.Target, true)...)
		if e.TimeoutMs < 0 {
			add("timeout_ms", "timeout must not be negative")
		}

	case *TransferEntry:
		ps = append(ps, checkDialTarget(tenant, path, table, e.Target, false)...)
		if mode := strings.TrimSpace(e.Mode); mode != "" && mode != "blind" {
			add("mode", fmt.Sprintf(
				"transfer mode %q is not supported; only \"blind\" is, and attended transfer is a "+
					"separate feature rather than a variant of this one", mode))
		}
	}

	return ps
}

// checkDialTarget checks a destination names something the tenant has. It does
// NOT check registration: that is runtime state, and a load-time registration
// check would fail every boot before a single phone has registered. An
// unregistered extension is a runtime "unavailable" exit, not a config error.
func checkDialTarget(tenant, path string, table *RoutingTable, target string, externalOnly bool) Problems {
	var ps Problems
	err := func(msg string) Problems {
		return append(ps, Problem{tenant, path + ".entry.target", msg, SeverityError})
	}

	target = strings.TrimSpace(target)
	if target == "" {
		return err("node names no dial target")
	}

	if externalOnly {
		// dial_external takes symbolic names ONLY. This is the narrowing that
		// stops a configuration file from expressing a raw premium-rate number.
		if strings.HasPrefix(target, "user/") {
			return err(fmt.Sprintf(
				"dial_external target %q is an internal extension; use dial_user for that", target))
		}
		if _, isGroup := IsGroupTarget(target); isGroup {
			return err(fmt.Sprintf(
				"dial_external target %q is a ring group; use dial_user for that", target))
		}
		if looksLikeRawNumber(target) {
			return err(fmt.Sprintf(
				"dial_external target %q looks like a raw number; only symbolic names from "+
					"symbolic_targets are dialable, so that editing a flow cannot reach an "+
					"arbitrary destination", target))
		}
		if table == nil || table.SymbolicTargets[target] == "" {
			return err(fmt.Sprintf(
				"dial_external target %q is not defined in the tenant's symbolic_targets", target))
		}
		return ps
	}

	if name, isGroup := IsGroupTarget(target); isGroup {
		if table == nil {
			return err(fmt.Sprintf("target names ring group %q but the tenant has no routing table", name))
		}
		if _, ok := table.Groups[name]; !ok {
			return err(fmt.Sprintf("target names ring group %q, which the tenant does not define", name))
		}
		return ps
	}

	if strings.HasPrefix(target, "user/") {
		if strings.TrimSpace(strings.TrimPrefix(target, "user/")) == "" {
			return err("target is \"user/\" with no extension")
		}
		return ps
	}

	// A bare symbolic name is allowed for dial_user too, as long as it exists.
	if table != nil && table.SymbolicTargets[target] != "" {
		return ps
	}
	return err(fmt.Sprintf(
		"target %q is neither user/<ext>, group/<name>, nor a defined symbolic target", target))
}

// looksLikeRawNumber reports whether a target is a dialable number rather than a
// name. Used only to give a better error than "not a symbolic target".
func looksLikeRawNumber(s string) bool {
	s = strings.TrimPrefix(strings.TrimSpace(s), "+")
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// --- Graph algorithms ---

func sortedNodeIDs(flow *FlowDef) []string {
	out := make([]string, 0, len(flow.Nodes))
	for id := range flow.Nodes {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// edgesOf returns a node's outgoing edges, sorted so cycle reports are stable.
// Terminal exits contribute no edge, which is exactly why the graph can be
// acyclic while still expressing "ring, and on busy try someone else".
func edgesOf(flow *FlowDef, id string) []string {
	node := flow.Nodes[id]
	if node == nil {
		return nil
	}
	out := make([]string, 0, len(node.Exits))
	for exit, target := range node.Exits {
		if IsTerminalExit(node.Type, exit) {
			continue
		}
		if _, ok := flow.Nodes[target]; ok {
			out = append(out, target)
		}
	}
	sort.Strings(out)
	return out
}

func reachableFrom(flow *FlowDef, start string) map[string]bool {
	seen := map[string]bool{}
	queue := []string{start}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if seen[id] {
			continue
		}
		seen[id] = true
		queue = append(queue, edgesOf(flow, id)...)
	}
	return seen
}

// findCycle returns the node path of a cycle, or nil. Reporting the path rather
// than merely "cycle detected" is the difference between fixing it and hunting
// for it across thirty nodes.
func findCycle(flow *FlowDef) []string {
	const (
		white = 0
		grey  = 1
		black = 2
	)
	colour := map[string]int{}
	var stack []string

	var visit func(string) []string
	visit = func(id string) []string {
		colour[id] = grey
		stack = append(stack, id)

		for _, next := range edgesOf(flow, id) {
			switch colour[next] {
			case grey:
				// Found it: slice the stack from where this node first appears.
				for i, s := range stack {
					if s == next {
						return append(append([]string(nil), stack[i:]...), next)
					}
				}
				return []string{next, next}
			case white:
				if cycle := visit(next); cycle != nil {
					return cycle
				}
			}
		}

		stack = stack[:len(stack)-1]
		colour[id] = black
		return nil
	}

	for _, id := range sortedNodeIDs(flow) {
		if colour[id] == white {
			if cycle := visit(id); cycle != nil {
				return cycle
			}
		}
	}
	return nil
}
