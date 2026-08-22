package dialplan

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// A flow is a directed graph of nodes that routes a call without a language
// model. Every node has the same shape — a type, a type-specific entry, and a
// map from declared exit name to the node that exit leads to — so one validator
// can check every flow ever written.
//
// Two rules give the graph its useful property. The inter-node graph is acyclic,
// and repetition lives INSIDE a node bounded by a counter (an ivr node's
// max_retries is a bounded self-loop that contributes no edge). Together they
// make every flow provably terminating, which no priority-ordered dialplan can
// offer: a Goto loop is found in production, an inter-node cycle is found at
// load.
//
// Exit names are fixed here in Go rather than in configuration. That is what
// makes both "this exit does not exist for this type" and "this exit exists but
// is not wired" detectable before a call arrives, instead of a typo like
// "no-answer" becoming a silently dead branch discovered during an outage.

// flowFileSuffix is the per-tenant flow file's extension, so "default" is
// described by default.routing.json and default.flows.json.
const flowFileSuffix = ".flows.json"

// DefaultFlowTimeoutMs bounds a whole flow when it declares no deadline of its
// own. It is generous — a caller navigating menus is not idle — but finite, so a
// call can never be held open forever by a flow that stops making progress.
const DefaultFlowTimeoutMs = 300000

// MaxHops bounds how many nodes one call may traverse. Acyclicity is proved at
// load, so this is unreachable in a correct system; it exists so that a defect
// in validation degrades to a dropped call rather than a call that never ends.
const MaxHops = 100

// NodeType is the closed set of things a flow node can be.
type NodeType string

const (
	// NodeIVR plays a prompt and collects a digit, with its own bounded retries.
	NodeIVR NodeType = "ivr"
	// NodeTTS speaks text.
	NodeTTS NodeType = "tts"
	// NodePlayAudio plays an audio file.
	NodePlayAudio NodeType = "play_audio"
	// NodeDialUser dials a directory extension or a ring group.
	NodeDialUser NodeType = "dial_user"
	// NodeDialExternal dials a symbolic external destination.
	NodeDialExternal NodeType = "dial_external"
	// NodeTransfer performs a blind transfer.
	NodeTransfer NodeType = "transfer"
	// NodeHangup ends the call with a stated cause.
	NodeHangup NodeType = "hangup"
)

// nodeExits is the exit contract: every exit a node type can produce, other than
// the terminal ones. A flow MUST wire all of them — there are no defaults,
// because "what happens when the line is busy" should never be a question the
// reader of a flow has to answer by reading Go.
var nodeExits = map[NodeType][]string{
	NodeIVR:          {"timeout", "invalid", "retries_exceeded"},
	NodeTTS:          {"done"},
	NodePlayAudio:    {"done"},
	NodeDialUser:     {"no_answer", "busy", "rejected", "unavailable"},
	NodeDialExternal: {"no_answer", "busy", "denied", "failed"},
	NodeTransfer:     {"failed"},
	NodeHangup:       nil,
}

// terminalExits are the outcomes that END a flow: the call is connected and the
// cursor is released. They are NOT declarable in configuration — the graph
// cannot express "after the bridge ends, do X", and pretending otherwise would
// mean an exit that silently never fires.
var terminalExits = map[NodeType][]string{
	NodeDialUser:     {"answered"},
	NodeDialExternal: {"answered"},
	NodeTransfer:     {"accepted"},
}

// IsTerminalExit reports whether an exit ends the flow for a given node type.
func IsTerminalExit(t NodeType, exit string) bool {
	for _, e := range terminalExits[t] {
		if e == exit {
			return true
		}
	}
	return false
}

// DeclaredExits returns the exits a node type must wire, sorted for stable
// error messages.
func DeclaredExits(t NodeType) []string {
	out := append([]string(nil), nodeExits[t]...)
	sort.Strings(out)
	return out
}

// KnownNodeType reports whether a type is in the closed set.
func KnownNodeType(t NodeType) bool {
	_, ok := nodeExits[t]
	return ok
}

