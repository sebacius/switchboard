package dialplan

import "sort"

// Which audio files a tenant's flows actually name.
//
// Two node types reference audio, not one: play_audio names a file directly,
// and an ivr node's prompt can too — PromptSpec is shared, so a menu plays a
// recorded prompt without needing a separate node in front of it. Listing only
// play_audio would under-report every such menu, which is the case an operator
// most wants checked.

// AudioRef is one place a flow names an audio file.
type AudioRef struct {
	Tenant   string `json:"tenant"`
	Flow     string `json:"flow"`
	Node     string `json:"node"`
	NodeType string `json:"node_type"`
	File     string `json:"file"`
}

// AudioReferences returns every audio file a tenant's flows name, sorted by
// file and then by where it is referenced, so the list is stable between calls.
func AudioReferences(tenant string, set *FlowSet) []AudioRef {
	if set == nil {
		return nil
	}

	var refs []AudioRef
	add := func(flow, node string, kind NodeType, files ...string) {
		for _, f := range files {
			if f == "" {
				continue
			}
			refs = append(refs, AudioRef{
				Tenant:   tenant,
				Flow:     flow,
				Node:     node,
				NodeType: string(kind),
				File:     f,
			})
		}
	}

	for flowName, def := range set.Flows {
		if def == nil {
			continue
		}
		for nodeID, node := range def.Nodes {
			if node == nil {
				continue
			}
			switch entry := node.DecodedEntry().(type) {
			case *PlayAudioEntry:
				add(flowName, nodeID, node.Type, entry.File)
			case *IVREntry:
				add(flowName, nodeID, node.Type, entry.Prompt.File)
				add(flowName, nodeID, node.Type, entry.Prompt.Files...)
			}
		}
	}

	sort.Slice(refs, func(i, j int) bool {
		a, b := refs[i], refs[j]
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Flow != b.Flow {
			return a.Flow < b.Flow
		}
		return a.Node < b.Node
	})
	return refs
}
