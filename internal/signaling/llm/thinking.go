package llm

import "strings"

// Reasoning must never be spoken to a caller. Where a provider returns it as its
// own field that is easy. Where a model folds it into content — as <think> tags,
// which is what an OpenAI-compatible gateway serving a reasoning model does, and
// what qwen3:4b was measured doing on Ollama even with think:false — content has
// to be filtered rather than trusted.

const (
	thinkOpen  = "<think>"
	thinkClose = "</think>"
)

// stripThinkTags separates reasoning a model folded into content from the
// content itself, returning (content, thinking) with both trimmed.
//
// It fails CLOSED: an opening tag with no closer — which is what a response
// truncated by a token limit looks like — is treated as reasoning through to the
// end of the string. Failing open there would leak exactly what this exists to
// prevent. A closing tag with no opener is the same story from the other side:
// some templates pre-fill the opening tag, so everything before the first
// </think> is reasoning.
func stripThinkTags(s string) (string, string) {
	// Overwhelmingly the common case: nothing to do, nothing allocated.
	if !strings.Contains(s, thinkOpen) && !strings.Contains(s, thinkClose) {
		return s, ""
	}

	var content, thinking strings.Builder
	rest := s

	// Orphaned close first: if a </think> arrives before any <think>, the model
	// was primed with the opening tag and everything up to here is reasoning.
	if close := strings.Index(rest, thinkClose); close >= 0 {
		open := strings.Index(rest, thinkOpen)
		if open < 0 || close < open {
			thinking.WriteString(rest[:close])
			rest = rest[close+len(thinkClose):]
		}
	}

	for {
		open := strings.Index(rest, thinkOpen)
		if open < 0 {
			content.WriteString(rest)
			break
		}
		content.WriteString(rest[:open])
		rest = rest[open+len(thinkOpen):]

		close := strings.Index(rest, thinkClose)
		if close < 0 {
			// Unterminated: the rest is reasoning, and none of it is spoken.
			thinking.WriteString(rest)
			break
		}
		thinking.WriteString(rest[:close])
		thinking.WriteString("\n")
		rest = rest[close+len(thinkClose):]
	}

	return strings.TrimSpace(content.String()), strings.TrimSpace(thinking.String())
}

// joinThinking merges reasoning from a provider's own field with reasoning
// recovered from content, dropping whichever is empty.
func joinThinking(parts ...string) string {
	kept := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, "\n")
}