// NodeTypes returns every valid node type, sorted, for error messages that tell
// an operator what they could have written instead.
func NodeTypes() []string {
	out := make([]string, 0, len(nodeExits))
	for t := range nodeExits {
		out = append(out, string(t))
	}
	sort.Strings(out)
	return out
}

// PromptSpec is what a caller hears. One shared type across tts, play_audio, and
// an ivr node's prompt, so a menu needs no separate node in front of it just to
// say something.
//
// Exactly one of Text or File/Files is set; the validator enforces that rather
// than silently preferring one.
type PromptSpec struct {
	// Text is synthesised by the TTS server.
	Text string `json:"text,omitempty"`
	// Voice names the TTS voice; empty uses the server default.
	Voice string `json:"voice,omitempty"`
	// File is a single audio file, relative to the audio directory.
	File string `json:"file,omitempty"`
	// Files is a playlist played in order.
	Files []string `json:"files,omitempty"`
}

// IsZero reports whether the prompt says nothing at all.
func (p PromptSpec) IsZero() bool {
	return strings.TrimSpace(p.Text) == "" && strings.TrimSpace(p.File) == "" && len(p.Files) == 0
}

// validate checks a prompt names exactly one source of audio.
func (p PromptSpec) validate() error {
	sources := 0
	if strings.TrimSpace(p.Text) != "" {
		sources++
	}
	if strings.TrimSpace(p.File) != "" {
		sources++
	}
	if len(p.Files) > 0 {
		sources++
	}
	switch {
	case sources == 0:
		return fmt.Errorf("prompt says nothing: set one of text, file, or files")
	case sources > 1:
		return fmt.Errorf("prompt sets more than one of text, file, and files; pick one")
	}
	if strings.TrimSpace(p.Voice) != "" && strings.TrimSpace(p.Text) == "" {
		return fmt.Errorf("prompt sets a voice but no text; voice only applies to synthesised speech")
	}
	return nil
}

// --- Per-type entry structs ---

// IVREntry plays a prompt and collects one selection.
type IVREntry struct {
	Prompt PromptSpec `json:"prompt"`
	// TimeoutMs bounds how long to wait for the caller's first digit.
	TimeoutMs int `json:"timeout_ms"`
	// MaxRetries bounds the node's internal re-prompt loop. This is the only
	// legal repetition in a flow, and it is what keeps the graph acyclic.
	MaxRetries int `json:"max_retries"`
	// Terminator ends collection early when the caller presses it.
	Terminator string `json:"terminator,omitempty"`
	// Interruptible lets the first digit cut the prompt short.
	Interruptible bool `json:"interruptible,omitempty"`
}

// TTSEntry speaks text.
type TTSEntry struct {
	Text          string `json:"text"`
	Voice         string `json:"voice,omitempty"`
	Interruptible bool   `json:"interruptible,omitempty"`
}

// PlayAudioEntry plays a file.
type PlayAudioEntry struct {
	File          string `json:"file"`
	Interruptible bool   `json:"interruptible,omitempty"`
}

// DialUserEntry dials an extension or a ring group.
type DialUserEntry struct {
	Target    string `json:"target"`
	TimeoutMs int    `json:"timeout_ms"`
}

// DialExternalEntry dials a symbolic external destination. The target is
// symbolic ONLY: a flow cannot express a raw number, which is the narrowing that
// stops a config edit from reaching a premium-rate line.
type DialExternalEntry struct {
	Target    string `json:"target"`
	TimeoutMs int    `json:"timeout_ms"`
}

// TransferEntry performs a blind transfer.
type TransferEntry struct {
	Target string `json:"target"`
	// Mode is "blind". Attended transfer is a different feature and is not
	// expressible here; the field exists so a config that asks for it is
	// rejected by name rather than silently performing a blind one.
	Mode string `json:"mode,omitempty"`
}

// HangupEntry ends the call.
type HangupEntry struct {
	Cause string `json:"cause,omitempty"`
}

