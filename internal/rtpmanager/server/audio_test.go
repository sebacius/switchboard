package server

import (
	"path/filepath"
	"testing"
)

// --audio-path was parsed into the config and then never read, so "welcome.wav"
// in a flow was opened relative to whatever directory the process happened to
// start in. These are the cases that fix has to get right.
func TestResolveAudio(t *testing.T) {
	base := filepath.Join(string(filepath.Separator), "srv", "audio")
	s := &Server{config: &Config{AudioBasePath: base}}

	tests := []struct {
		name string
		in   string
		want string
	}{
		{"a plain name lands in the audio directory", "welcome.wav", filepath.Join(base, "welcome.wav")},
		{"a subdirectory is kept", "acme/welcome.wav", filepath.Join(base, "acme", "welcome.wav")},
		{"an absolute path is honored as written", "/mnt/prompts/x.wav", "/mnt/prompts/x.wav"},
		{"an empty name stays empty", "", ""},
		// A tenant's flow file is operator-supplied, but it should not be a way
		// to read outside the prompt library.
		{"traversal cannot escape", "../../etc/passwd", filepath.Join(base, "etc", "passwd")},
		// An absolute path is honored whatever it cleans to, because honoring
		// absolute paths at all is what makes that so. See resolveAudio.
		{"an absolute path is not reinterpreted", "/../../etc/passwd", "/../../etc/passwd"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := s.resolveAudio(tc.in); got != tc.want {
				t.Errorf("resolveAudio(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// With no audio directory configured, names pass through exactly as before, so
// a deployment that never set one behaves identically.
func TestResolveAudioWithoutABasePath(t *testing.T) {
	s := &Server{config: &Config{}}
	if got := s.resolveAudio("welcome.wav"); got != "welcome.wav" {
		t.Errorf("expected the name unchanged, got %q", got)
	}
}
