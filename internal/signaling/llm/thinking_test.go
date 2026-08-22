package llm

import "testing"

// Content is filtered rather than trusted. Every row here is a shape a real model
// has produced, and the failure mode for any of them is a caller hearing the
// model's scratchpad read aloud.
func TestStripThinkTags(t *testing.T) {
	tests := []struct {
		name         string
		in           string
		wantContent  string
		wantThinking string
	}{
		{"no tags", "Thanks for calling Acme.", "Thanks for calling Acme.", ""},
		{"leading span", "<think>route to sales</think>One moment.", "One moment.", "route to sales"},
		{"trailing span", "One moment.<think>route to sales</think>", "One moment.", "route to sales"},
		{"span in the middle", "a<think>x</think>b", "ab", "x"},
		{"multiple spans", "a<think>x</think>b<think>y</think>c", "abc", "x\ny"},
		// A response cut off by a token limit: everything after the open tag is
		// reasoning, and none of it is spoken. Failing open here is the leak.
		{"unterminated open tag", "<think>still deciding", "", "still deciding"},
		{"unterminated after content", "Hello.<think>now what", "Hello.", "now what"},
		// Some templates pre-fill the opening tag, so the model emits only the
		// closer.
		{"orphaned close tag", "route to sales</think>One moment.", "One moment.", "route to sales"},
		{"tags spanning newlines", "<think>line one\nline two</think>Hi.", "Hi.", "line one\nline two"},
		{"whitespace is trimmed", "  <think> x </think>  Hi.  ", "Hi.", "x"},
		{"empty span", "<think></think>Hi.", "Hi.", ""},
		{"empty input", "", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotContent, gotThinking := stripThinkTags(tt.in)
			if gotContent != tt.wantContent {
				t.Errorf("content = %q, want %q", gotContent, tt.wantContent)
			}
			if gotThinking != tt.wantThinking {
				t.Errorf("thinking = %q, want %q", gotThinking, tt.wantThinking)
			}
		})
	}
}

// The overwhelmingly common case must not pay for the rare one.
func TestStripThinkTagsLeavesOrdinaryContentIdentical(t *testing.T) {
	const in = "Thanks for calling Acme. Who would you like to speak to?"
	got, thinking := stripThinkTags(in)
	if got != in {
		t.Errorf("content = %q, want it returned unchanged", got)
	}
	if thinking != "" {
		t.Errorf("thinking = %q, want empty", thinking)
	}
}

func TestJoinThinking(t *testing.T) {
	tests := []struct {
		name  string
		parts []string
		want  string
	}{
		{"all empty", []string{"", "", ""}, ""},
		{"one value", []string{"", "reasoned", ""}, "reasoned"},
		{"two values joined", []string{"from the field", "from content"}, "from the field\nfrom content"},
		{"whitespace-only is dropped", []string{"   ", "kept"}, "kept"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := joinThinking(tt.parts...); got != tt.want {
				t.Errorf("joinThinking(%q) = %q, want %q", tt.parts, got, tt.want)
			}
		})
	}
}