// Node is one step in a flow. Entry is decoded into the type-specific struct at
// LOAD, never per call, so a malformed entry is a startup error rather than a
// surprise three menus deep.
type Node struct {
	// ID is the node's key in the flow, filled in at load so a node can be
	// identified from the node alone.
	ID string `json:"-"`

	Type  NodeType          `json:"type"`
	Entry json.RawMessage   `json:"entry,omitempty"`
	Exits map[string]string `json:"exits,omitempty"`

	// entry holds the decoded entry. Decoding happens at load so a malformed
	// entry is a startup error rather than a surprise three menus deep.
	entry     any
	entryOnce sync.Once
}

// DecodedEntry returns the type-specific entry.
//
// It decodes on demand if the load path has not already done so. That path
// always validates, and validation decodes — but a Node is an exported type and
// a caller who builds one directly should get a working node rather than one
// that silently does nothing.
func (n *Node) DecodedEntry() any {
	if n.entry == nil {
		n.entryOnce.Do(func() { _ = n.decodeEntry() })
	}
	return n.entry
}

// decodeEntry decodes Entry into the struct for this node's type, rejecting
// unknown fields. DisallowUnknownFields is the point: without it a typo like
// "timout_ms" defaults the real field to zero, and a menu that should wait five
// seconds silently waits none.
func (n *Node) decodeEntry() error {
	if n.entry != nil {
		return nil
	}
	target, err := entryTarget(n.Type)
	if err != nil {
		return err
	}
	if len(n.Entry) == 0 {
		// hangup is the only node that needs no entry at all.
		if n.Type == NodeHangup {
			n.entry = &HangupEntry{}
			return nil
		}
		return fmt.Errorf("node type %q requires an entry", n.Type)
	}

	dec := json.NewDecoder(bytes.NewReader(n.Entry))
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		return fmt.Errorf("entry: %w", err)
	}
	n.entry = target
	return nil
}

// entryTarget returns a fresh entry struct for a node type.
func entryTarget(t NodeType) (any, error) {
	switch t {
	case NodeIVR:
		return &IVREntry{}, nil
	case NodeTTS:
		return &TTSEntry{}, nil
	case NodePlayAudio:
		return &PlayAudioEntry{}, nil
	case NodeDialUser:
		return &DialUserEntry{}, nil
	case NodeDialExternal:
		return &DialExternalEntry{}, nil
	case NodeTransfer:
		return &TransferEntry{}, nil
	case NodeHangup:
		return &HangupEntry{}, nil
	default:
		return nil, fmt.Errorf("unknown node type %q (want one of %s)", t, strings.Join(NodeTypes(), ", "))
	}
}

// FlowDef is one named flow.
type FlowDef struct {
	// Start names the node execution begins at.
	Start string `json:"start"`
	// TimeoutMs bounds the whole flow. Zero inherits DefaultFlowTimeoutMs.
	TimeoutMs int `json:"timeout_ms,omitempty"`
	// Nodes is the graph, keyed by node ID.
	Nodes map[string]*Node `json:"nodes"`
}

// FlowTimeout returns the flow's deadline, defaulted.
func (f *FlowDef) FlowTimeout() int {
	if f == nil || f.TimeoutMs <= 0 {
		return DefaultFlowTimeoutMs
	}
	return f.TimeoutMs
}

// FlowSet is one tenant's flows, as loaded from <tenant>.flows.json.
type FlowSet struct {
	Flows map[string]*FlowDef `json:"flows"`
}

// Flow returns a flow by name.
func (s *FlowSet) Flow(name string) (*FlowDef, bool) {
	if s == nil {
		return nil, false
	}
	f, ok := s.Flows[name]
	return f, ok && f != nil
}

// Names returns every flow name, sorted.
func (s *FlowSet) Names() []string {
	if s == nil {
		return nil
	}
	out := make([]string, 0, len(s.Flows))
	for name := range s.Flows {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
