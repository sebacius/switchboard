package server

import (
	"context"
	"log/slog"
	"net/http"

	types "github.com/sebas/switchboard/api/types/v1"
	"github.com/sebas/switchboard/internal/ui/client"
)

// audioVM builds the audio inventory, for the page and the fragment.
func (s *Server) audioVM(ctx context.Context, c *client.Client) ConfigAudioData {
	data := ConfigAudioData{Server: c.Name()}
	listing, err := c.ListAudio(ctx)
	if err != nil {
		data.Failure = loadFailed(c, "the audio inventory", err)
		return data
	}
	data.Listing = listing
	data.Unavailable = listing.Source == "unavailable"
	return data
}

// handleConfigAudioPartial renders the audio inventory: what the flows ask for,
// and what the player actually has. Browse only — nothing here writes.
func (s *Server) handleConfigAudioPartial(w http.ResponseWriter, r *http.Request) {
	c := s.requireBackendPartial(w)
	if c == nil {
		return
	}

	data := s.audioVM(r.Context(), c)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.RenderConfigAudio(w, data); err != nil {
		slog.Error("[UI] Failed to render audio inventory", "error", err)
	}
}

// ConfigAudioData holds the audio inventory.
type ConfigAudioData struct {
	// Failure is a read that did not happen, as distinct from a read that came
	// back empty. Error stays for everything else — a refused write, a file that
	// parsed badly — because those were read fine, and blaming the backend for
	// them would be a second lie.
	Failure *LoadFailure
	Layout  LayoutData
	Server  string
	Listing types.AudioListing
	// Unavailable means the RTP manager could not be asked. Every reference is
	// then "unknown" rather than "missing" — the UI must not accuse a file of
	// being absent when the lookup is what failed.
	Unavailable bool
	Error       string
}
