package server

import (
	"fmt"
	"log/slog"
	"net/http"

	types "github.com/sebas/switchboard/api/types/v1"
)

// handleConfigAudioPartial renders the audio inventory: what the flows ask for,
// and what the player actually has. Browse only — nothing here writes.
func (s *Server) handleConfigAudioPartial(w http.ResponseWriter, r *http.Request) {
	c := s.findClient(r.URL.Query().Get("server"))
	if c == nil {
		http.Error(w, "No backend configured", http.StatusServiceUnavailable)
		return
	}

	data := ConfigAudioData{Server: c.Name()}
	listing, err := c.ListAudio(r.Context())
	if err != nil {
		data.Error = fmt.Sprintf("Failed to load audio inventory: %v", err)
	} else {
		data.Listing = listing
		data.Unavailable = listing.Source == "unavailable"
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.RenderConfigAudio(w, data); err != nil {
		slog.Error("[UI] Failed to render audio inventory", "error", err)
	}
}

// ConfigAudioData holds the audio inventory.
type ConfigAudioData struct {
	Server  string
	Listing types.AudioListing
	// Unavailable means the RTP manager could not be asked. Every reference is
	// then "unknown" rather than "missing" — the UI must not accuse a file of
	// being absent when the lookup is what failed.
	Unavailable bool
	Error       string
}
