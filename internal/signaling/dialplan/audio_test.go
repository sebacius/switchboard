package dialplan

import (
	"encoding/json"
	"testing"
)

// An ivr node's prompt can name a file just as play_audio can, because both use
// PromptSpec. A reference list that misses those misses every menu that plays a
// recording rather than synthesized speech.
func TestAudioReferencesCoversMenuPromptsAndPlayNodes(t *testing.T) {
	var set FlowSet
	err := json.Unmarshal([]byte(`{"flows":{"main":{
		"start":"greeting",
		"nodes":{
			"greeting":{"type":"ivr","entry":{"prompt":{"file":"menu.wav"},"max_retries":1},
				"exits":{"1":"closed","timeout":"closed","invalid":"closed","retries_exceeded":"closed"}},
			"closed":{"type":"play_audio","entry":{"file":"afterhours.wav"},"exits":{"done":"bye"}},
			"playlist":{"type":"ivr","entry":{"prompt":{"files":["a.wav","b.wav"]},"max_retries":1},
				"exits":{"timeout":"bye","invalid":"bye","retries_exceeded":"bye"}},
			"bye":{"type":"hangup","entry":{}}
		}}}}`), &set)
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}

	refs := AudioReferences("acme", &set)
	got := map[string]string{}
	for _, r := range refs {
		got[r.File] = r.NodeType
	}

	for file, wantType := range map[string]string{
		"menu.wav":       "ivr",
		"afterhours.wav": "play_audio",
		"a.wav":          "ivr",
		"b.wav":          "ivr",
	} {
		if got[file] != wantType {
			t.Errorf("%s: expected a %s reference, got %q", file, wantType, got[file])
		}
	}
	if len(refs) != 4 {
		t.Errorf("expected 4 references, got %d: %+v", len(refs), refs)
	}
}

func TestAudioReferencesHandlesATenantWithNoFlows(t *testing.T) {
	if refs := AudioReferences("acme", nil); refs != nil {
		t.Errorf("a tenant with no flows references nothing: %+v", refs)
	}
}
